package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"unreal-agent-tui/internal/skills"
)

// typeKeys drives text through the real Update path, one KeyMsg per rune
// like bubbletea delivers them.
func typeKeys(t *testing.T, m Model, text string) Model {
	t.Helper()
	current := tea.Model(m)
	for _, r := range text {
		current, _ = current.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return current.(Model)
}

// TestMenuOpensOnLeadingSlash checks the Command Menu opens above the Prompt
// Box on a leading "/" and lists every static command with its description.
func TestMenuOpensOnLeadingSlash(t *testing.T) {
	m := typeKeys(t, resize(t, 100, 30), "/")
	view := stripANSI(m.View())
	for _, cmd := range commandTable {
		if !strings.Contains(view, cmd.display()) {
			t.Fatalf("menu missing %q:\n%s", cmd.display(), view)
		}
		if !strings.Contains(view, cmd.desc) {
			t.Fatalf("menu missing description %q for %s:\n%s", cmd.desc, cmd.name, view)
		}
	}
	// The menu is anchored above the Composer.
	if menuPos, promptPos := strings.Index(view, "↑/↓ select"), strings.Index(view, "❯"); menuPos == -1 || promptPos == -1 || menuPos > promptPos {
		t.Fatalf("menu must render above the Composer rule (menu=%d prompt=%d):\n%s", menuPos, promptPos, view)
	}
}

// TestMenuFiltersByPrefixCaseInsensitive checks case-insensitive prefix
// filtering, that a non-command closes the menu, and that deleting back to
// "/" reopens it.
func TestMenuFiltersByPrefixCaseInsensitive(t *testing.T) {
	m := typeKeys(t, resize(t, 100, 30), "/SK")
	view := stripANSI(m.View())
	if !strings.Contains(view, "/skills") {
		t.Fatalf("prefix /SK should match /skills:\n%s", view)
	}
	if strings.Contains(view, "/new") {
		t.Fatalf("prefix /SK must filter out /new:\n%s", view)
	}

	// A non-command closes the menu.
	m = typeKeys(t, m, "xyz")
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close on a non-command:\n%s", stripANSI(m.View()))
	}

	// Deleting back to "/" reopens it.
	for i := 0; i < 3; i++ { // remove "xyz"
		current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = current.(Model)
	}
	if !strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must reopen after deleting back to /:\n%s", stripANSI(m.View()))
	}

	// Deleting the "/" itself closes it: remove all remaining characters.
	for m.textarea.Value() != "" {
		current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = current.(Model)
	}
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close when the leading / is gone:\n%s", stripANSI(m.View()))
	}

	// Typing "/" again reopens it.
	m = typeKeys(t, m, "/")
	if !strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must reopen when / is typed again:\n%s", stripANSI(m.View()))
	}
}

// TestMenuUpDownMovesSelectionNotScroll checks the locked routing precedence:
// while the menu is open, ↑/↓ change only the selection — the transcript does
// not scroll and the Composer cursor does not move.
func TestMenuUpDownMovesSelectionNotScroll(t *testing.T) {
	current := tea.Model(resize(t, 100, 30))
	for i := 0; i < 30; i++ {
		current, _ = current.Update(blockMsg{b: block{kind: blockUser, text: strings.Repeat("x", 80)}})
	}
	m := current.(Model)
	m.viewport.SetYOffset(3)
	if m.viewport.AtBottom() {
		t.Fatal("setup: viewport should be scrolled up")
	}

	m = typeKeys(t, m, "/")
	editorBefore := m.textarea.View() // includes the rendered cursor

	// ↓ once: selection moves to the second entry.
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyDown})
	m = current.(Model)
	view := stripANSI(m.View())
	second := commandTable[1].name
	if !strings.Contains(view, "▸ "+second) {
		t.Fatalf("down did not move selection to %s:\n%s", second, view)
	}
	if m.viewport.YOffset != 3 {
		t.Fatalf("menu ↓ scrolled the transcript: YOffset=%d", m.viewport.YOffset)
	}
	if got := m.textarea.View(); got != editorBefore {
		t.Fatalf("menu ↓ moved the Composer input:\nbefore %q\nafter  %q", editorBefore, got)
	}

	// ↑ at the top stays on the first entry.
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyUp})
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = current.(Model)
	if !strings.Contains(stripANSI(m.View()), "▸ "+commandTable[0].name) {
		t.Fatalf("up did not return selection to %s:\n%s", commandTable[0].name, stripANSI(m.View()))
	}
	if m.viewport.YOffset != 3 {
		t.Fatalf("menu ↑ scrolled the transcript: YOffset=%d", m.viewport.YOffset)
	}
}

// TestMenuTabCompletes checks tab completes the highlighted entry into the
// input and the menu closes (the command token is fully chosen). For a
// command with an argument placeholder the inserted text is the command
// only — never the displayed placeholder.
func TestMenuTabCompletes(t *testing.T) {
	m := typeKeys(t, resize(t, 100, 30), "/sk")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyTab})
	m = current.(Model)
	if got := m.textarea.Value(); got != "/skills " {
		t.Fatalf("tab completion produced %q, want \"/skills \"", got)
	}
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close after completing:\n%s", stripANSI(m.View()))
	}

	// Argument placeholder: insert "/model ", not "/model [id] ".
	m = typeKeys(t, resize(t, 100, 30), "/mod")
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyTab})
	m = current.(Model)
	if got := m.textarea.Value(); got != "/model " {
		t.Fatalf("tab on /model produced %q, want \"/model \"", got)
	}
}

