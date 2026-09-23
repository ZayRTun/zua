package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbletea"

	"unreal-agent-tui/internal/runner"
	"unreal-agent-tui/internal/skills"
	"unreal-agent-tui/internal/testsrv"
)

// TestReplayAndListSessions runs one real agent session against the fake LLM
// server, then verifies the TUI can list and replay it.
func TestReplayAndListSessions(t *testing.T) {
	server := testsrv.New(t)
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", server.URL)

	workspace := t.TempDir()
	code := runner.Run(t.Context(), []string{"-workspace", workspace, `-p`, `create hello.txt with a greeting`}, os.Getenv, os.Stderr, os.Stderr)
	if code != 0 {
		t.Fatalf("runner exit code %d", code)
	}

	message := listSessions(workspace)()
	sessions, ok := message.(sessionsMsg)
	if !ok {
		t.Fatalf("listSessions returned %T", message)
	}
	if sessions.err != nil {
		t.Fatalf("listSessions error: %v", sessions.err)
	}
	if len(sessions.entries) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions.entries))
	}
	entry := sessions.entries[0]
	if !strings.Contains(entry.title, "create hello.txt") {
		t.Fatalf("session title %q, want the user prompt", entry.title)
	}

	blocks, id, err := replaySession(workspace, entry)
	if err != nil {
		t.Fatalf("replaySession: %v", err)
	}
	if id != entry.id {
		t.Fatalf("replayed id %q, want %q", id, entry.id)
	}
	var sawUser, sawAssistant, sawTool bool
	var toolIndex, assistantIndex int
	for index, b := range blocks {
		switch b.kind {
		case blockUser:
			sawUser = strings.Contains(b.text, "create hello.txt")
		case blockAssistant:
			sawAssistant = strings.Contains(b.text, "done")
			assistantIndex = index
		case blockTool:
			if b.tool != nil && b.tool.name == "Write" && b.tool.status == "ok" {
				sawTool = true
				toolIndex = index
				// A replayed card must render exactly like a live one.
				got := stripANSI(collapsedEntry(b.tool, 200))
				if !strings.Contains(got, "⏺ Write: hello.txt") || !strings.Contains(got, "⎿ ok") {
					t.Fatalf("replayed card renders wrong: %q", got)
				}
			}
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("replay missing user=%v assistant=%v blocks", sawUser, sawAssistant)
	}
	if !sawTool {
		t.Fatalf("replay missing the Write tool card; kinds: %v", blockKinds(blocks))
	}
	if !(toolIndex < assistantIndex) {
		t.Fatalf("replayed card order wrong: tool=%d assistant=%d", toolIndex, assistantIndex)
	}
}

func blockKinds(blocks []block) []string {
	out := make([]string, len(blocks))
	for index, b := range blocks {
		out[index] = fmt.Sprintf("kind=%d", b.kind)
		if b.tool != nil {
			out[index] += " tool=" + b.tool.name + " status=" + b.tool.status
		}
	}
	return out
}

func TestEventParserToolLifecycle(t *testing.T) {
	parser := newEventParser()
	messages := parser.parse("model_response", []byte(`{"Response":{"Output":[
		{"Type":"tool_call","Data":{"CallID":"c1","Name":"Edit","Arguments":"{\"path\":\"main.go\",\"old_text\":\"a\",\"new_text\":\"b\"}"}}
	],"Usage":{"InputTokens":1200,"OutputTokens":34}}}`))
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2 (tool card + usage)", len(messages))
	}
	messages = parser.parse("tool_call_status", []byte(`{"CallID":"c1","Status":{},"Operations":[{"ID":"o1","Type":"file_edit","Status":"completed"}]}`))
	if len(messages) != 1 {
		t.Fatalf("got %d status messages, want 1", len(messages))
	}
	status, ok := messages[0].(toolStatusMsg)
	if !ok || status.status != "ok" || status.name != "Edit" {
		t.Fatalf("unexpected status message %#v", messages[0])
	}

	card := &toolCard{callID: "c1", name: "Edit", argsRaw: `{"path":"main.go","old_text":"a","new_text":"b"}`, status: "ok"}
	model := New(t.TempDir(), "", "", nil)
	model.appendBlock(block{kind: blockTool, tool: card})
	rendered := model.renderToolCard(card, 100)
	if !strings.Contains(rendered, "main.go") || !strings.Contains(rendered, "- a") || !strings.Contains(rendered, "+ b") {
		t.Fatalf("diff card rendering incomplete:\n%s", rendered)
	}
}

