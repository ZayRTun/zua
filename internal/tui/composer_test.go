package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"unreal-agent-tui/internal/settings"
)

// resize returns a resized model ready for View assertions.
func resize(t *testing.T, width, height int) Model {
	t.Helper()
	current := tea.Model(New(t.TempDir(), settings.Settings{}, nil))
	current, _ = current.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return current.(Model)
}

// TestComposerRendersRules checks the Composer is a thin rule above, the
// accent ❯ prompt with the input, and a rule below — never a rounded
// border box (primary seam: Update → View).
func TestComposerRendersRules(t *testing.T) {
	m := resize(t, 100, 30)
	m.textarea.SetValue("build the thing")
	view := stripANSI(m.View())
	if strings.Contains(view, "╭") || strings.Contains(view, "╰") || strings.Contains(view, "╯") {
		t.Fatalf("composer must not render a rounded border:\n%s", view)
	}
	if !strings.Contains(view, "❯") || !strings.Contains(view, "build the thing") {
		t.Fatalf("composer missing the ❯ prompt with input:\n%s", view)
	}
	// The prompt line is exactly the ❯ prompt plus the text: no textarea
	// prompt glyph or line-number gutter may leak in front of the text.
	promptLine := composerPromptLine(t, m)
	if got := strings.TrimRight(stripANSI(promptLine), " "); got != "❯ build the thing" {
		t.Fatalf("composer prompt line = %q, want %q", got, "❯ build the thing")
	}
	rules := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.TrimRight(stripANSI(line), " ") == strings.Repeat("─", 100) {
			rules++
		}
	}
	if rules != 2 {
		t.Fatalf("composer needs exactly one rule above and one below, found %d full-width rules:\n%s", rules, view)
	}
}

// composerPromptLine returns the View line carrying the ❯ prompt.
func composerPromptLine(t *testing.T, m Model) string {
	t.Helper()
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(stripANSI(line), "❯") {
			return line
		}
	}
	t.Fatalf("no composer prompt line in view:\n%s", stripANSI(m.View()))
	return ""
}

// TestComposerPromptAccent pins the palette: accent ❯ prompt, dim rules
// (colors are asserted on the style helpers because lipgloss degrades to
// Ascii without a TTY).
func TestComposerPromptAccent(t *testing.T) {
	if got := composerPromptStyle.GetForeground(); got != accentColor {
		t.Fatalf("composer prompt %v, want accent %v", got, accentColor)
	}
	if got := composerRuleStyle.GetForeground(); got != dimColor {
		t.Fatalf("composer rule %v, want dim %v", got, dimColor)
	}
}

// TestComposerNoPlaceholderNoHint checks the glossary rules: no placeholder
// text and no hint line — the Usage Line (issue #13) replaces the hints.
func TestComposerNoPlaceholderNoHint(t *testing.T) {
	m := resize(t, 100, 30)
	if m.textarea.Placeholder != "" {
		t.Fatalf("placeholder must be empty, got %q", m.textarea.Placeholder)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "Describe a task") {
		t.Fatalf("placeholder text leaked into the view:\n%s", view)
	}
	if strings.Contains(view, "shift+enter newline") {
		t.Fatalf("hint line must not render:\n%s", view)
	}
}

// TestEnterSendsAndAltEnterNewlines checks editing keys are unchanged:
// Enter sends, alt/shift+enter inserts a newline and the editor grows.
func TestEnterSendsAndAltEnterNewlines(t *testing.T) {
	// Enter sends.
	m := resize(t, 100, 30)
	m.textarea.SetValue("fix the bug")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if !m.running {
		t.Fatal("enter did not send the prompt")
	}
	sent := stripANSI(m.View())
	if !strings.Contains(sent, "❯ fix the bug") {
		t.Fatalf("sent prompt missing from transcript:\n%s", sent)
	}

	// Alt+enter inserts a newline and grows the editor.
	m = resize(t, 100, 30)
	m.textarea.SetValue("line one")
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = current.(Model)
	if m.running {
		t.Fatal("alt+enter must not send")
	}
	if got := m.textarea.Value(); !strings.Contains(got, "\n") {
		t.Fatalf("alt+enter did not insert a newline: %q", got)
	}
	if m.textarea.Height() != 2 {
		t.Fatalf("editor height %d, want 2 after newline", m.textarea.Height())
	}
}

