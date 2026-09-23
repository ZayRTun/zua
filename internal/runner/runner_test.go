package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unreal-agent-tui/internal/settings"
	"unreal-agent-tui/internal/testsrv"
)

// hermeticEnv returns a getenv that pins HOME to an empty temp dir (so skill
// discovery stays hermetic) and wires the fake LLM server.
func hermeticEnv(t *testing.T, serverURL string) func(string) string {
	t.Helper()
	home := t.TempDir()
	return func(key string) string {
		switch key {
		case "HOME":
			return home
		case "OPENAI_API_KEY":
			return "test-key"
		case "OPENAI_BASE_URL":
			return serverURL
		case "OPENCODE_PROVIDER":
			// The global default provider is opencode-go; most tests still
			// pin the openai family (the chat-completions tests use the
			// settings file instead).
			return "openai"
		}
		return os.Getenv(key)
	}
}

func scanEvents(stdout string, visit func(line string)) {
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		visit(scanner.Text())
	}
}

func TestRunEndToEndFileToolFlow(t *testing.T) {
	server := testsrv.New(t)
	defer server.Close()
	getenv := hermeticEnv(t, server.URL)

	workspace := t.TempDir()
	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `create hello.txt with a greeting`}, getenv, &stdout, os.Stderr)
	if code != 0 {
		t.Fatalf("Run exit code %d, stdout:\n%s", code, stdout.String())
	}

	content, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("agent did not write hello.txt: %v\nstdout:\n%s", err, stdout.String())
	}
	if string(content) != "hi from the agent" {
		t.Fatalf("hello.txt = %q", content)
	}

	var sawMeta, sawToolStatus, sawAssistant bool
	var sessionID string
	scanEvents(stdout.String(), func(line string) {
		if strings.Contains(line, `"type":"meta"`) {
			sawMeta = true
			var meta struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(line), &meta); err == nil {
				sessionID = meta.SessionID
			}
		}
		if strings.Contains(line, `"Kind":"tool_call_status"`) && strings.Contains(line, `"ID"`) {
			sawToolStatus = true
		}
		if strings.Contains(line, `"Kind":"model_response"`) && strings.Contains(line, `"Text":"done"`) {
			sawAssistant = true
		}
	})
	if !sawMeta || !sawToolStatus || !sawAssistant {
		t.Fatalf("expected meta, tool status, and assistant events; stdout:\n%s", stdout.String())
	}
	if sessionID == "" {
		t.Fatal("meta event missing session_id")
	}

	// Resume: a second run reusing the session id must continue the same
	// session (the tool set is constant, so restore validates cleanly).
	var stdout2 bytes.Buffer
	request := `{"prompt":"continue","session_id":"` + sessionID + `"}`
	code = Run(t.Context(), []string{"-workspace", workspace, request}, getenv, &stdout2, os.Stderr)
	if code != 0 {
		t.Fatalf("resume exit code %d, stdout:\n%s", code, stdout2.String())
	}
	if !strings.Contains(stdout2.String(), `"session_id":"`+sessionID+`"`) {
		t.Fatalf("resume did not reuse session %s:\n%s", sessionID, stdout2.String())
	}
}

// settingsFileFor writes a settings.json under the fake HOME and returns a
// getenv fake exposing only that HOME — no provider env vars at all, so the
// run must take its configuration from the settings file.
func settingsFileFor(t *testing.T, contents string) func(string) string {
	t.Helper()
	home := t.TempDir()
	path := settings.Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
}

func TestRunOpenCodeGoEndToEnd(t *testing.T) {
	server, snapshot := testsrv.NewChatCompletions(t)
	getenv := settingsFileFor(t, fmt.Sprintf(
		`{"provider":"opencode-go","api_key":"oc_test","base_url":%q,"model":"glm-5.3-flash","thinking_level":"high"}`, server.URL))

	workspace := t.TempDir()
	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `create hello.txt with a greeting`}, getenv, &stdout, os.Stderr)
	if code != 0 {
		t.Fatalf("Run exit code %d, stdout:\n%s", code, stdout.String())
	}
	if _, err := os.ReadFile(filepath.Join(workspace, "hello.txt")); err != nil {
		t.Fatalf("agent did not write hello.txt: %v\nstdout:\n%s", err, stdout.String())
	}

	// Every request carried zua's session id and the settings key.
	exchanges := snapshot()
	if len(exchanges) != 2 {
		t.Fatalf("exchanges = %d, want 2 (tool turn + final)", len(exchanges))
	}
	sessionID := ""
	for _, exchange := range exchanges {
		if got := exchange.Header.Get("x-opencode-session"); got == "" {
			t.Fatal("request missing x-opencode-session")
		} else if sessionID == "" {
			sessionID = got
		} else if sessionID != got {
			t.Fatalf("session header changed between requests: %q vs %q", sessionID, got)
		}
		if got := exchange.Header.Get("Authorization"); got != "Bearer oc_test" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := json.Unmarshal(exchange.Body, &body); err != nil || body.ReasoningEffort != "high" {
			t.Fatalf("reasoning_effort = %q (err %v)", body.ReasoningEffort, err)
		}
	}

	// The meta event carries the resolved model; the reasoning item and the
	// usage buckets survive into the JSONL.
	var sawReasoning, sawUsage, sawCached, sawCacheWriteField bool
	scanEvents(stdout.String(), func(line string) {
		var event struct {
			Type string          `json:"type"`
			Kind string          `json:"Kind"`
			Data json.RawMessage `json:"Data"`
		}
		_ = json.Unmarshal([]byte(line), &event)
		if event.Kind == "model_response" {
			var payload struct {
				Response struct {
					Output []struct {
						Type string `json:"Type"`
					} `json:"Output"`
					Usage struct {
						InputTokens           int64 `json:"InputTokens"`
						CachedInputTokens     int64 `json:"CachedInputTokens"`
						CacheWriteInputTokens int64 `json:"CacheWriteInputTokens"`
						OutputTokens          int64 `json:"OutputTokens"`
					} `json:"Usage"`
				} `json:"Response"`
			}
			if err := json.Unmarshal(event.Data, &payload); err == nil {
				for _, item := range payload.Response.Output {
					if item.Type == "reasoning" {
						sawReasoning = true
					}
				}
				usage := payload.Response.Usage
				if usage.InputTokens > 0 && usage.OutputTokens > 0 {
					sawUsage = true
				}
				if usage.CachedInputTokens > 0 {
					sawCached = true
				}
				// The Usage Line's cache buckets travel in the wire format
				// (issue #13): the cache-write bucket is present even when
				// zero, so consumers can distinguish it from a dropped field.
				if strings.Contains(line, "CacheWriteInputTokens") {
					sawCacheWriteField = true
				}
			}
		}
	})
	if !sawReasoning {
		t.Fatal("JSONL lacks a reasoning item")
	}
	if !sawUsage {
		t.Fatal("JSONL lacks nonzero usage buckets")
	}
	if !sawCached {
		t.Fatal("JSONL lacks cached input tokens")
	}
	if !sawCacheWriteField {
		t.Fatal("JSONL lacks the CacheWriteInputTokens bucket")
	}
}

