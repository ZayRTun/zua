package testsrv

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ChatExchange records one request the fake chat-completions server saw.
type ChatExchange struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// NewChatCompletions starts a fake OpenCode Go chat-completions server. Turn
// 1 replies with GLM's reasoning_content + a Write tool call and usage with
// cached input; later turns reply with a final "done" message. Requests
// without the mandatory x-opencode-session header get HTTP 400
// MissingSessionID, matching the real gateway. Returns the server and a
// snapshot function exposing every recorded exchange.
func NewChatCompletions(t *testing.T) (*httptest.Server, func() []ChatExchange) {
	t.Helper()
	var mu sync.Mutex
	var exchanges []ChatExchange
	var turn int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		exchanges = append(exchanges, ChatExchange{
			Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body,
		})
		turn++
		current := turn
		mu.Unlock()

		if r.Header.Get("x-opencode-session") == "" {
			// Literal mirrors opencodego.SessionHeader; a testsrv import of
			// opencodego would cycle, so the coupling is by convention only.
			writeChatError(t, w, http.StatusBadRequest, "MissingSessionID", "Requests must include the x-opencode-session header.")
			return
		}
		var request struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.Unmarshal(body, &request)

		var reply string
		switch {
		case current == 1:
			reply = `{
				"id": "chatcmpl_1",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"reasoning_content": "I should create the greeting file.",
						"content": null,
						"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "Write", "arguments": "{\"path\":\"hello.txt\",\"content\":\"hi from the agent\"}"}}]
					},
					"finish_reason": "tool_calls"
				}],
				"usage": {
					"prompt_tokens": 100,
					"completion_tokens": 55,
					"prompt_tokens_details": {"cached_tokens": 40},
					"completion_tokens_details": {"reasoning_tokens": 15}
				}
			}`
		default:
			reply = `{
				"id": "chatcmpl_2",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "done"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 120, "completion_tokens": 5, "prompt_tokens_details": {"cached_tokens": 40}}
			}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(server.Close)
	snapshot := func() []ChatExchange {
		mu.Lock()
		defer mu.Unlock()
		return append([]ChatExchange(nil), exchanges...)
	}
	return server, snapshot
}

func writeChatError(t *testing.T, w http.ResponseWriter, status int, code string, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoded, err := json.Marshal(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(encoded)
}
