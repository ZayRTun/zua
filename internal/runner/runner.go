// Package runner wires the unreal-agent harness into a headless agent binary
// with the Read/Write/Edit file tools added on top of the built-in tools.
package runner

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/coordinator"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/fireworks"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/ollama"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openai"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openrouter"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
	"github.com/unreallabsai/unreal-agent/harness/tool/viewimage"

	"unreal-agent-tui/internal/filetools"
	"unreal-agent-tui/internal/skills"
)

const (
	defaultProvider = "openai"
	defaultModel    = "gpt-6-astra"
)

const defaultSystemPrompt = `You are an expert pair programmer working directly inside the user's project directory.

## Workflow
- Prefer the Read tool to inspect files; use Bash for builds, tests, and version control.
- Make changes with the Write and Edit tools rather than shell redirection.
- Verify your work (run tests or builds) before declaring it complete.
- Be concise in your final answer.`

// Request is the JSON request accepted on stdin, as an argument, or via -p.
type Request struct {
	Prompt        *string   `json:"prompt"`
	Messages      []Message `json:"messages"`
	SystemPrompt  *string   `json:"system_prompt"`
	Model         string    `json:"model"`
	Provider      string    `json:"provider"`
	ThinkingLevel string    `json:"thinking_level"`
	SessionID     *string   `json:"session_id"`
	FileTools     bool      `json:"file_tools"`
	SkillDirs     []string  `json:"skill_dirs"`
}

// Message is one conversation turn for the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type metaEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Workspace string `json:"workspace"`
}

type errorEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Run parses arguments, runs one agent session, and writes session items to
// output as JSONL. A first {"type":"meta",...} line precedes the items.
func Run(ctx context.Context, args []string, getenv func(string) string, output io.Writer, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	if err := run(ctx, args, getenv, output, stderr); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return 130
		}
		encoded, encodeErr := json.Marshal(errorEvent{Type: "error", Message: err.Error()})
		if encodeErr == nil {
			fmt.Fprintf(output, "%s\n", encoded)
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, getenv func(string) string, output io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("zua-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", ".", "workspace directory the agent operates in")
	prompt := flags.String("p", "", "prompt (or pass a JSON request as the only argument / on stdin)")
	providerFlag := flags.String("provider", "", "llm provider: openai, commandcode, openrouter, fireworks, ollama")
	modelFlag := flags.String("model", "", "model id")
	skillsFlag := flags.String("skills", "", "comma-separated extra skill directories (beyond <workspace>/.harness/skills)")
	fileTools := flags.Bool("file-tools", false, "enable Read/Write/Edit file tools; default is the pristine benchmark configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}

	request, err := parseRequest(flags.Args(), *prompt, stdin())
	if err != nil {
		return err
	}
	if *providerFlag != "" {
		request.Provider = *providerFlag
	}
	if *modelFlag != "" {
		request.Model = *modelFlag
	}
	if *skillsFlag != "" {
		for _, dir := range strings.Split(*skillsFlag, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				request.SkillDirs = append(request.SkillDirs, dir)
			}
		}
	}
	if request.Prompt == nil && len(request.Messages) == 0 {
		return fmt.Errorf("no prompt or messages provided")
	}

	absolute, err := filepath.Abs(*workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	client, model, closeClient, err := newClient(request, getenv)
	if err != nil {
		return err
	}
	defer closeClient()

	storeDirectory := filepath.Join(absolute, ".harness", "sessions")
	store, err := localfile.New(storeDirectory)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	sessionID, restored, err := openSession(ctx, store, request.SessionID)
	if err != nil {
		return err
	}
	useFileTools := *fileTools || request.FileTools
	registry, operations, registeredSkills, err := buildTools(ctx, absolute, storeDirectory, sessionID, useFileTools, request.SkillDirs, getenv("HOME"))
	if err != nil {
		return err
	}
	encodedMeta, err := json.Marshal(metaEvent{
		Type: "meta", SessionID: string(sessionID), Model: model, Workspace: absolute,
	})
	if err != nil {
		return fmt.Errorf("encode meta event: %w", err)
	}
	if _, err := fmt.Fprintf(output, "%s\n", encodedMeta); err != nil {
		return fmt.Errorf("write meta event: %w", err)
	}
	observerID := store.AddObserver(func(_ session.ID, item sessionstore.Item) {
		encoded, err := json.Marshal(item)
		if err != nil {
			return
		}
		fmt.Fprintf(output, "%s\n", encoded)
	})
	defer store.RemoveObserver(observerID)

	inputs, err := inbox.New(ctx, restored.ExternalInputIDs)
	if err != nil {
		return fmt.Errorf("open inbox: %w", err)
	}
	settingsPayload, err := json.Marshal(inbox.ControlMessage{
		Mode: inbox.UpdateSettings,
		Parameters: inbox.Settings{
			ReasoningEffort: reasoningEffort(request.ThinkingLevel),
		},
	})
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := inputs.Submit(ctx, inbox.Input{
		ID: inbox.ID(uuid.New().String()), Kind: inbox.InputControl, Payload: settingsPayload,
	}); err != nil {
		return fmt.Errorf("submit settings: %w", err)
	}
	for _, message := range messageList(request) {
		payload, err := json.Marshal(message.Content)
		if err != nil {
			return fmt.Errorf("encode message: %w", err)
		}
		if err := inputs.Submit(ctx, inbox.Input{
			ID: inbox.ID(uuid.New().String()), Kind: inbox.InputExternal, Payload: payload,
		}); err != nil {
			return fmt.Errorf("submit message: %w", err)
		}
	}
	stopPayload, err := json.Marshal(inbox.ControlMessage{Mode: inbox.StopWhenIdle})
	if err != nil {
		return fmt.Errorf("encode stop: %w", err)
	}
	if err := inputs.Submit(ctx, inbox.Input{
		ID: inbox.ID(uuid.New().String()), Kind: inbox.InputControl, Payload: stopPayload,
	}); err != nil {
		return fmt.Errorf("submit stop: %w", err)
	}

	// Pass the registered skills so the builder advertises them (name +
	// description + path) in the system prompt — the model loads full
	// instructions on demand via the SkillUse tool.
	builder := contextbuilder.NewBuilder(registeredSkills...)
	builder.SetModel(llm.Model{ID: model, ReasoningEffort: reasoningEffort(request.ThinkingLevel)})
	builder.SetSystemPrompt(systemPrompt(request, useFileTools))
	for _, definition := range registry.StaticDefinitions() {
		builder.AddTool(definition.Tool)
	}

	return coordinator.New(coordinator.Dependencies{
		SessionID:      sessionID,
		Inbox:          inputs,
		Restored:       restored,
		Sessions:       store,
		ContextBuilder: builder,
		LLM:            client,
		Tools:          registry,
		Operations:     operations,
	}).Run(ctx)
}

func parseRequest(args []string, prompt string, stdinReader io.Reader) (Request, error) {
	var request Request
	switch {
	case prompt != "":
		request.Prompt = &prompt
	case len(args) == 1 && strings.HasPrefix(strings.TrimSpace(args[0]), "{"):
		if err := json.Unmarshal([]byte(args[0]), &request); err != nil {
			return Request{}, fmt.Errorf("decode request: %w", err)
		}
	default:
		encoded, err := io.ReadAll(stdinReader)
		if err != nil {
			return Request{}, fmt.Errorf("read request: %w", err)
		}
		if len(strings.TrimSpace(string(encoded))) > 0 {
			if err := json.Unmarshal(encoded, &request); err != nil {
				return Request{}, fmt.Errorf("decode request: %w", err)
			}
		}
	}
	for _, message := range request.Messages {
		switch message.Role {
		case "user", "assistant":
		default:
			return Request{}, fmt.Errorf("message role must be user or assistant, got %q", message.Role)
		}
	}
	return request, nil
}

func messageList(request Request) []Message {
	if len(request.Messages) > 0 {
		return request.Messages
	}
	if request.Prompt != nil {
		return []Message{{Role: "user", Content: *request.Prompt}}
	}
	return nil
}

const upstreamSystemPrompt = `You are an AI agent running inside an isolated sandbox container.

## Guidelines
- Save output files to the workspace root.
- For large datasets, inspect a sample first before processing everything.
`

// systemPrompt mirrors upstream's default in pristine mode and uses the
// pair-programming prompt only when the extra file tools are available.
func systemPrompt(request Request, useFileTools bool) string {
	if request.SystemPrompt != nil && strings.TrimSpace(*request.SystemPrompt) != "" {
		return *request.SystemPrompt
	}
	if useFileTools {
		return defaultSystemPrompt
	}
	return upstreamSystemPrompt
}

func openSession(ctx context.Context, store *localfile.Store, requested *string) (session.ID, sessionstore.ResumeState, error) {
	id := session.ID(uuid.New().String())
	if requested != nil && strings.TrimSpace(*requested) != "" {
		id = session.ID(strings.TrimSpace(*requested))
	}
	restored, err := store.Resume(ctx, id)
	if err == nil {
		return id, restored, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", sessionstore.ResumeState{}, fmt.Errorf("resume session %q: %w", id, err)
	}
	snapshot, err := store.Create(ctx, id)
	if err != nil {
		return "", sessionstore.ResumeState{}, fmt.Errorf("create session %q: %w", id, err)
	}
	return id, sessionstore.ResumeState{Snapshot: snapshot}, nil
}

func reasoningEffort(level string) llm.ReasoningEffort {
	switch level {
	case "low":
		return llm.ReasoningEffortLow
	case "medium":
		return llm.ReasoningEffortMedium
	case "xhigh":
		return llm.ReasoningEffortXHigh
	case "max":
		return llm.ReasoningEffortMax
	default:
		return llm.ReasoningEffortHigh
	}
}

func newClient(request Request, getenv func(string) string) (llm.Adapter, string, func() error, error) {
	provider := request.Provider
	if provider == "" {
		provider = getenv("UNREAL_TUI_PROVIDER")
	}
	if provider == "" {
		provider = defaultProvider
	}
	model := request.Model
	if model == "" {
		model = getenv("UNREAL_TUI_MODEL")
	}
	maxAttempts := 4
	switch provider {
	case "openai":
		apiKey := getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, "", nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		if model == "" {
			model = defaultModel
		}
		client, err := openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: getenv("OPENAI_BASE_URL"), MaxAttempts: &maxAttempts})
		if err != nil {
			return nil, "", nil, err
		}
		return client, model, client.Close, nil
	case "commandcode":
		apiKey := getenv("COMMANDCODE_API_KEY")
		if apiKey == "" {
			return nil, "", nil, fmt.Errorf("COMMANDCODE_API_KEY is not set")
		}
		if model == "" {
			model = "z-ai/glm-5.3-flash"
		}
		client, err := openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: "https://api.commandcode.ai/provider/v1", MaxAttempts: &maxAttempts})
		if err != nil {
			return nil, "", nil, err
		}
		return client, model, client.Close, nil
	case "openrouter":
		apiKey := getenv("OPENROUTER_API_KEY")
		if apiKey == "" {
			return nil, "", nil, fmt.Errorf("OPENROUTER_API_KEY is not set")
		}
		if model == "" {
			model = defaultModel
		}
		client, err := openrouter.NewClient(openrouter.Config{APIKey: apiKey, BaseURL: "https://openrouter.ai/api/v1", MaxAttempts: &maxAttempts})
		if err != nil {
			return nil, "", nil, err
		}
		return client, model, client.Close, nil
	case "fireworks":
		apiKey := getenv("FIREWORKS_AI_API_KEY")
		if apiKey == "" {
			return nil, "", nil, fmt.Errorf("FIREWORKS_AI_API_KEY is not set")
		}
		if model == "" {
			model = defaultModel
		}
		client, err := fireworks.NewClient(fireworks.Config{APIKey: apiKey, BaseURL: "https://api.fireworks.ai/inference/v1", MaxAttempts: &maxAttempts})
		if err != nil {
			return nil, "", nil, err
		}
		return client, model, client.Close, nil
	case "ollama":
		if model == "" {
			model = "qwen3:8b"
		}
		client, err := ollama.NewClient(ollama.Config{BaseURL: ollama.BaseURL, MaxAttempts: &maxAttempts})
		if err != nil {
			return nil, "", nil, err
		}
		return client, model, client.Close, nil
	default:
		return nil, "", nil, fmt.Errorf("unknown provider %q (want openai, commandcode, openrouter, fireworks, or ollama)", provider)
	}
}