// TestMenuEscDismissesThenEnterSends checks esc dismisses the menu (before
// any quit behavior) without altering the input, and that a subsequent enter
// goes through the normal send path.
func TestMenuEscDismissesThenEnterSends(t *testing.T) {
	m := typeKeys(t, resize(t, 100, 30), "/sk")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = current.(Model)
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("esc did not dismiss the menu:\n%s", stripANSI(m.View()))
	}
	if got := m.textarea.Value(); got != "/sk" {
		t.Fatalf("esc changed the input: %q", got)
	}

	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if !strings.Contains(stripANSI(m.View()), "unknown command /sk") {
		t.Fatalf("enter after esc must send normally:\n%s", stripANSI(m.View()))
	}
}

// TestMenuEnterAcceptsWithoutSending pins the deliberate divergence: enter
// accepts the highlighted entry into the input without sending; a second
// enter (menu closed) sends.
func TestMenuEnterAcceptsWithoutSendingThenSends(t *testing.T) {
	m := typeKeys(t, resize(t, 100, 30), "/hel")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if got := m.textarea.Value(); got != "/help " {
		t.Fatalf("enter accepted %q, want \"/help \"", got)
	}
	if m.running {
		t.Fatal("enter with the menu open must not send")
	}
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close after accepting:\n%s", stripANSI(m.View()))
	}

	// Second enter sends the accepted command.
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if m.running {
		t.Fatal("/help must not start a turn")
	}
	if !strings.Contains(stripANSI(m.View()), "commands:") {
		t.Fatalf("second enter did not run the accepted command:\n%s", stripANSI(m.View()))
	}

	// Accepting an arg-taking command inserts the command, not the placeholder.
	m = typeKeys(t, resize(t, 100, 30), "/mo")
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	if got := m.textarea.Value(); got != "/model " {
		t.Fatalf("enter on /model accepted %q, want \"/model \"", got)
	}
}

// TestHelpComesFromCommandTable pins the prefactor: /help output is rendered
// from the same command table the menu lists — the two can never drift.
func TestHelpComesFromCommandTable(t *testing.T) {
	m := resize(t, 100, 30)
	m.textarea.SetValue("/help ") // trailing space: menu closed, enter sends
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	view := stripANSI(m.View())
	for _, cmd := range commandTable {
		if !strings.Contains(view, cmd.name) || !strings.Contains(view, cmd.desc) {
			t.Fatalf("/help missing %s (%q):\n%s", cmd.name, cmd.desc, view)
		}
	}
}

// TestMenuRespectsWidthAndCappedHeight checks the popup stays within the
// terminal width and its height is capped at 8 rows + footer.
func TestMenuRespectsWidthAndCappedHeight(t *testing.T) {
	m := typeKeys(t, resize(t, 40, 20), "/s")
	view := m.View()
	var menuLines []string
	inMenu := false
	for _, line := range strings.Split(view, "\n") {
		plain := stripANSI(line)
		switch {
		case strings.HasPrefix(plain, "▸ /"):
			inMenu = true
			menuLines = append(menuLines, line)
		case inMenu && strings.Contains(plain, "↑/↓ select"): // footer
			inMenu = false
			menuLines = append(menuLines, line)
		case inMenu:
			menuLines = append(menuLines, line)
		}
	}
	if len(menuLines) == 0 {
		t.Fatalf("menu not found in view:\n%s", stripANSI(view))
	}
	if len(menuLines) > 9 { // 8 rows + footer
		t.Fatalf("menu height %d exceeds the 8-row cap + footer", len(menuLines))
	}
	for _, line := range menuLines {
		// JoinVertical pads to the widest line; measure actual content.
		if w := lipgloss.Width(strings.TrimRight(stripANSI(line), " ")); w > 40 {
			t.Fatalf("menu line %d wide exceeds terminal width 40: %q", w, stripANSI(line))
		}
	}
}

// pressKey drives a single non-rune key through the real Update path.
func pressKey(t *testing.T, m Model, key tea.KeyType) Model {
	t.Helper()
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: key})
	return current.(Model)
}

// seedSkills runs real skill discovery over deterministic temp dirs (no
// pollution from the developer's real ~/.agents/skills) and assigns the
// result, mirroring what New does with its own scan.
func seedSkills(t *testing.T, m Model, workspace string, extras ...string) Model {
	t.Helper()
	entries := skills.Discover(append(skills.DefaultDirs(workspace, ""), extras...))
	m.skills = entries
	m.refresh()
	return m
}

