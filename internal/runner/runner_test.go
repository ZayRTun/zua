package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
