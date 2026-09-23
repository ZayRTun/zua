// Package testsrv provides a minimal fake OpenAI Responses API (SSE) for
// tests: turn 1 returns a configurable tool call, later turns return a final
// message.
package testsrv

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// New starts a fake Responses API server that issues a Write tool call on the
// first turn; the returned URL is the base URL.
func New(t *testing.T) *httptest.Server {
	return NewWithScript(t, "Write", `{"path":"hello.txt","content":"hi from the agent"}`)
}

// NewCapture starts a fake server like New (Write tool call on turn 1,
// "done" afterwards), recording every request body for assertions.
func NewCapture(t *testing.T) (*httptest.Server, *[][]byte) {
	t.Helper()
	var bodies [][]byte
	var turn atomic.Int64
	firstItem := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Write","status":"completed","arguments":"{\"path\":\"hello.txt\",\"content\":\"hi\"}"}`
	finalItem := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		send := func(event, data string) {
			w.Write([]byte("event: " + event + "\ndata: " + data + "\n\n"))
			flusher.Flush()
		}
		item := firstItem
		if turn.Add(1) > 1 {
			item = finalItem
		}
		send("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":`+item+`}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[`+item+`]}}`)
	}))
	t.Cleanup(server.Close)
	return server, &bodies
}

// NewWithScript starts a fake server whose first turn issues the given tool
// call; subsequent turns return an assistant message "done".
func NewWithScript(t *testing.T, tool string, arguments string) *httptest.Server {
	t.Helper()
	var turn atomic.Int64
	firstItem := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"` + tool + `","status":"completed","arguments":` + quote(arguments) + `}`
	finalItem := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := turn.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		send := func(event, data string) {
			w.Write([]byte("event: " + event + "\ndata: " + data + "\n\n"))
			flusher.Flush()
		}
		item := firstItem
		if turn > 1 {
			item = finalItem
		}
		send("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":`+item+`}`)
		send("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[`+item+`]}}`)
	}))
}

// quote encodes s as a JSON string literal.
func quote(s string) string {
	encoded, _ := marshalString(s)
	return encoded
}

func marshalString(s string) (string, error) {
	data, err := json.Marshal(s)
	return string(data), err
}