// writeSkill writes a SKILL.md with the given frontmatter fields and body
// into dir.
func writeSkill(t *testing.T, dir, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\n" + frontmatter + "---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillMenuListsUserInvokedOnly checks the /skill: prefix lists
// user-invoked skills with their frontmatter descriptions — and that model
// tools never surface as /skill: entries (issue #5).
func TestSkillMenuListsUserInvokedOnly(t *testing.T) {
	dir := t.TempDir()
	// Model-invocable skill in the workspace (no disable-model-invocation).
	writeSkill(t, filepath.Join(dir, ".harness", "skills", "lint-check"),
		"name: lint-check\ndescription: verify deploys\n", "Run deploy checks.")
	// User-invoked skill in an extra directory.
	extra := t.TempDir()
	writeSkill(t, filepath.Join(extra, "journal"),
		"name: journal\ndescription: keep a journal\ndisable-model-invocation: true\n", "Write a journal entry.")

	m := seedSkills(t, resize(t, 100, 30), dir, extra)
	m = typeKeys(t, m, "/skill:")
	view := stripANSI(m.View())

	if !strings.Contains(view, "/skill:journal") {
		t.Fatalf("menu missing user-invoked /skill:journal:\n%s", view)
	}
	if !strings.Contains(view, "keep a journal") {
		t.Fatalf("menu missing journal description:\n%s", view)
	}
	if strings.Contains(view, "/skill:lint-check") {
		t.Fatalf("model tool surfaced as /skill: entry:\n%s", view)
	}
}

// TestSkillMenuFilterCaseInsensitive checks prefix filtering past the
// /skill: prefix, case-insensitively (issue #5).
func TestSkillMenuFilterCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, filepath.Join(extra, "journal"),
		"name: journal\ndescription: keep a journal\ndisable-model-invocation: true\n", "Body.")
	writeSkill(t, filepath.Join(extra, "spin-check"),
		"name: spin-check\ndescription: spin check\ndisable-model-invocation: true\n", "Body.")

	m := seedSkills(t, resize(t, 100, 30), dir, extra)
	m = typeKeys(t, m, "/SKILL:JOU")
	view := stripANSI(m.View())
	if !strings.Contains(view, "/skill:journal") || strings.Contains(view, "/skill:spin-check") {
		t.Fatalf("case-insensitive /skill: filter failed:\n%s", view)
	}
}

// TestSkillMenuAcceptFillsToken checks enter accepts a skill entry into the
// input without sending, filling the /skill:name token with a trailing space
// so arguments can follow (issue #5).
func TestSkillMenuAcceptFillsToken(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, filepath.Join(extra, "journal"),
		"name: journal\ndescription: keep a journal\ndisable-model-invocation: true\n", "Body.")

	m := seedSkills(t, resize(t, 100, 30), dir, extra)
	m = typeKeys(t, m, "/skill:jou")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)

	// Rendered Composer carries the accepted token; textarea.Value() pins
	// the exact trailing space + cursor-at-end that makes room for arguments.
	if !strings.Contains(stripANSI(m.View()), "/skill:journal") {
		t.Fatalf("Composer missing accepted /skill:journal:\n%s", stripANSI(m.View()))
	}
	if got := m.textarea.Value(); got != "/skill:journal " {
		t.Fatalf("accept produced %q, want \"/skill:journal \"", got)
	}
	if m.running {
		t.Fatal("enter with the menu open must not send")
	}
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close after accepting:\n%s", stripANSI(m.View()))
	}
}

// TestSkillMenuUpDownTabRouting checks the locked key routing over skill
// entries: ↓ moves the selection, tab completes the highlighted skill.
func TestSkillMenuUpDownTabRouting(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, filepath.Join(extra, "journal"),
		"name: journal\ndescription: keep a journal\ndisable-model-invocation: true\n", "Body.")
	writeSkill(t, filepath.Join(extra, "spin-check"),
		"name: spin-check\ndescription: spin check\ndisable-model-invocation: true\n", "Body.")

	m := seedSkills(t, resize(t, 100, 30), dir, extra)
	m = typeKeys(t, m, "/skill:")
	m = pressKey(t, m, tea.KeyDown) // select spin-check
	view := stripANSI(m.View())
	if !strings.Contains(view, "▸ /skill:spin-check") {
		t.Fatalf("↓ did not move selection to spin-check:\n%s", view)
	}
	m = pressKey(t, m, tea.KeyTab)
	if got := m.textarea.Value(); got != "/skill:spin-check " {
		t.Fatalf("tab produced %q, want \"/skill:spin-check \"", got)
	}
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must close after completing:\n%s", stripANSI(m.View()))
	}
}

// TestSkillMenuClosedWithoutSkills checks that with no discovered skills,
// /skill: opens nothing and input behaves as before (issue #5).
func TestSkillMenuClosedWithoutSkills(t *testing.T) {
	m := resize(t, 100, 30)
	m.skills = nil
	m.refresh()
	m = typeKeys(t, m, "/skill:")
	if strings.Contains(stripANSI(m.View()), "↑/↓ select") {
		t.Fatalf("menu must stay closed with no skills:\n%s", stripANSI(m.View()))
	}
	// Input still works as before.
	m = typeKeys(t, m, "hello")
	if !strings.Contains(stripANSI(m.View()), "hello") {
		t.Fatalf("input broke with no skills:\n%s", stripANSI(m.View()))
	}
}
