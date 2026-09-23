package opencodego

import (
	"encoding/json"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"unreal-agent-tui/internal/testsrv"
)

func newTestClient(t *testing.T, baseURL string, trace func(Exchange)) *Client {
	t.Helper()
	client, err := NewClient(Config{APIKey: "oc_test", BaseURL: baseURL, MaxAttempts: intPtr(1), Trace: trace})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func intPtr(value int) *int { return &value }

// TestRespondHeadersAndSession pins the wire contract: POST to
// <base>/chat/completions, Bearer key, and the mandatory session header.
func TestRespondHeadersAndSession(t *testing.T) {
	server, snapshot := testsrv.NewChatCompletions(t)
	client := newTestClient(t, server.URL, nil)
	client.SetSessionID("sess-123")

	request := llm.Request{
		Model: llm.Model{ID: "glm-5.3-flash", ReasoningEffort: llm.ReasoningEffortLow},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "create the file"}}},
	}
	response, err := client.Respond(t.Context(), request, llm.RequestOptions{})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if response.ID != "chatcmpl_1" {
		t.Fatalf("response id = %q", response.ID)
	}

	exchanges := snapshot()
	if len(exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(exchanges))
	}
	exchange := exchanges[0]
	if exchange.Method != "POST" || exchange.Path != "/chat/completions" {
		t.Fatalf("request line: %s %s", exchange.Method, exchange.Path)
	}
	if got := exchange.Header.Get("Authorization"); got != "Bearer oc_test" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := exchange.Header.Get("x-opencode-session"); got != "sess-123" {
		t.Fatalf("x-opencode-session = %q", got)
	}
	var body struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(exchange.Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body.Model != "glm-5.3-flash" {
		t.Fatalf("model = %q", body.Model)
	}
	if body.ReasoningEffort != "low" {
		t.Fatalf("reasoning_effort = %q", body.ReasoningEffort)
	}
}

// TestRespondWithoutSessionFails pins the fail-fast guard: without
// SetSessionID the client refuses to send a doomed request at all.
func TestRespondWithoutSessionFails(t *testing.T) {
	server, snapshot := testsrv.NewChatCompletions(t)
	client := newTestClient(t, server.URL, nil)
	// No SetSessionID: the header stays unset and Respond refuses up front.
	request := llm.Request{
		Model: llm.Model{ID: "glm-5.3-flash"},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}},
	}
	if _, err := client.Respond(t.Context(), request, llm.RequestOptions{}); err == nil {
		t.Fatal("Respond must fail without a session id")
	}
	if exchanges := snapshot(); len(exchanges) != 0 {
		t.Fatalf("exchanges = %d, want 0 (fail fast, nothing sent)", len(exchanges))
	}
}

// TestRespondMapsUsageAndReasoning pins the full-turn round trip through the
// fake server: reasoning_content lands on a reasoning item, usage buckets
// survive.
func TestRespondMapsUsageAndReasoning(t *testing.T) {
	server, _ := testsrv.NewChatCompletions(t)
	client := newTestClient(t, server.URL, nil)
	client.SetSessionID("sess-1")

	response, err := client.Respond(t.Context(), llm.Request{
		Model: llm.Model{ID: "glm-5.3-flash"},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "create the file"}}},
	}, llm.RequestOptions{})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(response.Output) != 2 {
		t.Fatalf("output items = %d, want 2", len(response.Output))
	}
	if response.Output[0].Type != llm.ItemReasoning {
		t.Fatalf("first item = %q, want reasoning", response.Output[0].Type)
	}
	if response.Output[1].Type != llm.ItemToolCall {
		t.Fatalf("second item = %q, want tool_call", response.Output[1].Type)
	}
	if response.Usage.InputTokens != 100 || response.Usage.CachedInputTokens != 40 || response.Usage.OutputTokens != 55 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

// TestNewClientRequiresKey pins the key precondition at construction.
func TestNewClientRequiresKey(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("NewClient must reject a missing key")
	}
}
