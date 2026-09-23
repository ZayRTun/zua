package tui

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// itemKind mirrors the JSONL item kinds emitted by the runner.
const (
	kindMeta           = "meta"
	kindModelResponse  = "model_response"
	kindToolCallStatus = "tool_call_status"
	kindError          = "error"
)

// ---- wire shapes (runner JSONL) ----

type wireItem struct {
	Kind string          `json:"Kind"`
	Data json.RawMessage `json:"Data"`
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type wireMeta struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Workspace string `json:"workspace"`
}

type wireModelResponse struct {
	Response wireResponse `json:"Response"`
}

type wireResponse struct {
	Output []wireOutputItem `json:"Output"`
	Usage  wireUsage        `json:"Usage"`
}

type wireUsage struct {
	InputTokens  int64 `json:"InputTokens"`
	OutputTokens int64 `json:"OutputTokens"`
}

type wireOutputItem struct {
	Type string          `json:"Type"`
	Data json.RawMessage `json:"Data"`
}

type wireMessage struct {
	Role string `json:"Role"`
	Text string `json:"Text"`
}

type wireToolCall struct {
	CallID string          `json:"CallID"`
	Name   string          `json:"Name"`
	Args   json.RawMessage `json:"Arguments"`
}

type wireReasoning struct {
	Summary []string `json:"Summary"`
}

type wireToolCallStatus struct {
	CallID     string     `json:"CallID"`
	Status     wireStatus `json:"Status"`
	Operations []wireOp   `json:"Operations"`
}

type wireStatus struct {
	Error string `json:"Error"`
}

type wireOp struct {
	ID     string      `json:"ID"`
	Type   string      `json:"Type"`
	Status string      `json:"Status"`
	State  wireOpState `json:"State"`
}

type wireOpState struct {
	Result *wireOpResult `json:"Result"`
}

type wireOpResult struct {
	Out      string `json:"Out"`
	Err      string `json:"Err"`
	ExitCode *int   `json:"ExitCode"`
}

func decodeItems(reader io.Reader, handle func(kind string, data json.RawMessage)) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var meta wireMeta
		if err := json.Unmarshal(line, &meta); err == nil && meta.Type == "meta" {
			handle(kindMeta, line)
			continue
		}
		var errEvent wireError
		if err := json.Unmarshal(line, &errEvent); err == nil && errEvent.Type == "error" {
			handle(kindError, line)
			continue
		}
		var item wireItem
		if err := json.Unmarshal(line, &item); err != nil {
			continue
		}
		handle(item.Kind, item.Data)
	}
}

// toolInfo is the model-facing tool call recorded when it appears in a model
// response, so the later tool_call_status can render a full card.
type toolInfo struct {
	name    string
	argsRaw string
}

// eventParser turns JSONL wire items into UI messages.
type eventParser struct {
	toolCalls map[string]toolInfo
}

func newEventParser() *eventParser {
	return &eventParser{toolCalls: map[string]toolInfo{}}
}

