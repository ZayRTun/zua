// Package opencodego adapts the OpenCode Go gateway (chat-completions API,
// OpenAI-compatible) to the harness's one-method llm.Adapter interface. It
// wraps harness primitives — never forks them — so the runner treats it like
// any other provider client.
//
// Gateway facts verified against https://opencode.ai/zen (spec zua#7):
// the /responses endpoint returns 503 for GLM models, so this adapter speaks
// /chat/completions; every request must carry x-opencode-session with the
// caller's session id or the gateway answers HTTP 400 MissingSessionID.
package opencodego

import (
	"encoding/json/jsontext"
	"strconv"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const (
	chatPath = "/chat/completions"
	// SessionHeader is the mandatory gateway header naming the caller's
	// conversation; zua passes its harness session id.
	SessionHeader = "x-opencode-session"
)

// chatToolCall mirrors chat-completions assistant tool_calls entries.
type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatMessage mirrors one chat-completions message. Tool calls replay as an
// assistant message carrying tool_calls; tool results replay as role "tool"
// messages with tool_call_id.
type chatMessage struct {
	Role             string         `json:"role"`
	Content          jsontext.Value `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []chatTool    `json:"tools,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type chatTool struct {
	Type     string      `json:"type"`
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

// firstFinish returns the first choice's finish reason, "" with no choices.
func (response chatResponse) firstFinish() string {
	if len(response.Choices) > 0 {
		return response.Choices[0].FinishReason
	}
	return ""
}

type chatChoice struct {
	Index        int       `json:"index"`
	Message      chatReply `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type chatReply struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
}

// items translates a reply into harness items in the GLM turn shape:
// reasoning first, then tool calls, then the message text.
func (reply chatReply) items() []llm.Item {
	var items []llm.Item
	if reply.ReasoningContent != "" {
		items = append(items, llm.Item{
			ProviderID: "r_1",
			Type:       llm.ItemReasoning,
			Data:       llm.Reasoning{Summary: []string{reply.ReasoningContent}},
		})
	}
	for i, call := range reply.ToolCalls {
		items = append(items, llm.Item{
			ProviderID: toolCallProviderID(i),
			Type:       llm.ItemToolCall,
			Data: llm.ToolCall{
				CallID:    call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	if reply.Content != "" {
		items = append(items, llm.Item{
			ProviderID: "msg_1",
			Type:       llm.ItemMessage,
			Data:       llm.Message{Role: llm.Role(reply.Role), Text: reply.Content},
		})
	}
	return items
}

// toolCallProviderID gives tool-call items stable provider ids (fc_1, fc_2, …).
func toolCallProviderID(index int) string {
	return "fc_" + strconv.Itoa(index+1)
}

type chatUsage struct {
	PromptTokens     int64                `json:"prompt_tokens"`
	CompletionTokens int64                `json:"completion_tokens"`
	PromptDetails    *chatPromptUsage     `json:"prompt_tokens_details,omitempty"`
	CompletionDetail *chatCompletionUsage `json:"completion_tokens_details,omitempty"`
}

type chatPromptUsage struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type chatCompletionUsage struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// cached reports cache-read input tokens; a missing details object means the
// provider did not report caching (zero).
func (usage *chatPromptUsage) cached() int64 {
	if usage == nil {
		return 0
	}
	return usage.CachedTokens
}

// reasoning reports reasoning tokens nested in completion token details.
func (usage *chatCompletionUsage) reasoning() int64 {
	if usage == nil {
		return 0
	}
	return usage.ReasoningTokens
}
