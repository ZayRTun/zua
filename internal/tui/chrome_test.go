package tui

// Chrome tests: Header, Usage Line, NO_COLOR degradation, spinner verbs,
// dim-gray + accent-12 palette, and NO_COLOR / narrow-width degradation.
// All assertions go through the Update → View seam; palette checks use style
// getters (lipgloss renders Ascii in the no-TTY test environment).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fmt"
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

// TestFreshSessionShowsHeaderBlock pins the launch decision (amendment to
// the headerless-transcript rule): zua boots straight into a fresh session
// whose transcript starts with the Header as a welcome block — visible at
// launch, scrolling away with the conversation. No pinned chrome, no extra
// rule, and no picker at boot (the launcher stays on /resume).
func TestFreshSessionShowsHeaderBlock(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash", ThinkingLevel: "high"}, nil), 160, 30)
	if m.picking {
		t.Fatal("launch must not open the picker")
	}
	view := stripANSI(m.View())
	for _, want := range []string{"zua", workspace, "GLM-5.3-Flash · high"} {
		if !strings.Contains(view, want) {
			t.Fatalf("fresh session missing header content %q:\n%s", want, view)
		}
	}
	// The Header block is content, not chrome: still only the Composer's
	// two rules.
	if rules := countFullWidthRules(t, m.View(), 160); rules != 2 {
		t.Fatalf("fresh session must show only the composer's two rules, found %d:\n%s", rules, view)
	}
}

// TestNewCommandShowsFreshHeader pins that /new starts the fresh session
// with the welcome Header block too.
func TestNewCommandShowsFreshHeader(t *testing.T) {
	// Tall terminal: the closing divider and the fresh Header must both be
	// in view at once.
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 60)
	m = typeKeys(t, m, "/new")
	// First Enter accepts the Command Menu entry ("/new"), the second sends.
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	view := stripANSI(m.View())
	for _, want := range []string{"zua", "new session"} {
		if !strings.Contains(view, want) {
			t.Fatalf("/new transcript missing %q:\n%s", want, view)
		}
	}
}

