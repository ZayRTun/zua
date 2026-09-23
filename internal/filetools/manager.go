package filetools

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

// Manager wraps a harness operation.Manager, executing file operations itself
// and delegating everything else to the inner manager (Bash, ViewImage, ...).
type Manager struct {
	inner      operation.Manager
	directory  string
	updates    chan operation.Operation
	done       chan operation.Operation
	innerState <-chan operation.Operation

	mu      sync.Mutex
	running map[operation.ID]context.CancelFunc
}

var _ operation.Manager = (*Manager)(nil)

// NewManager wraps inner. File operations resolve relative paths inside
// directory.
func NewManager(ctx context.Context, inner operation.Manager, directory string) *Manager {
	manager := &Manager{
		inner:      inner,
		directory:  directory,
		updates:    make(chan operation.Operation),
		done:       make(chan operation.Operation),
		innerState: inner.Updates(),
		running:    make(map[operation.ID]context.CancelFunc),
	}
	go manager.pump(ctx)
	return manager
}

func (manager *Manager) Add(current operation.Operation) error {
	switch current.Type {
	case TypeRead, TypeWrite, TypeEdit:
		execCtx, cancel := context.WithCancel(context.Background())
		manager.mu.Lock()
		manager.running[current.ID] = cancel
		manager.mu.Unlock()
		go manager.execute(execCtx, current)
		return nil
	default:
		return manager.inner.Add(current)
	}
}

func (manager *Manager) Cancel(id operation.ID, reason string) error {
	manager.mu.Lock()
	cancel, exists := manager.running[id]
	manager.mu.Unlock()
	if exists {
		// The executor observes cancellation and emits the canceled update.
		go cancel()
		return nil
	}
	return manager.inner.Cancel(id, reason)
}

func (manager *Manager) Updates() <-chan operation.Operation {
	return manager.updates
}

func (manager *Manager) pump(ctx context.Context) {
	for {
		select {
		case update := <-manager.innerState:
			select {
			case manager.updates <- update:
			case <-ctx.Done():
				return
			}
		case update := <-manager.done:
			manager.mu.Lock()
			delete(manager.running, update.ID)
			manager.mu.Unlock()
			select {
			case manager.updates <- update:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (manager *Manager) execute(ctx context.Context, current operation.Operation) {
	var request State
	if err := json.Unmarshal(current.State, &request); err != nil {
		manager.finish(current, operation.StatusFailed, State{Path: request.Path, Error: fmt.Sprintf("decode operation state: %v", err)})
		return
	}

	resultState := State{Path: request.Path}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	err := manager.run(execCtx, current.Type, &request, &resultState)
	if ctx.Err() != nil {
		resultState.Error = "canceled"
		manager.finish(current, operation.StatusCanceled, resultState)
		return
	}
	if err != nil {
		resultState.Error = err.Error()
		manager.finish(current, operation.StatusFailed, resultState)
		return
	}
	manager.finish(current, operation.StatusCompleted, resultState)
}

func (manager *Manager) run(ctx context.Context, opType operation.Type, request *State, result *State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(manager.directory, request.Path)
	switch opType {
	case TypeRead:
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(content), "\n")
		start, end := 0, len(lines)
		if request.Offset > 0 {
			start = min(request.Offset-1, len(lines))
		}
		if request.Limit > 0 {
			end = min(start+request.Limit, len(lines))
		}
		var numbered strings.Builder
		for index := start; index < end; index++ {
			numbered.WriteString(strconv.Itoa(index+1) + "\t" + lines[index] + "\n")
		}
		bounded, _ := operation.BoundOutput(numbered.String(), defaultMaxOutputLength)
		result.Result = bounded
		return nil
	case TypeWrite:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(request.Content), 0o644); err != nil {
			return err
		}
		result.Result = fmt.Sprintf("Wrote %d bytes to %s.", len(request.Content), request.Path)
		return nil
	case TypeEdit:
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		count := strings.Count(text, request.OldText)
		if count == 0 {
			return fmt.Errorf("old_text not found in %s", request.Path)
		}
		if count > 1 && !request.ReplaceAll {
			return fmt.Errorf("old_text matches %d times in %s; provide more surrounding text or set replace_all", count, request.Path)
		}
		if request.ReplaceAll {
			text = strings.ReplaceAll(text, request.OldText, request.NewText)
		} else {
			text = strings.Replace(text, request.OldText, request.NewText, 1)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return err
		}
		result.Result = fmt.Sprintf("Edited %s: replaced %d occurrence(s).", request.Path, count)
		return nil
	default:
		return fmt.Errorf("unsupported file operation type %q", opType)
	}
}

func (manager *Manager) finish(current operation.Operation, status operation.Status, state State) {
	encoded, err := json.Marshal(state)
	if err != nil {
		status = operation.StatusFailed
		encoded, _ = json.Marshal(State{Path: state.Path, Error: err.Error()})
	}
	current.Status = status
	current.State = jsontext.Value(encoded)
	manager.done <- current
}