// TestUpdateRearmsListener is the regression test for the "stuck on working"
// bug: the event listener must be re-armed on every message path until
// turnDoneMsg is processed.
func TestUpdateRearmsListener(t *testing.T) {
	m := New(t.TempDir(), "", "", nil)
	m.running = true
	m.events = make(chan tea.Msg, 16)

	// Simulate the greeting turn: meta, assistant block, then completion.
	m.events <- metaMsg{sessionID: "test-session", model: "test-model"}
	m.events <- blockMsg{b: block{kind: blockAssistant, text: "Hi!"}}
	m.events <- usageMsg{in: 663, out: 94}
	m.events <- turnDoneMsg{}
	close(m.events)

	current := tea.Model(m)
	deadline := 20
	for i := 0; i < deadline; i++ {
		// An arbitrary message (a resize) must not break the listener chain.
		current, cmd := current.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		if cmd == nil {
			t.Fatalf("step %d: listener lost (cmd nil) — stuck-turn bug", i)
		}
		message := cmd()
		if done, ok := message.(turnDoneMsg); ok {
			current, _ = current.Update(done)
			final, ok := current.(Model)
			if !ok {
				t.Fatal("unexpected model type")
			}
			if final.running {
				t.Fatal("running still true after turnDoneMsg")
			}
			return // turn completed cleanly
		}
		// Feed the consumed event back through Update like the runtime would.
		current, _ = current.Update(message)
		if fm, ok := current.(Model); ok && !fm.running {
			return // turn completed
		}
	}
	t.Fatal("turnDoneMsg was never processed: stuck-turn bug")
}

// TestToolCardArgsFlow verifies tool-call arguments survive the parser →
// message → card path (regression for the empty "Bash $" cards).
func TestToolCardArgsFlow(t *testing.T) {
	parser := newEventParser()
	response := `{"Response":{"Output":[
		{"Type":"tool_call","Data":{"CallID":"c1","Name":"Bash","Arguments":"{\"command\":\"cat sample.txt\"}"}}
	],"Usage":{"InputTokens":10,"OutputTokens":5}}}`
	msgs := parser.parse("model_response", json.RawMessage(response))
	if len(msgs) == 0 {
		t.Fatal("no messages parsed")
	}
	var running toolStatusMsg
	for _, m := range msgs {
		if s, ok := m.(toolStatusMsg); ok && s.status == "running" {
			running = s
		}
	}
	if running.argsRaw == "" {
		t.Fatal("running toolStatusMsg lost argsRaw")
	}
	status := `{"CallID":"c1","Status":{"Error":""},"Operations":[{"Status":"completed"}]}`
	msgs = parser.parse("tool_call_status", json.RawMessage(status))
	if len(msgs) != 1 {
		t.Fatal("expected one status message")
	}
	done, ok := msgs[0].(toolStatusMsg)
	if !ok || done.status != "ok" || done.argsRaw == "" {
		t.Fatalf("completed status lost args: %+v", done)
	}
	args := parseArgs(done.argsRaw)
	if argString(args, "command") != "cat sample.txt" {
		t.Fatalf("command arg mismatch: %q", argString(args, "command"))
	}
}

func TestReloadDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, ".harness", "skills", "deploy-check"),
		"name: deploy-check\ndescription: verify deploys\n", "Run deploy checks.")
	entries := skills.Discover(skills.DefaultDirs(dir, ""))
	if len(entries) != 1 || entries[0].Name != "deploy-check" || entries[0].UserOnly {
		t.Fatalf("skill discovery mismatch: %+v", entries)
	}
	if body, err := skills.Body(entries[0].Path); err != nil || body != "Run deploy checks." {
		t.Fatalf("skill body mismatch: %q (%v)", body, err)
	}

	m := New(dir, "", "", nil)
	report := m.reloadReport()
	if !strings.Contains(report, "deploy-check") || !strings.Contains(report, "SkillUse enabled") {
		t.Fatalf("reload report missing skill info: %q", report)
	}
	if !strings.Contains(report, "Bash, ViewImage, Read, Write, Edit") {
		t.Fatalf("reload report missing tool list: %q", report)
	}
	if strings.Contains(report, "pristine") || strings.Contains(report, "file-tools") {
		t.Fatalf("reload report must not show a mode label: %q", report)
	}
	if !strings.Contains(report, "(provider default)") {
		t.Fatalf("reload report missing model: %q", report)
	}
}