func TestUsageLineShowsModel(t *testing.T) {
	// Provider default model.
	m := resize(t, 100, 30)
	view := stripANSI(m.View())
	if !strings.Contains(view, "(default model)") {
		t.Fatalf("Usage Line missing model id:\n%s", view)
	}
	// No mode label: the tool configuration has no mode concept (glossary).
	if strings.Contains(view, "pristine") || strings.Contains(view, "file-tools") {
		t.Fatalf("Usage Line must not show a mode label:\n%s", view)
	}

	// /model sets the model id, visible in the Usage Line.
	m = sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 30)
	m = typeKeys(t, m, "/model claude-sonnet-4-5")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if view = stripANSI(m.View()); !strings.Contains(view, "claude-sonnet-4-5") {
		t.Fatalf("Usage Line missing set model id:\n%s", view)
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
	for _, want := range []string{"⏺ Bash", "•"} {
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

// TestHeaderPlainUnderNoColor degrades the whole launcher — logo, name,
// path, stats, picker — to plain text under a no-color profile.
func TestHeaderPlainUnderNoColor(t *testing.T) {
	m := resize(t, 100, 30)
	current, _ := tea.Model(m).Update(sessionsMsg{})
	m = current.(Model)
	view := m.View()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("launcher emitted ANSI escapes under a no-color profile:\n%q", view)
	}
	plain := stripANSI(view)
	for _, want := range []string{"zua", "skills", "Resume Session"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain launcher missing %q:\n%s", want, plain)
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

// ---- Header (issue #12) ----

// countFullWidthRules counts lines that are exactly width dashes after
// stripping color and trailing padding.
func countFullWidthRules(t *testing.T, view string, width int) int {
	t.Helper()
	rules := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.TrimRight(stripANSI(line), " ") == strings.Repeat("─", width) {
			rules++
		}
	}
	return rules
}

// TestTranscriptViewLosesHeaderAndDivider checks a resumed transcript
// renders with no header and no chrome divider — the welcome Header block
// belongs only to fresh sessions (resumed ones replace the blocks
// wholesale); only the Composer's own rules remain.
func TestTranscriptViewLosesHeaderAndDivider(t *testing.T) {
	m := resize(t, 100, 30)
	// Simulate a resumed session: blocks replaced wholesale, like
	// loadSession does.
	m.blocks = []block{{kind: blockUser, text: "earlier turn"}}
	m.refresh()
	view := stripANSI(m.View())
	if strings.Contains(view, "unreal-agent") || strings.Contains(view, "zua") {
		t.Fatalf("resumed transcript view must not render a header:\n%s", view)
	}
	if rules := countFullWidthRules(t, m.View(), 100); rules != 2 {
		t.Fatalf("transcript view must show only the composer's two rules, found %d:\n%s", rules, view)
	}
}

// TestLauncherShowsHeader drives the resume screen through the real Update
// path and checks the Header: logo art, name, workspace path, and the live
// stats line (model · thinking level · skills count).
func TestLauncherShowsHeader(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash", ThinkingLevel: "high"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{{title: "old work", updated: "2m ago"}}})
	m = current.(Model)
	if !m.picking {
		t.Fatal("sessionsMsg must open the launcher")
	}
	view := stripANSI(m.View())
	stats := "GLM-5.3-Flash · high · " + itoa(int64(len(m.skills))) + " skills"
	for _, want := range []string{"zua", workspace, stats, "old work", "❯"} {
		if !strings.Contains(view, want) {
			t.Fatalf("launcher missing %q:\n%s", want, view)
		}
	}
}

// TestLauncherKeepsComposerGrounded pins the launcher layout rule: the
// picker opens as a bottom part above the Composer (where the Command Menu
// sits when you type '/'), the transcript stays visible above it, and the
// view still fills the terminal — the Composer never floats up with dead
// space below.
func TestLauncherKeepsComposerGrounded(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 40)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{title: "older session", updated: "1h ago"},
		{title: "newer session", updated: "2m ago"},
	}})
	m = current.(Model)
	view := m.View()
	if lipgloss.Height(view) != 40 {
		t.Fatalf("launcher view must fill the terminal (Composer grounded), got %d rows:\n%s",
			lipgloss.Height(view), stripANSI(view))
	}
	plain := stripANSI(view)
	for _, want := range []string{"Resume Session", "newer session", "zua"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("launcher missing %q:\n%s", want, plain)
		}
	}
}

// TestLauncherCapsPickerRows pins that the picker height is capped like the
// Command Menu's: a scrolling window around the selection, never the whole
// unbounded session list.
func TestLauncherCapsPickerRows(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 40)
	var entries []sessionEntry
	for i := 0; i < 20; i++ {
		entries = append(entries, sessionEntry{title: fmt.Sprintf("session-%02d", i), updated: "1h ago"})
	}
	current, _ := tea.Model(m).Update(sessionsMsg{entries: entries})
	m = current.(Model)
	// ↓ three times: selection moves to the fourth row; the window starts
	// at the top, so early rows stay visible and rows past the cap vanish.
	for i := 0; i < 3; i++ {
		current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyDown})
		m = current.(Model)
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "session-03") {
		t.Fatalf("selected row must be visible:\n%s", plain)
	}
	if strings.Contains(plain, "session-08") {
		t.Fatalf("picker rows must be capped — row 8 leaked:\n%s", plain)
	}
	if !strings.Contains(plain, "(4/20)") {
		t.Fatalf("picker footer must report selection and total:\n%s", plain)
	}
}

