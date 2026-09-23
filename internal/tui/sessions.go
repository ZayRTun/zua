package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type sessionEntry struct {
	id      string
	title   string
	updated string
}

func sessionsDirectory(workspace string) string {
	return filepath.Join(workspace, ".harness", "sessions")
}

func listSessions(workspace string) tea.Cmd {
	return func() tea.Msg {
		store, err := localfile.New(sessionsDirectory(workspace))
		if err != nil {
			return sessionsMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		infos, err := store.ListSessions(ctx)
		if err != nil {
			return sessionsMsg{err: err}
		}
		sort.Slice(infos, func(i, j int) bool {
			return infos[i].LastUpdatedAt.After(infos[j].LastUpdatedAt)
		})
		entries := make([]sessionEntry, 0, len(infos))
		for _, info := range infos {
			title := sessionTitle(ctx, store, info.ID)
			entries = append(entries, sessionEntry{
				id:      string(info.ID),
				title:   orDefault(collapse(title, 60), "(empty session)"),
				updated: info.LastUpdatedAt.Format("Jan 2 15:04"),
			})
		}
		return sessionsMsg{entries: entries}
	}
}

// sessionTitle returns the first user prompt in a session, or "".
func sessionTitle(ctx context.Context, store *localfile.Store, id session.ID) string {
	page, err := store.Items(ctx, id, sessionstore.BeforeFirst, 20)
	if err != nil {
		return ""
	}
	for _, item := range page.Items {
		if input, ok := item.Data.(inbox.Input); ok && input.Kind == inbox.InputExternal {
			var text string
			if err := json.Unmarshal(input.Payload, &text); err == nil {
				return text
			}
		}
	}
	return ""
}

// replaySession rebuilds transcript blocks (user prompts, assistant messages,
// and tool cards) from a persisted session so a resumed transcript looks like
// a live one. Tool cards carry the recorded call arguments and final status,
// so they render through the same collapsed/verbose path as live cards.
func replaySession(workspace string, entry sessionEntry) ([]block, string, error) {
	store, err := localfile.New(sessionsDirectory(workspace))
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	blocks := []block{}
	cards := map[string]*toolCard{} // callID → card, updated by later statuses
	id := session.ID(entry.id)
	after := sessionstore.BeforeFirst
	for {
		page, err := store.Items(ctx, id, after, 200)
		if err != nil {
			return nil, "", err
		}
		for _, item := range page.Items {
			switch data := item.Data.(type) {
			case inbox.Input:
				if data.Kind == inbox.InputExternal {
					var text string
					if err := json.Unmarshal(data.Payload, &text); err == nil && strings.TrimSpace(text) != "" {
						blocks = append(blocks, block{kind: blockUser, text: text})
					}
				}
			case sessionstore.ModelResponse:
				for _, output := range data.Response.Output {
					switch output.Type {
					case llm.ItemMessage:
						if message, ok := output.Data.(llm.Message); ok && message.Role == llm.RoleAssistant && strings.TrimSpace(message.Text) != "" {
							blocks = append(blocks, block{kind: blockAssistant, text: strings.TrimRight(message.Text, "\n")})
						}
					case llm.ItemToolCall:
						// The card appears where the model requested the call
						// (as "running"), matching the live transcript order;
						// the matching tool_call_status item fills it in.
						if call, ok := output.Data.(llm.ToolCall); ok && call.CallID != "" {
							if _, exists := cards[call.CallID]; !exists {
								card := &toolCard{callID: call.CallID, name: call.Name, argsRaw: call.Arguments, status: "running"}
								cards[call.CallID] = card
								blocks = append(blocks, block{kind: blockTool, tool: card})
							}
						}
					}
				}
			case sessionstore.ToolCallStatus:
				if card, ok := cards[data.CallID]; ok {
					applyReplayStatus(card, data)
				}
			}
		}
		if !page.More {
			break
		}
		after = page.NextAfter
	}
	return blocks, entry.id, nil
}

// applyReplayStatus folds one persisted tool-call status into a replayed card.
// Later statuses overwrite earlier ones, so the terminal state wins. The
// operation snapshots are re-encoded into the wire shape and folded by the
// same resolver the live event parser uses.
func applyReplayStatus(card *toolCard, status sessionstore.ToolCallStatus) {
	ops := make([]wireOp, 0, len(status.Operations))
	for _, op := range status.Operations {
		wire := wireOp{ID: string(op.ID), Type: string(op.Type), Status: string(op.Status)}
		if op.State != nil && json.Unmarshal(op.State, &wire.State) != nil {
			wire.State = wireOpState{}
		}
		ops = append(ops, wire)
	}
	cs := resolveCallStatus(status.Status.Error, ops)
	card.status, card.errText, card.outText, card.exitCode = cs.status, cs.errText, cs.outText, cs.exitCode
}
