package opencodego

import (
	"encoding/json"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// TestChatRequestMapsHarnessItems pins the harness-item → chat-completions
// translation for the seam the runner exercises: system/user messages,
// assistant tool calls, tool results, and GLM reasoning replayed as
// reasoning_content.
func TestChatRequestMapsHarnessItems(t *testing.T) {
	request := llm.Request{
		Model: llm.Model{ID: "glm-5.3-flash", ReasoningEffort: llm.ReasoningEffortHigh},
		Input: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "system prompt"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "create the file"}},
			{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"I should write the file."}}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call_1", Name: "Write", Arguments: `{"path":"hello.txt"}`}},
			{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call_1", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Wrote 3 bytes."}}}},
		},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "Write", Description: "write a file", Parameters: map[string]any{"type": "object"}}},
	}
	body := buildChatRequest(request)

	if body.Model != "glm-5.3-flash" {
		t.Fatalf("model = %q", body.Model)
	}
	if body.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q", body.ReasoningEffort)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "Write" || body.Tools[0].Type != "function" {
		t.Fatalf("tools = %+v", body.Tools)
	}

	var roles []string
	for _, message := range body.Messages {
		roles = append(roles, message.Role)
	}
	// Reasoning and tool-call items merge into one assistant turn (the
	// chat-completions shape), so roles are: system, user, assistant+tools, tool.
	if got, want := joinStrings(roles), "system user assistant tool"; got != want {
		t.Fatalf("message roles = %q, want %q", got, want)
	}
	if unquote(body.Messages[0].Content) != "system prompt" || unquote(body.Messages[1].Content) != "create the file" {
		t.Fatalf("message content lost: %+v", body.Messages[:2])
	}
	replay := body.Messages[2]
	if replay.ReasoningContent != "I should write the file." {
		t.Fatalf("reasoning_content = %q", replay.ReasoningContent)
	}
	if len(replay.ToolCalls) != 1 || replay.ToolCalls[0].ID != "call_1" || replay.ToolCalls[0].Function.Name != "Write" {
		t.Fatalf("tool call replay = %+v", replay.ToolCalls)
	}
	if replay.ToolCalls[0].Function.Arguments != `{"path":"hello.txt"}` {
		t.Fatalf("tool call arguments = %q", replay.ToolCalls[0].Function.Arguments)
	}
	if body.Messages[3].ToolCallID != "call_1" || unquote(body.Messages[3].Content) != "Wrote 3 bytes." {
		t.Fatalf("tool result replay = %+v", body.Messages[3])
	}
}

// TestChatRequestReasoningWithRaw pins the Raw-first rule: a reasoning item
// carrying raw provider state replays verbatim, never re-encoded from Summary.
func TestChatRequestReasoningRawBeatsSummary(t *testing.T) {
	request := llm.Request{
		Input: []llm.Item{
			{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"summary"}, Raw: json.RawMessage(`"raw text"`)}, ProviderID: "r_1"},
		},
	}
	body := buildChatRequest(request)
	if body.Messages[0].ReasoningContent != "raw text" {
		t.Fatalf("raw reasoning_content = %q", body.Messages[0].ReasoningContent)
	}
}

// TestChatRequestThinkingClamp pins the GLM thinking-level map: low/high/max
// pass through, medium clamps up to high, xhigh clamps up to max, empty
// defaults to high.
func TestChatRequestThinkingClamp(t *testing.T) {
	cases := map[llm.ReasoningEffort]string{
		llm.ReasoningEffortLow:    "low",
		llm.ReasoningEffortMedium: "high",
		llm.ReasoningEffortHigh:   "high",
		llm.ReasoningEffortXHigh:  "max",
		llm.ReasoningEffortMax:    "max",
		"":                        "high",
	}
	for effort, want := range cases {
		body := buildChatRequest(llm.Request{Model: llm.Model{ID: "glm-5.3-flash", ReasoningEffort: effort}})
		if body.ReasoningEffort != want {
			t.Fatalf("effort %q: reasoning_effort = %q, want %q", effort, body.ReasoningEffort, want)
		}
	}
}

// TestChatResponseDecode pins the chat-completions → harness translation for
// the GLM turn shape: reasoning first, then tool calls, then usage buckets
// with cached input, and finish-reason mapping.
func TestChatResponseDecode(t *testing.T) {
	encoded := []byte(`{
		"id": "chatcmpl_1",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"reasoning_content": "I should write the greeting.",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "Write", "arguments": "{\"path\":\"hello.txt\",\"content\":\"hi\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 55,
			"prompt_tokens_details": {"cached_tokens": 40},
			"completion_tokens_details": {"reasoning_tokens": 15}
		}
	}`)
	response, err := decodeChatResponse(encoded)
	if err != nil {
		t.Fatalf("decodeChatResponse: %v", err)
	}
	if response.ID != "chatcmpl_1" {
		t.Fatalf("id = %q", response.ID)
	}
	if response.Stop != llm.StopComplete {
		t.Fatalf("stop = %q (tool_calls turns continue)", response.Stop)
	}
	if len(response.Output) != 2 {
		t.Fatalf("output items = %d, want 2 (reasoning + tool call)", len(response.Output))
	}
	reasoning, ok := response.Output[0].Data.(llm.Reasoning)
	if !ok || len(reasoning.Summary) != 1 || reasoning.Summary[0] != "I should write the greeting." {
		t.Fatalf("reasoning item = %+v", response.Output[0])
	}
	toolCall, ok := response.Output[1].Data.(llm.ToolCall)
	if !ok || toolCall.CallID != "call_1" || toolCall.Name != "Write" || toolCall.Arguments != `{"path":"hello.txt","content":"hi"}` {
		t.Fatalf("tool call item = %+v", response.Output[1])
	}
	if response.Usage.InputTokens != 100 || response.Usage.CachedInputTokens != 40 ||
		response.Usage.OutputTokens != 55 || response.Usage.ReasoningTokens != 15 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

// TestChatResponseDecodeFinalTurn pins the plain-message turn: content text
// becomes an assistant message item; stop maps to complete.
func TestChatResponseDecodeFinalTurn(t *testing.T) {
	encoded := []byte(`{
		"id": "chatcmpl_2",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "done"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 120, "completion_tokens": 5}
	}`)
	response, err := decodeChatResponse(encoded)
	if err != nil {
		t.Fatalf("decodeChatResponse: %v", err)
	}
	if response.Stop != llm.StopComplete {
		t.Fatalf("stop = %q", response.Stop)
	}
	if len(response.Output) != 1 {
		t.Fatalf("output items = %d, want 1", len(response.Output))
	}
	message, ok := response.Output[0].Data.(llm.Message)
	if !ok || message.Role != llm.RoleAssistant || message.Text != "done" {
		t.Fatalf("message item = %+v", response.Output[0])
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

// TestChatResponseFinishReasons pins the remaining finish-reason mappings.
func TestChatResponseFinishReasons(t *testing.T) {
	for finish, want := range map[string]llm.StopReason{
		"length":         llm.StopMaxOutputTokens,
		"content_filter": llm.StopRefused,
	} {
		encoded := []byte(`{"id":"c","choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		response, err := decodeChatResponse(encoded)
		if err != nil {
			t.Fatalf("decodeChatResponse(%s): %v", finish, err)
		}
		if response.Stop != want {
			t.Fatalf("finish %q: stop = %q, want %q", finish, response.Stop, want)
		}
	}
}

func joinStrings(values []string) string { return strings.Join(values, " ") }

// unquote decodes a JSON content value (string or null) to text.
func unquote(content jsontext.Value) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err != nil {
		return ""
	}
	return text
}