// TestHeaderSkillsCountRefreshesOnReload checks the stats line reads the
// live skill list: the count changes after /reload discovers another skill.
func TestHeaderSkillsCountRefreshesOnReload(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash", ThinkingLevel: "high"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(sessionsMsg{})
	m = current.(Model)

	before := headerSkillsCount(t, m)
	writeSkill(t, filepath.Join(workspace, ".harness", "skills", "late-skill"),
		"name: late-skill\ndescription: arrives after startup", "Do the thing.")
	// Slash input only exists in the transcript view, so dismiss the
	// launcher first: the picker consumes keys while it is open.
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = current.(Model)
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/reload")})
	// First enter accepts the Command Menu entry, the second sends.
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	// The launcher re-renders from the refreshed skill list.
	current, _ = tea.Model(m).Update(sessionsMsg{})
	m = current.(Model)
	if after := headerSkillsCount(t, m); after != before+1 {
		t.Fatalf("skills count %d after /reload, want %d", after, before+1)
	}
}

// headerSkillsCount reads the N out of the launcher header's stats line.
func headerSkillsCount(t *testing.T, m Model) int {
	t.Helper()
	view := stripANSI(m.View())
	for _, line := range strings.Split(view, "\n") {
		if index := strings.Index(line, "· "); index >= 0 && strings.Contains(line, "skills") {
			parts := strings.Split(strings.TrimSpace(line), " · ")
			if len(parts) == 3 {
				n := strings.TrimSuffix(parts[2], " skills")
				count, err := strconv.Atoi(n)
				if err != nil {
					t.Fatalf("unparseable skills count %q in %q", n, line)
				}
				return count
			}
		}
	}
	t.Fatalf("no stats line in launcher view:\n%s", view)
	return -1
}

// TestHeaderFitsNarrowWidth clips the launcher header lines to the terminal
// width so narrow sizes never wrap or overflow.
func TestHeaderFitsNarrowWidth(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash", ThinkingLevel: "high"}, nil), 44, 24)
	current, _ := tea.Model(m).Update(sessionsMsg{})
	m = current.(Model)
	for _, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(strings.TrimRight(stripANSI(line), " ")); w > 44 {
			t.Fatalf("header line %d wide overflows terminal width 44: %q", w, stripANSI(line))
		}
	}
}

// TestPickerFiltersFromComposer pins that the Composer is the picker's
// filter while the popup is open: typing narrows the rows (case-insensitive
// substring on the title), backspacing restores them, and the footer
// reports the position in the filtered list.
func TestPickerFiltersFromComposer(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 40)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{title: "analyze this project", updated: "Sep 23 18:21"},
		{title: "hello", updated: "Sep 23 09:20"},
		{title: "hello two", updated: "Sep 23 09:10"},
		{title: "unrelated work", updated: "Sep 23 09:03"},
	}})
	m = current.(Model)
	m = typeKeys(t, m, "HEL")
	plain := stripANSI(m.View())
	if strings.Contains(plain, "analyze this project") || strings.Contains(plain, "unrelated work") {
		t.Fatalf("filter must narrow the rows:\n%s", plain)
	}
	for _, want := range []string{"hello", "hello two", "Resume Session - (1/2)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("filtered picker missing %q:\n%s", want, plain)
		}
	}
	// Backspace clears the filter and all rows return.
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyBackspace})
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyBackspace})
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = current.(Model)
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("backspace did not clear the filter: %q", got)
	}
	plain = stripANSI(m.View())
	for _, want := range []string{"analyze this project", "unrelated work", "Resume Session - (1/4)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("unfiltered picker missing %q:\n%s", want, plain)
		}
	}
}

// TestPickerNoMatchShowsPlaceholder pins the empty-filter result: a dim
// "no matching sessions" row instead of an empty panel.
func TestPickerNoMatch(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 40)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{title: "hello", updated: "Sep 23 09:20"},
	}})
	m = current.(Model)
	m = typeKeys(t, m, "zzz")
	plain := stripANSI(m.View())
	for _, want := range []string{"no matching sessions", "Resume Session - (0/0)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("empty filter result missing %q:\n%s", want, plain)
		}
	}
}

// TestPickerEscResetsComposer pins Esc: the picker dismisses and the
// Composer returns to its normal, empty state (no filter text left).
func TestPickerEscResetsComposer(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 40)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{{title: "hello", updated: "Sep 23 09:20"}}})
	m = current.(Model)
	m = typeKeys(t, m, "hel")
	// The Composer really is the filter: the text must land and narrow the
	// list before Esc clears it.
	if got := m.textarea.Value(); got != "hel" {
		t.Fatalf("typed keys must reach the composer as the filter, got %q", got)
	}
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = current.(Model)
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("esc must reset the composer, got %q", got)
	}
	if m.picking {
		t.Fatal("esc must dismiss the picker")
	}
}