func TestRunUsesSettingsFile(t *testing.T) {
	server := testsrv.New(t)
	defer server.Close()
	getenv := settingsFileFor(t, fmt.Sprintf(
		`{"provider":"openai","api_key":"test-key","base_url":%q}`, server.URL))

	workspace := t.TempDir()
	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `create hello.txt with a greeting`}, getenv, &stdout, os.Stderr)
	if code != 0 {
		t.Fatalf("Run exit code %d, stdout:\n%s", code, stdout.String())
	}
	if _, err := os.ReadFile(filepath.Join(workspace, "hello.txt")); err != nil {
		t.Fatalf("agent did not write hello.txt: %v\nstdout:\n%s", err, stdout.String())
	}
}

func TestRunMalformedSettingsFileErrors(t *testing.T) {
	getenv := settingsFileFor(t, "{not json")
	workspace := t.TempDir()
	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `hi`}, getenv, &stdout, os.Stderr)
	if code == 0 {
		t.Fatalf("Run must fail on malformed settings, stdout:\n%s", stdout.String())
	}
	var errEvent errorEvent
	if err := json.Unmarshal(stdout.Bytes(), &errEvent); err != nil || errEvent.Type != "error" {
		t.Fatalf("expected error event; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(errEvent.Message, settings.Path(getenv("HOME"))) {
		t.Fatalf("error must name the settings path: %s", errEvent.Message)
	}
}

func TestRunBashFlow(t *testing.T) {
	server := testsrv.NewWithScript(t, "Bash", `{"command":"echo bash-flow"}`)
	defer server.Close()
	getenv := hermeticEnv(t, server.URL)

	workspace := t.TempDir()
	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `run echo bash-flow`}, getenv, &stdout, os.Stderr)
	if code != 0 {
		t.Fatalf("Run exit code %d, stdout:\n%s", code, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"Name":"Bash"`) {
		t.Fatalf("expected a Bash tool call; stdout:\n%s", out)
	}
	if !strings.Contains(out, "bash-flow") {
		t.Fatalf("expected bash output in transcript; stdout:\n%s", out)
	}
}

func TestSkillLocationsEndToEnd(t *testing.T) {
	server, bodies := testsrv.NewCapture(t)

	home := t.TempDir()
	workspace := t.TempDir()

	// Project model-invocable skill (pi project location).
	proj := filepath.Join(workspace, ".agents", "skills", "deploy-check")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "SKILL.md"),
		[]byte("---\nname: deploy-check\ndescription: verify deploys\n---\n\nRun deploy checks.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// User-level flagged skill (pi user location) — must NOT be advertised.
	usr := filepath.Join(home, ".agents", "skills", "journal")
	if err := os.MkdirAll(usr, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usr, "SKILL.md"),
		[]byte("---\nname: journal\ndescription: keep a journal\ndisable-model-invocation: true\n---\n\nWrite a journal entry.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	getenv := hermeticEnv(t, server.URL)
	base := getenv
	getenv = func(key string) string {
		if key == "HOME" {
			return home
		}
		return base(key)
	}

	var stdout bytes.Buffer
	code := Run(t.Context(), []string{"-workspace", workspace, `-p`, `say hi`}, getenv, &stdout, os.Stderr)
	if code != 0 {
		t.Fatalf("Run exit code %d, stdout:\n%s", code, stdout.String())
	}
	if len(*bodies) == 0 {
		t.Fatal("no requests captured")
	}
	first := string((*bodies)[0])
	if !strings.Contains(first, "deploy-check") {
		t.Error("model-invocable skill not advertised to the model")
	}
	if strings.Contains(first, "journal") {
		t.Error("user-invoked (flagged) skill leaked into the request")
	}
	if !strings.Contains(first, "SkillUse") {
		t.Error("SkillUse tool missing from request despite registered skills")
	}
}
