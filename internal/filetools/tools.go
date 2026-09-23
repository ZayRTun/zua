// Package filetools adds Read, Write, and Edit file tools to the unreal-agent
// harness, following the same translator/operation pattern as the built-in
// Bash tool.
package filetools

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// Operation types handled by this package's operation manager wrapper.
const (
	TypeRead  = operation.Type("file_read")
	TypeWrite = operation.Type("file_write")
	TypeEdit  = operation.Type("file_edit")
)

const defaultMaxOutputLength = 40_000

// Tool names as the model sees them.
const (
	ReadName  = "Read"
	WriteName = "Write"
	EditName  = "Edit"
)

// State is the serializable state of every file operation, both the request
// arguments (recorded at translate time) and the terminal result (recorded at
// execution time).
type State struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	OldText    string `json:"old_text,omitempty"`
	NewText    string `json:"new_text,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Registry wraps a harness tool.Registry and adds the file tools.
type Registry struct {
	Inner   tool.Registry
	File    []tool.Definition
	fileMap map[string]tool.Translator
}

var _ tool.Registry = (*Registry)(nil)

// NewRegistry wraps inner with the Read/Write/Edit tools rooted at directory.
func NewRegistry(inner tool.Registry, directory string) *Registry {
	read := &translator{opType: TypeRead, directory: directory}
	write := &translator{opType: TypeWrite, directory: directory}
	edit := &translator{opType: TypeEdit, directory: directory}
	return &Registry{
		Inner: inner,
		File:  []tool.Definition{read.Definition(), write.Definition(), edit.Definition()},
		fileMap: map[string]tool.Translator{
			ReadName:  read,
			WriteName: write,
			EditName:  edit,
		},
	}
}

func (registry *Registry) StaticDefinitions() []tool.Definition {
	return append(registry.Inner.StaticDefinitions(), registry.File...)
}

func (registry *Registry) Resolve(name string) (tool.Translator, bool) {
	if translator, exists := registry.fileMap[name]; exists {
		return translator, true
	}
	return registry.Inner.Resolve(name)
}

func (registry *Registry) RegisterSkill(skill tool.Skill) (tool.RegistrationID, error) {
	return registry.Inner.RegisterSkill(skill)
}

func (registry *Registry) UnregisterSkill(id tool.RegistrationID) {
	registry.Inner.UnregisterSkill(id)
}

func (registry *Registry) Skills() []tool.Skill {
	return registry.Inner.Skills()
}

// translator implements tool.Translator for each file tool.
type translator struct {
	opType    operation.Type
	directory string
}

func (translator *translator) Definition() tool.Definition {
	return tool.Definition{Tool: toolSchema(translator.opType)}
}

func toolSchema(opType operation.Type) llm.Tool {
	switch opType {
	case TypeRead:
		return llm.Tool{
			Type:        llm.ToolFunction,
			Name:        ReadName,
			Description: "Read a text file inside the workspace. Returns the file content. Optionally read a slice of lines starting at 1-based offset.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "File path relative to the workspace root."},
					"offset": map[string]any{"type": "integer", "description": "1-based line number to start reading from."},
					"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read."},
				},
				"required": []string{"path"},
			},
		}
	case TypeWrite:
		return llm.Tool{
			Type:        llm.ToolFunction,
			Name:        WriteName,
			Description: "Create or overwrite a file inside the workspace with the given content. Parent directories are created as needed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "File path relative to the workspace root."},
					"content": map[string]any{"type": "string", "description": "Complete new file content."},
				},
				"required": []string{"path", "content"},
			},
		}
	default: // TypeEdit
		return llm.Tool{
			Type:        llm.ToolFunction,
			Name:        EditName,
			Description: "Replace text in an existing file inside the workspace. old_text must match exactly and be unique in the file unless replace_all is true.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        map[string]any{"type": "string", "description": "File path relative to the workspace root."},
					"old_text":    map[string]any{"type": "string", "description": "Exact text to replace."},
					"new_text":    map[string]any{"type": "string", "description": "Replacement text."},
					"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."},
				},
				"required": []string{"path", "old_text", "new_text"},
			},
		}
	}
}

func (translator *translator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	state, limit, err := translator.validate(call.Arguments)
	if err != nil {
		return tool.ErrorStatus(err.Error(), limit)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return tool.ErrorStatus(fmt.Sprintf("encode %s arguments: %v", translator.opType, err), limit)
	}
	spec := operation.Spec{
		Type:            translator.opType,
		Version:         1,
		State:           jsontext.Value(encoded),
		MaxOutputLength: defaultMaxOutputLength,
	}
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}

func (translator *translator) validate(encoded string) (State, int, error) {
	var state State
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return State{}, 0, fmt.Errorf("decode %s arguments: %w", translator.opType, err)
	}
	if strings.TrimSpace(state.Path) == "" {
		return State{}, 0, fmt.Errorf("path is required")
	}
	relative, err := filepath.Rel(translator.directory, filepath.Join(translator.directory, state.Path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return State{}, 0, fmt.Errorf("path %q escapes the workspace", state.Path)
	}
	state.Path = relative
	switch translator.opType {
	case TypeRead:
		if state.Offset < 0 || state.Limit < 0 {
			return State{}, 0, fmt.Errorf("offset and limit must be non-negative")
		}
	case TypeWrite:
		// Content may be empty (truncate), nothing to validate.
	case TypeEdit:
		if state.OldText == "" {
			return State{}, 0, fmt.Errorf("old_text is required")
		}
	}
	return state, 0, nil
}

func (translator *translator) TranslateResult(
	callID string,
	status tool.CallStatus,
	operations []operation.Operation,
) (llm.ToolResult, error) {
	if status.Error != "" {
		if len(operations) != 0 {
			return llm.ToolResult{}, fmt.Errorf("%s tool call %q has both a validation error and operations", translator.opType, callID)
		}
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Error: " + status.Error}}}, nil
	}
	if len(operations) != 1 {
		return llm.ToolResult{}, fmt.Errorf("%s tool call %q has %d operations, want 1", translator.opType, callID, len(operations))
	}
	current := operations[0]
	if current.Type != translator.opType {
		return llm.ToolResult{}, fmt.Errorf("%s tool call %q operation %q has type %q, want %q", translator.opType, callID, current.ID, current.Type, translator.opType)
	}
	var state State
	if err := json.Unmarshal(current.State, &state); err != nil {
		return llm.ToolResult{}, fmt.Errorf("decode %s operation %q state: %w", translator.opType, current.ID, err)
	}
	switch current.Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "File operation is still running."}}}, nil
	case operation.StatusCompleted:
		text := state.Result
		if text == "" {
			text = "(no output)"
		}
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}, nil
	case operation.StatusFailed, operation.StatusCanceled:
		message := state.Error
		if message == "" {
			message = string(current.Status) + " " + string(translator.opType) + " operation"
		}
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Error: " + message}}}, nil
	default:
		return llm.ToolResult{}, fmt.Errorf("%s operation %q has invalid status %q", translator.opType, current.ID, current.Status)
	}
}
