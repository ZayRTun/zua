package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// startTurn spawns one zua-agent process for the prompt and streams
// its JSONL events into m.events.
func (m *Model) startTurn(prompt string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.cmdCancel = cancel
	agentBinary := agentPath()
	request := map[string]any{"prompt": prompt}
	if m.sessionID != "" {
		request["session_id"] = m.sessionID
	}
	if m.provider != "" {
		request["provider"] = m.provider
	}
	if m.model != "" {
		request["model"] = m.model
	}
	encoded, _ := json.Marshal(request)

	cmd := exec.CommandContext(ctx, agentBinary, "-workspace", m.workspace, string(encoded))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.events <- blockMsg{b: block{kind: blockError, text: err.Error()}}
		m.events <- turnDoneMsg{}
		return
	}
	stderr := &stderrTail{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		m.events <- blockMsg{b: block{kind: blockError, text: err.Error()}}
		m.events <- turnDoneMsg{}
		return
	}

	parser := newEventParser()
	events := m.events
	go func() {
		decodeItems(stdout, func(kind string, data json.RawMessage) {
			for _, message := range parser.parse(kind, data) {
				events <- message
			}
		})
		err := cmd.Wait()
		if err != nil {
			if tail := stderr.Tail(6); tail != "" {
				err = fmt.Errorf("%w\nagent stderr:\n%s", err, tail)
			}
		}
		events <- turnDoneMsg{err: err}
		close(events)
	}()
}

// stderrTail captures the agent's stderr, keeping the last lines so a failed
// run can show why it failed (missing API key, version mismatch, …).
type stderrTail struct {
	mu      sync.Mutex
	lines   []string
	dropped int
}

func (buffer *stderrTail) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	for _, line := range strings.Split(string(payload), "\n") {
		if line == "" {
			continue
		}
		buffer.lines = append(buffer.lines, line)
		if len(buffer.lines) > maxStderrLines {
			buffer.lines = buffer.lines[1:]
			buffer.dropped++
		}
	}
	return len(payload), nil
}

// Tail returns the last n stderr lines as one string ("" if none).
func (buffer *stderrTail) Tail(n int) string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.lines) == 0 {
		return ""
	}
	start := 0
	if len(buffer.lines) > n {
		start = len(buffer.lines) - n
	}
	head := ""
	if buffer.dropped > 0 {
		head = fmt.Sprintf("… %d earlier lines hidden\n", buffer.dropped)
	}
	return head + strings.Join(buffer.lines[start:], "\n")
}

const maxStderrLines = 40

// listen returns the next message from the event stream.
func (m *Model) listen() tea.Cmd {
	events := m.events
	return func() tea.Msg {
		message, ok := <-events
		if !ok {
			return turnDoneMsg{}
		}
		return message
	}
}

func agentPath() string {
	executable, err := os.Executable()
	if err == nil {
		sibling := filepath.Join(filepath.Dir(executable), "zua-agent")
		if _, err := os.Stat(sibling); err == nil {
			return sibling
		}
	}
	if path, err := exec.LookPath("zua-agent"); err == nil {
		return path
	}
	return "zua-agent"
}

// collapse reduces text to one line of at most width runes.
func collapse(text string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if width <= 0 || len(runes) <= width {
		return text
	}
	return string(runes[:width]) + "…"
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