// parse converts one wire item into zero or more UI messages.
func (parser *eventParser) parse(kind string, data json.RawMessage) []tea.Msg {
	switch kind {
	case kindMeta:
		var meta wireMeta
		if err := json.Unmarshal(data, &meta); err == nil {
			return []tea.Msg{metaMsg{sessionID: meta.SessionID, model: meta.Model}}
		}
	case kindModelResponse:
		var response wireModelResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return nil
		}
		var messages []tea.Msg
		var texts []string
		for _, output := range response.Response.Output {
			switch output.Type {
			case "message":
				var message wireMessage
				if err := json.Unmarshal(output.Data, &message); err == nil && message.Role == "assistant" && strings.TrimSpace(message.Text) != "" {
					texts = append(texts, strings.TrimRight(message.Text, "\n"))
				}
			case "tool_call":
				var call wireToolCall
				if err := json.Unmarshal(output.Data, &call); err == nil && call.CallID != "" {
					parser.toolCalls[call.CallID] = toolInfo{name: call.Name, argsRaw: string(call.Args)}
					messages = append(messages, toolStatusMsg{callID: call.CallID, name: call.Name, argsRaw: string(call.Args), status: "running"})
				}
			case "reasoning":
				var reasoning wireReasoning
				if err := json.Unmarshal(output.Data, &reasoning); err == nil && len(reasoning.Summary) > 0 {
					text := collapse(strings.Join(reasoning.Summary, " "), 200)
					if text != "" {
						messages = append(messages, blockMsg{b: block{kind: blockReasoning, text: text}})
					}
				}
			}
		}
		usage := response.Response.Usage
		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			messages = append(messages, usageMsg{in: usage.InputTokens, out: usage.OutputTokens})
		}
		// Merge all assistant messages of one response into a single markdown
		// block so glamour renders the document as a whole, not fragments.
		if len(texts) > 0 {
			messages = append(messages, blockMsg{b: block{kind: blockAssistant, text: strings.Join(texts, "\n\n")}})
		}
		return messages
	case kindToolCallStatus:
		var status wireToolCallStatus
		if err := json.Unmarshal(data, &status); err != nil {
			return nil
		}
		info := parser.toolCalls[status.CallID]
		cs := resolveCallStatus(status.Status.Error, status.Operations)
		return []tea.Msg{toolStatusMsg{
			callID:   status.CallID,
			name:     info.name,
			argsRaw:  info.argsRaw,
			status:   cs.status,
			errText:  cs.errText,
			outText:  cs.outText,
			exitCode: cs.exitCode,
		}}
	case kindError:
		var errEvent wireError
		if err := json.Unmarshal(data, &errEvent); err == nil {
			return []tea.Msg{blockMsg{b: block{kind: blockError, text: errEvent.Message}}}
		}
	}
	return nil
}

// callStatus is the UI-facing fold of a harness tool-call status: everything
// a Tool Card displays. The fields always travel together into toolStatusMsg
// and toolCard, so they move as one struct.
type callStatus struct {
	status   string // "running" | "ok" | "failed" | "canceled"
	errText  string
	outText  string
	exitCode int
}

// resolveCallStatus folds a harness tool-call status into the UI-facing card
// fields: (status, errText, outText, exitCode). It is the single source of
// truth shared by the live event parser and session replay, so both paths
// agree on statuses and captured output.
func resolveCallStatus(statusError string, ops []wireOp) callStatus {
	if statusError != "" {
		return callStatus{status: "failed", errText: statusError}
	}
	var resolved callStatus
	last := ""
	for _, operation := range ops {
		last = operation.Status
		if result := operation.State.Result; result != nil {
			output := result.Out
			if result.Err != "" {
				if output != "" {
					output += "\n"
				}
				output += result.Err
			}
			// Cap the payload; the card only shows a few lines.
			if len(output) > 4000 {
				output = output[:4000]
			}
			resolved.outText = output
			if result.ExitCode != nil && *result.ExitCode != 0 {
				resolved.exitCode = *result.ExitCode
			}
		}
	}
	switch last {
	case "completed":
		resolved.status = "ok"
	case "failed":
		resolved.status = "failed"
	case "canceled":
		resolved.status = "canceled"
	default:
		resolved.status = "running"
	}
	return resolved
}

func humanCount(value int64) string {
	switch {
	case value >= 1_000_000:
		return trimFloat(float64(value)/1_000_000) + "M"
	case value >= 1_000:
		return trimFloat(float64(value)/1_000) + "k"
	default:
		return itoa(value)
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func trimFloat(value float64) string {
	whole := int(value)
	fraction := int((value - float64(whole)) * 10)
	if fraction == 0 {
		return itoa(int64(whole))
	}
	return itoa(int64(whole)) + "." + itoa(int64(fraction))
}
