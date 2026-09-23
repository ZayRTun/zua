package opencodego

import (
	"encoding/json"
	"encoding/json/jsontext"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// buildChatRequest translates harness request items into a chat-completions
// body. Reasoning items replay their Raw provider state verbatim when present
// (the harness contract: Raw is replayed unchanged, never re-encoded from
// Summary); GLM reasoning_content is that state. Consecutive assistant
// reasoning + tool-call items merge into one assistant turn — the
// chat-completions shape carries both on a single message.
func buildChatRequest(request llm.Request) chatRequest {
	body := chatRequest{
		Model:           request.Model.ID,
		ReasoningEffort: thinkingLevel(request.Model.ReasoningEffort),
	}
	var pending *chatMessage
	flush := func() {
		if pending != nil {
			body.Messages = append(body.Messages, *pending)
			pending = nil
		}
	}
	for _, item := range request.Input {
		switch item.Type {
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				continue
			}
			flush()
			body.Messages = append(body.Messages, chatMessage{
				Role:    string(message.Role),
				Content: textContent(message.Text),
			})
		case llm.ItemToolCall:
			call, ok := item.Data.(llm.ToolCall)
			if !ok {
				continue
			}
			if pending == nil {
				pending = &chatMessage{Role: "assistant"}
			}
			pending.ToolCalls = append(pending.ToolCalls, chatToolCall{
				ID:       call.CallID,
				Type:     "function",
				Function: chatFunction{Name: call.Name, Arguments: call.Arguments},
			})
		case llm.ItemToolResult:
			result, ok := item.Data.(llm.ToolResult)
			if !ok {
				continue
			}
			flush()
			body.Messages = append(body.Messages, chatMessage{
				Role:       "tool",
				ToolCallID: result.CallID,
				Content:    textContent(joinOutputs(result)),
			})
		case llm.ItemReasoning:
			reasoning, ok := item.Data.(llm.Reasoning)
			if !ok {
				continue
			}
			if pending == nil {
				pending = &chatMessage{Role: "assistant"}
			}
			pending.ReasoningContent = reasoningText(reasoning)
		}
	}
	flush()
	for _, tool := range request.Tools {
		if tool.Type != llm.ToolFunction {
			continue
		}
		body.Tools = append(body.Tools, chatTool{
			Type: "function",
			Function: chatToolDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return body
}

// thinkingLevel maps harness reasoning efforts onto GLM's supported levels.
// medium clamps up to high and xhigh clamps up to max; empty defaults high
// (the settings default).
func thinkingLevel(effort llm.ReasoningEffort) string {
	switch effort {
	case llm.ReasoningEffortLow:
		return "low"
	case llm.ReasoningEffortMax, llm.ReasoningEffortXHigh:
		return "max"
	default:
		return "high"
	}
}

// decodeChatResponse translates a chat-completions reply into harness items:
// reasoning first (the GLM shape), then tool calls, then the message text.
// tool_calls turns still report complete — the turn continues through tool
// results, not a terminal stop.
func decodeChatResponse(encoded []byte) (llm.Response, error) {
	var reply chatResponse
	if err := json.Unmarshal(encoded, &reply); err != nil {
		return llm.Response{}, err
	}
	response := llm.Response{ID: reply.ID, Stop: finishReason(reply.firstFinish())}
	if len(reply.Choices) > 0 {
		response.Output = reply.Choices[0].Message.items()
	}
	response.Usage = llm.Usage{
		InputTokens:           reply.Usage.PromptTokens,
		CachedInputTokens:     reply.Usage.PromptDetails.cached(),
		CacheWriteInputTokens: 0, // chat-completions does not report cache writes
		OutputTokens:          reply.Usage.CompletionTokens,
		ReasoningTokens:       reply.Usage.CompletionDetail.reasoning(),
	}
	return response, nil
}

// finishReason maps chat-completions finish reasons to harness stop reasons.
func finishReason(reason string) llm.StopReason {
	switch reason {
	case "length":
		return llm.StopMaxOutputTokens
	case "content_filter":
		return llm.StopRefused
	default:
		return llm.StopComplete
	}
}

// textContent encodes message text as the JSON content value: null when empty
// (the chat-completions convention for assistant tool-call messages), a
// string literal otherwise.
func textContent(text string) jsontext.Value {
	if text == "" {
		return jsontext.Value("null")
	}
	encoded, _ := json.Marshal(text)
	return jsontext.Value(encoded)
}

// joinOutputs flattens a tool result's text outputs; image outputs have no
// chat-completions text form and are represented by a placeholder.
func joinOutputs(result llm.ToolResult) string {
	texts := make([]string, 0, len(result.Output))
	for _, output := range result.Output {
		if output.Kind == llm.ToolResultImage {
			texts = append(texts, "[image output omitted]")
			continue
		}
		texts = append(texts, output.Value)
	}
	return strings.Join(texts, "\n")
}

// reasoningText extracts replayable reasoning text: Raw provider state first,
// Summary joined otherwise.
func reasoningText(reasoning llm.Reasoning) string {
	if len(reasoning.Raw) > 0 {
		var raw string
		if err := json.Unmarshal(reasoning.Raw, &raw); err == nil {
			return raw
		}
	}
	return strings.Join(reasoning.Summary, "\n")
}