func TestCommandMenuAndUserSkills(t *testing.T) {
	dir := t.TempDir()
	// One model-invocable skill in the workspace, one user-only skill in an
	// extra directory.
	writeSkill(t, filepath.Join(dir, ".harness", "skills", "deploy-check"),
		"name: deploy-check\ndescription: verify deploys\n", "Run deploy checks.")
	extra := t.TempDir()
	writeSkill(t, filepath.Join(extra, "journal"),
		"name: journal\ndescription: keep a journal\ndisable-model-invocation: true\n", "Write a journal entry.")

	entries := skills.Discover(append(skills.DefaultDirs(dir, ""), extra))
	if len(entries) != 2 {
		t.Fatalf("expected 2 skills, got %+v", entries)
	}
	byName := map[string]skills.Entry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if byName["deploy-check"].UserOnly || !byName["journal"].UserOnly {
		t.Fatalf("UserOnly flags wrong: %+v", entries)
	}

	m := New(dir, "", "", []string{extra})
	m.textarea.SetValue("/")
	matches := m.menuMatches()
	var names []string
	for _, item := range matches {
		names = append(names, item.display)
	}
	present := map[string]bool{}
	for _, name := range names {
		present[name] = true
	}
	// The default scan includes the user's real ~/.agents/skills, so assert
	// that our user-invoked skill is present rather than exact equality. The
	// model tool deploy-check must NOT surface as a /skill: entry (issue #5).
	for _, required := range []string{"/help", "/new", "/resume", "/model [id]", "/skills", "/reload", "/quit", "/skill:journal"} {
		if !present[required] {
			t.Fatalf("menu missing %q; got %v", required, names)
		}
	}
	if present["/skill:deploy-check"] {
		t.Fatalf("model tool surfaced as /skill: entry; got %v", names)
	}

	// Filter to skills only: journal present, model tool excluded.
	m.textarea.SetValue("/skill:")
	matches = m.menuMatches()
	matchedNames := map[string]bool{}
	for _, item := range matches {
		matchedNames[item.display] = true
	}
	if !matchedNames["/skill:journal"] {
		t.Fatalf("expected journal in filtered matches, got %v", matchedNames)
	}
	if matchedNames["/skill:deploy-check"] {
		t.Fatalf("model tool in /skill: matches, got %v", matchedNames)
	}
}

func slicesEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[string]int{}
	for _, value := range a {
		count[value]++
	}
	for _, value := range b {
		count[value]--
		if count[value] < 0 {
			return false
		}
	}
	return true
}

func TestHunkDiff(t *testing.T) {
	old := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	new := "line1\nline2\nline3\nCHANGED\nline5\nline6\nline7\nline8\nline9\nline10"
	lines := hunkDiff(strings.Split(old, "\n"), strings.Split(new, "\n"), 80)
	joined := strings.Join(lines, "\n")
	// A one-line change must produce a small hunk with context, not 20 lines.
	if len(lines) > 7 {
		t.Fatalf("hunk too large for 1-line change: %d lines\n%s", len(lines), joined)
	}
	if !strings.Contains(joined, "- line4") || !strings.Contains(joined, "+ CHANGED") {
		t.Fatalf("hunk missing change markers:\n%s", joined)
	}
	if strings.Contains(joined, "- line1") {
		t.Fatalf("unchanged head should not appear as a removal:\n%s", joined)
	}

	// Identical content → no diff lines.
	if got := hunkDiff(strings.Split("a\nb", "\n"), strings.Split("a\nb", "\n"), 80); len(got) != 0 {
		t.Fatalf("expected no hunks for identical input, got %v", got)
	}
}

