package tui

// Chrome restyle tests (issue #6): status line mode+model, spinner verbs,
// dim-gray + accent-12 palette, and NO_COLOR / narrow-width degradation.
// All assertions go through the Update → View seam; palette checks use style
// getters (lipgloss renders Ascii in the no-TTY test environment).

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	termenv "github.com/muesli/termenv"

	"unreal-agent-tui/internal/settings"
)

// sizeModel re-flows an existing model to a terminal size via the real
// WindowSizeMsg path.
func sizeModel(t *testing.T, m Model, width, height int) Model {
	t.Helper()
	current, _ := tea.Model(m).Update(tea.WindowSizeMsg{Width: width, Height: height})
	return current.(Model)
}

func TestStatusLineShowsModel(t *testing.T) {
	// Provider default model.
	m := resize(t, 100, 30)
	view := stripANSI(m.View())
	if !strings.Contains(view, "(default model)") {
		t.Fatalf("status line missing model id:\n%s", view)
	}
	// No mode label: the tool configuration has no mode concept (glossary).
	if strings.Contains(view, "pristine") || strings.Contains(view, "file-tools") {
		t.Fatalf("status line must not show a mode label:\n%s", view)
	}

	// /model sets the model id, visible in the status line.
	m = sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 30)
	m = typeKeys(t, m, "/model claude-sonnet-4-5")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if view = stripANSI(m.View()); !strings.Contains(view, "claude-sonnet-4-5") {
		t.Fatalf("status line missing set model id:\n%s", view)
	}
}

func TestSpinnerVerbPerTurn(t *testing.T) {
	m := resize(t, 100, 30)
	m.textarea.SetValue("do the thing")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if !m.running {
		t.Fatal("enter must start a turn")
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "working…") {
		t.Fatalf("static working… should be replaced by a verb:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Fatalf("spinner verb missing:\n%s", view)
	}
	// The verb is one of the fixed list and stable within the turn.
	var verb string
	for _, candidate := range spinnerVerbs {
		if strings.Contains(view, candidate) {
			verb = candidate
			break
		}
	}
	if verb == "" {
		t.Fatalf("no known spinner verb in view:\n%s", view)
	}
	if again := stripANSI(m.View()); !strings.Contains(again, verb) {
		t.Fatalf("verb changed between renders of one turn:\n%s\n%s", view, again)
	}
}

func TestChromeUsesDimGrayAccentPalette(t *testing.T) {
	// Single accent: the status/menu chrome accent moved from cyan 6 to 12.
	if got := statusStyle.GetForeground(); got != lipgloss.Color("12") {
		t.Fatalf("statusStyle accent = %v, want accent 12", got)
	}
	if got := toolStyle.GetForeground(); got != lipgloss.Color("8") {
		t.Fatalf("toolStyle = %v, want dim gray 8", got)
	}
	if got := okStyle.GetForeground(); got != lipgloss.Color("12") {
		t.Fatalf("okStyle = %v, want accent 12", got)
	}
	// Rendered check: header title and ok glyph carry the accent color when
	// the profile allows it (ANSI256 renders color 12 as bright-blue 94).
	prior := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(prior)
	m := resize(t, 100, 30)
	m.appendBlock(block{kind: blockTool, tool: &toolCard{name: "Bash", argsRaw: `{"command":"go build"}`, status: "ok"}})
	m.refresh()
	view := m.View()
	if !strings.Contains(view, "\x1b[94m") {
		t.Fatalf("rendered chrome missing accent-12 color:\n%q", view)
	}
	if !strings.Contains(view, "\x1b[94m⏺") {
		t.Fatalf("ok tool glyph missing accent color:\n%q", view)
	}
}

func TestChromePlainUnderNoColor(t *testing.T) {
	// Ascii profile is what NO_COLOR / no-color terminals degrade to (lipgloss
	// resolves this at startup); chrome must render as plain readable text.
	m := resize(t, 100, 30)
	m.appendBlock(block{kind: blockTool, tool: &toolCard{name: "Bash", argsRaw: `{"command":"go build"}`, status: "ok"}})
	m.refresh()
	view := m.View()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("chrome emitted ANSI escapes under a no-color profile:\n%q", view)
	}
	plain := stripANSI(view)
	for _, want := range []string{"unreal-agent", "⏺ Bash", "idle"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain chrome missing %q:\n%s", want, plain)
		}
	}
}

func TestChromeFitsNarrowWidth(t *testing.T) {
	m := resize(t, 44, 24)
	m.appendBlock(block{kind: blockUser, text: "refactor the session loader please"})
	m.appendBlock(block{kind: blockTool, tool: &toolCard{name: "Bash", argsRaw: `{"command":"go build ./... && go vet ./..."}`, status: "ok"}})
	m.appendBlock(block{kind: blockAssistant, text: "Done — split the loader into scan plus replay, every test green."})
	m.refresh()
	m.textarea.SetValue("do the thing")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)

	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(strings.TrimRight(stripANSI(line), " ")); w > 44 {
			t.Fatalf("chrome line %d wide overflows terminal width 44: %q", w, stripANSI(line))
		}
	}
}

// pickVerb is exercised indirectly through Update; pin that the list is
// non-empty, every pick is a known verb, and repeated picks cover the list.
func TestPickVerb(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		verb := pickVerb()
		if verb == "" {
			t.Fatal("pickVerb returned empty")
		}
		if !strings.HasSuffix(verb, "…") {
			t.Fatalf("verb %q is not an ellipsis-ended phrase", verb)
		}
		seen[verb] = true
	}
	if len(seen) != len(spinnerVerbs) {
		t.Fatalf("200 picks covered %d of %d verbs", len(seen), len(spinnerVerbs))
	}
}
