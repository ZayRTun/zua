package tui

// Terminals that disambiguate keys (kitty protocol, xterm modifyOtherKeys)
// send distinct escape sequences for modified keys — shift+enter is not a
// bare \r there. bubbletea v1 can't parse these CSI sequences and reports
// them as an unexported message type the model can't name, so CSIFilter
// (wired via tea.WithFilter) surfaces them as csiSequenceMsg. Terminals
// that don't disambiguate send a bare \r for shift+enter — indistinguishable
// from Enter; alt+enter (esc+return) is the working fallback there.

import (
	"fmt"
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
)

// csiSequenceMsg is an escape sequence bubbletea v1 could not parse (for
// example the kitty-protocol shift+enter), surfaced by CSIFilter.
type csiSequenceMsg []byte

// modifiedEnterSequences are the disambiguated encodings of shift+enter and
// alt+enter (the modifiers 2/3 applied to enter, keycode 13).
var modifiedEnterSequences = [][]byte{
	[]byte("\x1b[13;2u"),    // CSI u (kitty): shift+enter
	[]byte("\x1b[13;3u"),    // CSI u (kitty): alt+enter
	[]byte("\x1b[27;2;13~"), // xterm modifyOtherKeys: shift+enter
	[]byte("\x1b[27;3;13~"), // xterm modifyOtherKeys: alt+enter
}

// isModifiedEnter reports whether the sequence encodes shift+enter or
// alt+enter.
func isModifiedEnter(seq csiSequenceMsg) bool {
	for _, candidate := range modifiedEnterSequences {
		if string(seq) == string(candidate) {
			return true
		}
	}
	return false
}

// CSIFilter converts bubbletea's unexported unknownCSISequenceMsg into an
// exported csiSequenceMsg; every other message passes through untouched.
// Wire it with tea.WithFilter.
func CSIFilter(_ tea.Model, msg tea.Msg) tea.Msg {
	if fmt.Sprintf("%T", msg) != "tea.unknownCSISequenceMsg" {
		return msg
	}
	// The message is a named byte-slice type; Bytes() copies its contents.
	return csiSequenceMsg(reflect.ValueOf(msg).Bytes())
}