func TestStderrTail(t *testing.T) {
	var buffer stderrTail
	for i := 0; i < 50; i++ {
		buffer.Write([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	tail := buffer.Tail(6)
	if !strings.Contains(tail, "line-49") || strings.Contains(tail, "line-43\n") {
		t.Fatalf("tail wrong: %q", tail)
	}
	if !strings.Contains(tail, "earlier lines hidden") {
		t.Fatalf("missing drop notice: %q", tail)
	}
}

// Regression: skill-invoked turns must re-seed the spinner tick chain, or the
// "working…" status freezes (the chain dies when the previous turn ends).
func TestSkillInvocationReseedsSpinner(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".harness", "skills", "spin-check")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: spin-check\ndescription: spinner regression\ndisable-model-invocation: true\n---\n\nDo the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(dir, "", "", nil)
	cmds := m.invokeSkillCommand("/skill:spin-check with args", "/skill:spin-check")
	if len(cmds) == 0 {
		t.Fatal("invokeSkillCommand returned no commands — spinner would freeze")
	}
	msg := cmds[0]()
	if _, ok := msg.(spinner.TickMsg); !ok {
		t.Fatalf("first cmd is %T, want spinner.TickMsg", msg)
	}
	if !m.running {
		t.Fatal("skill invocation did not start a turn")
	}
}

// TestCtrlOTogglesVerboseTranscript covers the collapsed-entry default, the
// ctrl+O verbose toggle, and that fresh cards follow the current mode
// (primary seam: Update → View).
func TestCtrlOTogglesVerboseTranscript(t *testing.T) {
	current := tea.Model(New(t.TempDir(), "", "", nil))
	current, _ = current.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m := current.(Model)

	// A completed Edit card arrives through the normal message path.
	current, _ = current.Update(toolStatusMsg{
		callID:  "c1",
		name:    "Edit",
		argsRaw: `{"path":"main.go","old_text":"a","new_text":"b"}`,
		status:  "ok",
	})
	m = current.(Model)
	collapsed := stripANSI(m.View())
	if !strings.Contains(collapsed, "⏺ Edit: main.go +1/-1 ⎿ ok") {
		t.Fatalf("collapsed transcript missing one-line entry:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "- a") || strings.Contains(collapsed, "+ b") {
		t.Fatalf("collapsed transcript must not show the diff body:\n%s", collapsed)
	}

	// ctrl+O expands every card in place.
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = current.(Model)
	verbose := stripANSI(m.View())
	if !strings.Contains(verbose, "- a") || !strings.Contains(verbose, "+ b") {
		t.Fatalf("verbose transcript missing diff body:\n%s", verbose)
	}
	if strings.Contains(verbose, "⏺ Edit: main.go +1/-1 ⎿ ok") {
		t.Fatalf("verbose transcript still shows the collapsed entry:\n%s", verbose)
	}

	// Fresh cards arriving while verbose render expanded and keep the mode.
	current, _ = current.Update(toolStatusMsg{
		callID:  "c2",
		name:    "Edit",
		argsRaw: `{"path":"other.go","old_text":"x","new_text":"y"}`,
		status:  "ok",
	})
	verbose = stripANSI(current.(Model).View())
	if !strings.Contains(verbose, "- x") {
		t.Fatalf("fresh card did not follow verbose mode:\n%s", verbose)
	}
	if current.(Model).verbose != true {
		t.Fatal("fresh card flipped the verbose mode")
	}

	// ctrl+O collapses again.
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	collapsed = stripANSI(current.(Model).View())
	if !strings.Contains(collapsed, "⏺ Edit: other.go +1/-1 ⎿ ok") {
		t.Fatalf("collapsed toggle lost the second card:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "- x") {
		t.Fatalf("collapsed toggle still shows diff body:\n%s", collapsed)
	}
}

// TestToggleKeepsScrollPosition verifies ctrl+O does not yank the viewport
// when the user has scrolled away from the tail.
func TestToggleKeepsScrollPosition(t *testing.T) {
	current := tea.Model(New(t.TempDir(), "", "", nil))
	current, _ = current.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	m := current.(Model)
	for i := 0; i < 40; i++ {
		m.appendBlock(block{kind: blockUser, text: fmt.Sprintf("prompt %d", i)})
	}
	m.viewport.GotoBottom()
	m.viewport.SetYOffset(5)
	if m.viewport.AtBottom() {
		t.Fatal("setup: viewport should be scrolled up")
	}
	current, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = current.(Model)
	if m.viewport.YOffset != 5 {
		t.Fatalf("scroll position lost on toggle: YOffset=%d", m.viewport.YOffset)
	}
}

// TestUserAndAssistantNeverCollapse guards the rule that only tool cards
// participate in the collapsed/verbose toggle.
func TestUserAndAssistantNeverCollapse(t *testing.T) {
	current := tea.Model(New(t.TempDir(), "", "", nil))
	current, _ = current.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	current, _ = current.Update(blockMsg{b: block{kind: blockUser, text: "fix the bug"}})
	current, _ = current.Update(blockMsg{b: block{kind: blockAssistant, text: "On it."}})
	m := current.(Model)
	for _, want := range []string{"❯ fix the bug", "On it."} {
		if !strings.Contains(stripANSI(m.View()), want) {
			t.Fatalf("collapsed view dropped %q:\n%s", want, stripANSI(m.View()))
		}
	}
	current, _ = current.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = current.(Model)
	for _, want := range []string{"❯ fix the bug", "On it."} {
		if !strings.Contains(stripANSI(m.View()), want) {
			t.Fatalf("verbose view dropped %q:\n%s", want, stripANSI(m.View()))
		}
	}
}