// TestPickerEnterLoadsAndResetsComposer drives a real session file through
// the full path: the Composer filters, Enter loads the highlighted session,
// and the Composer returns to its normal empty state.
func TestPickerEnterLoadsAndResetsComposer(t *testing.T) {
	workspace := t.TempDir()
	id := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(workspace, ".harness", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := "{\"type\":\"session\",\"data\":{\"Version\":2,\"Session\":{\"ID\":\"" + id +
		"\",\"CreatedAt\":\"2026-09-23T09:10:57.144039Z\"}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":1,\"RecordedAt\":\"2026-09-23T09:10:57.165612Z\"," +
		"\"Kind\":\"input\",\"Data\":{\"ID\":\"c079e8cf-d55d-4ac3-b3f1-cacb2ffb3030\",\"Kind\":\"external\"," +
		"\"Payload\":\"resume me please\"}}}}\n"
	if err := os.WriteFile(filepath.Join(dir, id+".session.jsonl"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sizeModel(t, New(workspace, settings.Settings{}, nil), 100, 40)
	// Two sessions so the filter decides what Enter loads: the decoy is
	// first in the list, but the filter narrows to the target.
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{title: "decoy session", updated: "Sep 23 19:00"},
		{id: id, title: "resume me please", updated: "Sep 23 09:20"},
	}})
	m = current.(Model)
	m = typeKeys(t, m, "resume")
	if plain := stripANSI(m.View()); strings.Contains(plain, "decoy session") {
		t.Fatalf("filter must narrow to the target session:\n%s", plain)
	}
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if m.picking {
		t.Fatal("enter must load the session and dismiss the picker")
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("composer must return to its normal empty state, got %q", got)
	}
	if m.sessionID != id {
		t.Fatalf("sessionID = %q, want the loaded session", m.sessionID)
	}
	if plain := stripANSI(m.View()); !strings.Contains(plain, "resume me please") {
		t.Fatalf("loaded transcript missing the session's prompt:\n%s", plain)
	}
}

// TestPickerStyle pins the picker's chrome mirroring the Command Menu: no
// selection glyph, rows aligned on a two-space indent, the selected row's
// title accented with a dim datetime, and unselected rows fully dim.
func TestPickerSelectedStyle(t *testing.T) {
	prior := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(prior)
	m := sizeModel(t, New(t.TempDir(), settings.Settings{}, nil), 100, 40)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{title: "first work", updated: "Sep 23 18:21"},
		{title: "second work", updated: "Sep 23 09:20"},
	}})
	m = current.(Model)
	view := m.View()
	plain := stripANSI(view)
	if strings.Contains(plain, "▸") {
		t.Fatal("picker must not render a selection glyph")
	}
	accent, dim := 0, 0
	for _, line := range strings.Split(view, "\n") {
		t := strings.TrimRight(stripANSI(line), " ")
		switch {
		case strings.Contains(line, "\x1b[94m") && (strings.Contains(t, "first work") || strings.Contains(t, "second work")):
			accent++
		case strings.Contains(line, "\x1b[3;90m") && (strings.Contains(t, "first work") || strings.Contains(t, "second work")):
			dim++
		}
	}
	if accent != 1 {
		t.Fatalf("exactly one selected row may carry the accent, found %d:\n%s", accent, plain)
	}
	if dim != 1 {
		t.Fatalf("the unselected row must render dim, found %d:\n%s", dim, plain)
	}
	// The selected row's title carries the accent; its datetime does not.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "\x1b[94mfirst work\x1b[0m\x1b[3;90m  Sep 23 18:21") {
			return
		}
	}
	t.Fatalf("selected row must accent only the title, datetime dim:\n%s", plain)
}