// Disambiguated enter sequences (kitty CSI u / xterm modifyOtherKeys) insert
// a newline — this is what shift+enter actually sends from terminals that
// distinguish it from Enter. Unrelated unknown CSI sequences stay inert.
func TestDisambiguatedEnterSequencesInsertNewline(t *testing.T) {
	for _, seq := range []string{
		"\x1b[13;2u",    // CSI u: shift+enter
		"\x1b[13;3u",    // CSI u: alt+enter (kitty encodes the modifier)
		"\x1b[27;2;13~", // xterm modifyOtherKeys: shift+enter
		"\x1b[27;3;13~", // xterm modifyOtherKeys: alt+enter
	} {
		m := resize(t, 100, 30)
		m.textarea.SetValue("line one")
		current, _ := tea.Model(m).Update(csiSequenceMsg(seq))
		m = current.(Model)
		if m.running {
			t.Fatalf("%q must not send the prompt", seq)
		}
		if got := m.textarea.Value(); !strings.Contains(got, "\n") {
			t.Fatalf("%q did not insert a newline: %q", seq, got)
		}
	}

	// Unrelated unknown CSI sequences must do nothing.
	m := resize(t, 100, 30)
	m.textarea.SetValue("line one")
	current, _ := tea.Model(m).Update(csiSequenceMsg("\x1b[1;5C"))
	m = current.(Model)
	if m.running {
		t.Fatal("unrelated CSI sequence sent the prompt")
	}
	if got := m.textarea.Value(); strings.Contains(got, "\n") {
		t.Fatalf("unrelated CSI sequence inserted a newline: %q", got)
	}
}

// CSIFilter passes normal messages through untouched — only bubbletea's
// unexported unknown CSI report gets converted.
func TestCSIFilterPassthrough(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.WindowSizeMsg{Width: 80, Height: 24},
		csiSequenceMsg("\x1b[13;2u"), // already converted — must not re-wrap
	} {
		if got := CSIFilter(nil, msg); !reflect.DeepEqual(got, msg) {
			t.Fatalf("CSIFilter altered %T", msg)
		}
	}
}

// TestComposerRespectsWidth checks the composer's rules, prompt line, and
// Usage Line never overflows a narrow terminal.
func TestComposerRespectsWidth(t *testing.T) {
	m := resize(t, 40, 20)
	view := m.View()
	rules := countFullWidthRules(t, view, 40)
	if rules != 2 {
		t.Fatalf("want the composer's two rules at width 40, found %d:\n%s", rules, stripANSI(view))
	}
	for _, line := range strings.Split(view, "\n") {
		plain := strings.TrimRight(stripANSI(line), " ")
		if width := lipgloss.Width(plain); width > 40 {
			t.Fatalf("view line %d wide exceeds terminal width 40: %q", width, plain)
		}
	}
}

// TestLayoutHeightIsDynamic pins the structural requirement: the bottom
// section's height is computed from its parts (Usage Line, Command Menu,
// Composer rules + editor lines), never hardcoded, so the viewport absorbs
// the difference.
func TestLayoutHeightIsDynamic(t *testing.T) {
	// A taller editor takes lines from the viewport (rules = editor + 2).
	m := resize(t, 100, 30)
	base := m.viewport.Height
	m.textarea.SetValue("one\ntwo\nthree")
	m.resizeEditor()
	if m.viewport.Height != base-2 {
		t.Fatalf("viewport height %d with 3-line editor, want %d", m.viewport.Height, base-2)
	}
}
