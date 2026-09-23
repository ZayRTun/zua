package filetools

import (
	"encoding/json"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func TestValidateRejectsPathEscape(t *testing.T) {
	translator := &translator{opType: TypeRead, directory: t.TempDir()}
	for _, path := range []string{"../outside.txt", ".."} {
		if _, _, err := translator.validate(`{"path":"` + path + `"}`); err == nil {
			t.Errorf("validate(%q) succeeded, want workspace-escape error", path)
		}
	}
}

func TestManagerReadWriteEdit(t *testing.T) {
	directory := t.TempDir()
	ctx := t.Context()
	manager := NewManager(ctx, stubManager{}, directory)

	tests := []struct {
		name    string
		opType  operation.Type
		state   string
		want    string
		failure string
	}{
		{
			name:   "write",
			opType: TypeWrite,
			state:  `{"path":"a/b.txt","content":"hello world"}`,
			want:   "Wrote 11 bytes to a/b.txt.",
		},
		{
			name:   "read",
			opType: TypeRead,
			state:  `{"path":"a/b.txt"}`,
			want:   "1\thello world\n",
		},
		{
			name:   "edit",
			opType: TypeEdit,
			state:  `{"path":"a/b.txt","old_text":"world","new_text":"there"}`,
			want:   "Edited a/b.txt: replaced 1 occurrence(s).",
		},
		{
			name:    "edit missing",
			opType:  TypeEdit,
			state:   `{"path":"a/b.txt","old_text":"nope","new_text":"x"}`,
			failure: `old_text not found in a/b.txt`,
		},
	}

	operationID := operation.ID("op-1")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager.Add(operation.Operation{
				ID: operationID, Type: test.opType, Version: 1,
				State: jsontext.Value(test.state),
			})
			update := <-manager.Updates()
			var state State
			if err := jsonUnmarshal(update.State, &state); err != nil {
				t.Fatalf("decode state: %v", err)
			}
			if test.failure != "" {
				if update.Status != operation.StatusFailed || !strings.Contains(state.Error, test.failure) {
					t.Fatalf("got status %q error %q, want failure containing %q", update.Status, state.Error, test.failure)
				}
				return
			}
			if update.Status != operation.StatusCompleted {
				t.Fatalf("got status %q error %q, want completed", update.Status, state.Error)
			}
			if state.Result != test.want {
				t.Fatalf("got result %q, want %q", state.Result, test.want)
			}
		})
	}

	content, err := os.ReadFile(filepath.Join(directory, "a", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello there" {
		t.Fatalf("file content %q, want %q", content, "hello there")
	}
}

// stubManager satisfies operation.Manager without running anything; the file
// manager should never delegate the types used in the test above.
type stubManager struct{}

func (stubManager) Add(operation.Operation) error       { return operation.ErrUnsupported }
func (stubManager) Cancel(operation.ID, string) error   { return nil }
func (stubManager) Updates() <-chan operation.Operation { return make(chan operation.Operation) }

func jsonUnmarshal(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}
