package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// resize returns a resized model ready for View assertions.
func resize(t *testing.T, width, height int) Model {
	t.Helper()
	current := tea.Model(New(t.TempDir(), "", "", false, nil))
	current, _ = current.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return current.(Model)
}

// TestPromptBoxRendersRoundedBox checks the input renders as a rounded
// bordered box with the "> " prompt inside (primary seam: Update → View).
func TestPromptBoxRendersRoundedBox(t *testing.T) {
	m := resize(t, 100, 30)
	view := stripANSI(m.View())
	for _, want := range []string{"╭", "╰", "╯", "> "} {
		if !strings.Contains(view, want) {
			t.Fatalf("prompt box missing %q:\n%s", want, view)
		}
	}
}

// TestPromptBoxBorderBrightensOnFocus checks the border is dim when the
// textarea is unfocused and the brighter accent when focused. Rendered ANSI
// colors can't be asserted here (lipgloss degrades to no color without a
// TTY), so the color decision is pinned on the style helper and the View
// structure on the box itself.
func TestPromptBoxBorderBrightensOnFocus(t *testing.T) {
	m := resize(t, 100, 30)
	if !m.textarea.Focused() {
		t.Fatal("setup: textarea should start focused")
	}
	if got := promptBorderStyleFor(m.textarea.Focused()).GetBorderTopForeground(); got != focusBorder {
		t.Fatalf("focused border %v, want %v", got, focusBorder)
	}
	m.textarea.Blur()
	if got := promptBorderStyleFor(m.textarea.Focused()).GetBorderTopForeground(); got != dimBorder {
		t.Fatalf("blurred border %v, want %v", got, dimBorder)
	}
	view := stripANSI(m.View())
	for _, want := range []string{"╭", "╰"} {
		if !strings.Contains(view, want) {
			t.Fatalf("blurred view lost the rounded box:\n%s", view)
		}
	}
}

// TestHintLineIdleAndRunning checks the contextual hint line appears below
// the box when idle and hides while a Turn runs. The turn starts through the
// real Update path (Enter) so the whole seam is exercised.
func TestHintLineIdleAndRunning(t *testing.T) {
	m := resize(t, 100, 30)
	const hint = "shift+enter newline · ctrl+o verbose · /help commands"
	idle := stripANSI(m.View())
	if !strings.Contains(idle, hint) {
		t.Fatalf("idle view missing hint line:\n%s", idle)
	}
	if strings.Contains(idle, hint+"\n"+hint) {
		t.Fatal("hint line rendered twice")
	}

	m.textarea.SetValue("a prompt")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if !m.running || m.hintsVisible() {
		t.Fatalf("turn start state wrong: running=%v hintsVisible=%v", m.running, m.hintsVisible())
	}
	running := stripANSI(m.View())
	if strings.Contains(running, hint) {
		t.Fatalf("hint line must hide while a turn runs:\n%s", running)
	}
	if !strings.Contains(running, "╭") {
		t.Fatalf("running view lost the prompt box:\n%s", running)
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

// TestPromptBoxRespectsWidth checks the box and hint line never overflow a
// narrow terminal. (The header is a separate concern, out of scope here.)
func TestPromptBoxRespectsWidth(t *testing.T) {
	m := resize(t, 40, 20)
	view := m.View()
	var boxLines []string
	var hintLine string
	for _, line := range strings.Split(view, "\n") {
		plain := stripANSI(line)
		switch {
		case strings.ContainsAny(plain, "╭╰") || strings.HasPrefix(plain, "> "):
			boxLines = append(boxLines, line)
		case strings.Contains(plain, "shift+enter newline"):
			hintLine = line
		}
	}
	if len(boxLines) == 0 {
		t.Fatalf("no prompt box lines found in view:\n%s", stripANSI(view))
	}
	for _, line := range boxLines {
		// JoinVertical pads shorter lines with trailing spaces up to the
		// widest line (the header's workspace path); that padding is
		// cosmetic, so measure the actual rendered content.
		if width := lipgloss.Width(strings.TrimRight(stripANSI(line), " ")); width > 40 {
			t.Fatalf("box line %d wide exceeds terminal width 40: %q", width, stripANSI(line))
		}
	}
	if width := lipgloss.Width(strings.TrimRight(stripANSI(hintLine), " ")); width > 40 {
		t.Fatalf("hint line %d wide exceeds terminal width 40: %q", width, stripANSI(hintLine))
	}
}

// TestLayoutHeightIsDynamic pins the structural requirement: the bottom
// section's height is computed from its parts (editor lines, hint, palette),
// never hardcoded, so the viewport absorbs the difference.
func TestLayoutHeightIsDynamic(t *testing.T) {
	m := resize(t, 100, 30)
	idleHeight := m.viewport.Height

	// Hiding the hint line (turn running) gives one line back to the viewport.
	// The turn start's refresh re-flows the layout in the live app; a resize
	// exercises the same Update path here.
	m.running = true
	current, _ := tea.Model(m).Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = current.(Model)
	if m.viewport.Height != idleHeight+1 {
		t.Fatalf("viewport height %d with hint hidden, want %d", m.viewport.Height, idleHeight+1)
	}

	// A taller editor takes lines from the viewport (box = editor + border).
	m = resize(t, 100, 30)
	base := m.viewport.Height
	m.textarea.SetValue("one\ntwo\nthree")
	m.resizeEditor()
	if m.viewport.Height != base-2 {
		t.Fatalf("viewport height %d with 3-line editor, want %d", m.viewport.Height, base-2)
	}
}