func stdin() io.Reader { return os.Stdin }

// skillModelDisabled reports whether a SKILL.md's frontmatter sets
// disable-model-invocation: true (pi-compatible user-invoked-only skills).
func skillModelDisabled(path string) bool {
	contents, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found || strings.TrimSpace(key) != "disable-model-invocation" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "yes", "1":
			return true
		}
	}
	return false
}

// buildTools mirrors upstream's tool configuration exactly (same shell
// resolution, skill discovery, and enabled names); when useFileTools is true
// the Read/Write/Edit tools are added on top.
func buildTools(
	ctx context.Context,
	workspace, storeDirectory string,
	sessionID session.ID,
	useFileTools bool,
	skillDirs []string,
	home string,
) (tool.Registry, operation.Manager, []tool.Skill, error) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	operationDirectory := filepath.Join(storeDirectory, "operations", string(sessionID))
	if err := os.MkdirAll(operationDirectory, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create operation directory: %w", err)
	}
	// Skills: pi-style discovery — harness-native .harness/skills plus project
	// and user .agents/skills locations — then register every non-flagged skill
	// as a model tool (the harness's way of advertising skills). Skills with
	// disable-model-invocation: true are NOT registered (zero token cost); the
	// TUI exposes them via /skill:name. First location wins on name collisions.
	var skillEntries []tool.Skill
	for _, entry := range skills.Discover(skills.DefaultDirs(workspace, home)) {
		if entry.UserOnly {
			continue
		}
		skillEntries = append(skillEntries, tool.Skill{Name: entry.Name, Description: entry.Description, Path: entry.Path})
	}
	names := []string{tool.BashName, tool.ViewImageName}
	if len(skillEntries) != 0 {
		names = append(names, tool.SkillUseName)
	}
	static := tool.StaticTranslators{
		Bash:      bash.New(bash.Config{Shell: shell, Directory: workspace, BaseDirectory: operationDirectory}),
		ViewImage: viewimage.New(viewimage.Config{Directory: workspace}),
	}
	inner := tool.NewRegistry(static, names...)
	for _, skill := range skillEntries {
		if _, err := inner.RegisterSkill(skill); err != nil {
			return nil, nil, nil, fmt.Errorf("register skill %q: %w", skill.Path, err)
		}
	}
	local := operation.NewLocalOperationManager(ctx)
	if !useFileTools {
		return inner, local, skillEntries, nil
	}
	return filetools.NewRegistry(inner, workspace), filetools.NewManager(ctx, local, workspace), skillEntries, nil
}
