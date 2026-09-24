package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"unreal-agent-tui/internal/catalog"
	"unreal-agent-tui/internal/settings"
)

// TestFormatTokens pins the compact token formatting: raw below 1k,
// one-decimal k below 10k, rounded k below 1M, then one-decimal M.
func TestFormatTokens(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		999:        "999",
		1_000:      "1.0k",
		9_999:      "10.0k",
		10_000:     "10k",
		123_456:    "123k",
		999_999:    "999k",
		1_000_000:  "1.0M",
		12_600_000: "12.6M",
	}
	for input, want := range cases {
		if got := formatTokens(input); got != want {
			t.Fatalf("formatTokens(%d) = %q, want %q", input, got, want)
		}
	}
}

// TestFormatCost pins the dollar formatting at three decimals.
func TestFormatCost(t *testing.T) {
	if got := formatCost(0.042); got != "$0.042" {
		t.Fatalf("formatCost = %q, want $0.042", got)
	}
	if got := formatCost(12.5); got != "$12.500" {
		t.Fatalf("formatCost = %q, want $12.500", got)
	}
}

// driveTurns builds a model with glm-5.3-flash and feeds it two turns of
// usage through the real Update path, mirroring what the agent's JSONL
// produces.
func driveTurns(t *testing.T) Model {
	t.Helper()
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(usageMsg{in: 500_000, out: 10_000, cached: 350_000, cacheWrite: 50_000})
	current, _ = current.Update(usageMsg{in: 600_000, out: 20_000, cached: 300_000})
	return current.(Model)
}

// TestUsageLineBelowComposerRendersSessionUsage checks the Usage Line
// renders below the Composer with session-spanning totals, latest-turn CH,
// catalog cost, and the context percentage — the full specified format.
func TestUsageLineBelowComposerRendersSessionUsage(t *testing.T) {
	m := driveTurns(t)
	view := stripANSI(m.View())
	for _, want := range []string{"↑1.1M ↓30k R650k W50k CH50.0% $0.095 60.0%/1.0M", "- glm-5.3-flash • high"} {
		if !strings.Contains(view, want) {
			t.Fatalf("usage line missing %q:\n%s", want, view)
		}
	}
	// The model tail sits flush right on the meter row.
	lines := strings.Split(view, "\n")
	meter := lines[len(lines)-1]
	if !strings.HasSuffix(strings.TrimRight(meter, " "), "- glm-5.3-flash • high") {
		t.Fatalf("meter tail must be right-aligned:\n%q", meter)
	}
	// Below the Composer: the ❯ prompt line must appear before the usage
	// segments in the view.
	if strings.Index(view, "❯") > strings.Index(view, "↑1.1M") {
		t.Fatalf("usage line must render below the Composer:\n%s", view)
	}
}

// TestUsageLineTotalsSpanSessionAndCHIsLatestTurnOnly pins that ↑ ↓ R W are
// session totals while CH reflects only the latest turn — the second turn
// here has a lower cache hit than the first (70.0% → 50.0%).
func TestUsageLineTotalsSpanSessionAndCHIsLatestTurn(t *testing.T) {
	m := driveTurns(t)
	view := stripANSI(m.View())
	for _, want := range []string{"CH50.0%", "↑1.1M ↓30k R650k W50k"} {
		if !strings.Contains(view, want) {
			t.Fatalf("usage line missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "CH70.0%") {
		t.Fatalf("CH must reflect only the latest turn, not a session blend:\n%s", view)
	}
}

// TestUsageLineHidesCHWithoutCache checks CH and the R/W buckets hide when
// no cache was reported.
func TestUsageLineHidesCHWithoutCache(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(usageMsg{in: 50_000, out: 1_000})
	m = current.(Model)
	line := stripANSI(m.usageLine())
	for _, banned := range []string{"CH", "R350", "W50k"} {
		if strings.Contains(line, banned) {
			t.Fatalf("no cache reported but %q rendered in the Usage Line:\n%s", banned, line)
		}
	}
	if !strings.Contains(line, "↑50k ↓1.0k") {
		t.Fatalf("usage totals missing:\n%s", line)
	}
}

// TestUsageLineCatalogMissingOmitsCatalogSegments pins that a
// catalog-missing model omits the cost and context segments instead of
// inventing them — the harness-reported totals still render.
func TestUsageLineCatalogMissingOmitsCatalogSegments(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "unknown-model"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(usageMsg{in: 50_000, out: 1_000})
	m = current.(Model)
	view := stripANSI(m.View())
	if strings.Contains(view, "$") || strings.Contains(view, "%/") {
		t.Fatalf("catalog-missing model must omit cost/context segments:\n%s", view)
	}
	if !strings.Contains(view, "↑50k ↓1.0k") || !strings.Contains(view, "- unknown-model • high") {
		t.Fatalf("usage totals or model tail missing:\n%s", view)
	}
}

// TestUsageLineContextColorized pins the color level of the context
// percentage: plain below 70 percent of the window, warning past 70, error
// past 90 (Ascii profiles degrade the colors themselves).
func TestUsageLineContextColorized(t *testing.T) {
	cases := map[int64]string{
		650_000: "",      // 65 percent: plain
		750_000: "warn",  // 75 percent: warning
		950_000: "error", // 95 percent: error
	}
	for prompt, want := range cases {
		m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
		entry, _ := catalog.Lookup("glm-5.3-flash")
		_, level := m.usageContext(entry, prompt)
		if level != want {
			t.Fatalf("context level at %d prompt tokens = %q, want %q", prompt, level, want)
		}
	}
}

// TestUsageLineNoAutoUntilVerified pins that (auto) is absent — the
// harness's auto-compaction is not verified enabled, so the segment must
// not render (issue #13).
func TestUsageLineNoAutoUntilVerified(t *testing.T) {
	m := driveTurns(t)
	if strings.Contains(stripANSI(m.View()), "(auto)") {
		t.Fatalf("(auto) must be absent while auto-compaction is unverified")
	}
	if autoCompactionVerified {
		t.Fatalf("autoCompactionVerified must stay false until the harness's auto-compaction is verified")
	}
}

// TestUsageLineHasNoSpinnerWhileRunning pins that the Usage Line stays a
// meter: a running Turn must NOT prefix it with the spinner and verb —
// that row lives in the transcript, where the agent response will appear.
func TestUsageLineHasNoSpinnerWhileRunning(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	m.textarea.SetValue("do the thing")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	current, _ = current.Update(usageMsg{in: 500_000, out: 10_000, cached: 350_000, cacheWrite: 50_000})
	m = current.(Model)
	if !m.running {
		t.Fatal("turn must still be running")
	}
	lines := strings.Split(stripANSI(m.View()), "\n")
	usage := lines[len(lines)-1]
	for _, candidate := range spinnerVerbs {
		if strings.Contains(usage, candidate) {
			t.Fatalf("usage line must not carry the spinner verb:\n%s", usage)
		}
	}
	if !strings.Contains(usage, "↑500k") {
		t.Fatalf("usage segments missing:\n%s", usage)
	}
}

// TestTurnStatusRowInTranscript pins the running Turn's status row in the
// chat section where the agent response will appear — the Claude Code
// thinking row: spinner frame + gerund verb, with the elapsed time.
func TestTurnStatusRowInTranscript(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	m.textarea.SetValue("do the thing")
	current, _ := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	var row string
	for _, line := range strings.Split(stripANSI(m.View()), "\\n") {
		for _, candidate := range spinnerVerbs {
			if strings.Contains(line, candidate) {
				row = line
			}
		}
	}
	if row == "" {
		t.Fatalf("no turn status row in the transcript:\n%s", stripANSI(m.View()))
	}
	if !regexp.MustCompile(`\(\d+s\)`).MatchString(row) {
		t.Fatalf("status row must carry the elapsed seconds:\n%s", row)
	}
}

// TestTurnStatusRowElapsed pins the elapsed counter: the status row counts
// the Turn's seconds.
func TestTurnStatusRowElapsed(t *testing.T) {
	m := resize(t, 100, 30)
	m.running = true
	m.turnVerb = "Compiling courage…"
	m.turnStarted = time.Now().Add(-65 * time.Second)
	m.refresh()
	if plain := stripANSI(m.View()); !strings.Contains(plain, "Compiling courage… (65s)") {
		t.Fatalf("elapsed seconds missing:\n%s", plain)
	}
}

// TestSpinnerMatchesClaude pins the Claude Code thinking spinner: the
// reverse-mirror cycle of · ✢ ✳ ✶ ✻ ✽ at 120ms per frame.
func TestSpinnerMatchesClaude(t *testing.T) {
	want := []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"}
	if !slices.Equal(claudeSpinner.Frames, want) {
		t.Fatalf("spinner frames = %q, want the Claude Code cycle %q", claudeSpinner.Frames, want)
	}
	if claudeSpinner.FPS != 120*time.Millisecond {
		t.Fatalf("spinner FPS = %s, want 120ms per frame", claudeSpinner.FPS)
	}
}

// TestUsageLineClipsToWidth pins that the Usage Line never overflows the
// terminal width.
func TestUsageLineClipsToWidth(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 40, 30)
	current, _ := tea.Model(m).Update(usageMsg{in: 500_000, out: 10_000, cached: 350_000, cacheWrite: 50_000})
	m = current.(Model)
	for _, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(strings.TrimRight(stripANSI(line), " ")); w > 40 {
			t.Fatalf("usage line %d wide exceeds width 40: %q", w, stripANSI(line))
		}
	}
}

// TestUsageLineFreshSessionShowsZeroSegments pins the fresh-boot meter:
// zero segments render rather than hiding — `↑0 ↓0 $0.000 0%/1.0M` for a
// catalog-known model, model tail right-aligned.
func TestUsageLineFreshSessionShowsZeroSegments(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	lines := strings.Split(stripANSI(m.View()), "\n")
	meter := lines[len(lines)-1]
	for _, want := range []string{"↑0 ↓0", "$0.000", "0.0%/1.0M"} {
		if !strings.Contains(meter, want) {
			t.Fatalf("fresh meter missing %q:\n%q", want, meter)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(meter, " "), "- glm-5.3-flash • high") {
		t.Fatalf("fresh meter tail must be right-aligned:\n%q", meter)
	}
}

// TestUsageLineFreshSessionCatalogMissing pins that a catalog-missing model
// still gets the token segments but no invented cost/context.
func TestUsageLineFreshSessionCatalogMissing(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "mystery-model"}, nil), 100, 30)
	lines := strings.Split(stripANSI(m.View()), "\n")
	meter := lines[len(lines)-1]
	for _, want := range []string{"↑0 ↓0", "- mystery-model • high"} {
		if !strings.Contains(meter, want) {
			t.Fatalf("fresh meter missing %q:\n%q", want, meter)
		}
	}
	if strings.Contains(meter, "$") || strings.Contains(meter, "%/1.0M") {
		t.Fatalf("catalog-missing model must not invent cost or context:\n%q", meter)
	}
}

// TestNewCommandResetsUsageLine pins that /new zeroes the meter — values
// from the previous session must not carry into the fresh one.
func TestNewCommandResetsUsageLine(t *testing.T) {
	m := sizeModel(t, New(t.TempDir(), settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(usageMsg{in: 2_700_000, out: 72_000, cached: 1_500_000})
	m = current.(Model)
	m = typeKeys(t, m, "/new")
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	lines := strings.Split(stripANSI(m.View()), "\n")
	meter := lines[len(lines)-1]
	if strings.Contains(meter, "2.7k") || strings.Contains(meter, "72k") {
		t.Fatalf("/new must reset the meter:\n%q", meter)
	}
	if !strings.Contains(meter, "↑0 ↓0 $0.000 0.0%/1.0M") {
		t.Fatalf("/new meter must show the fresh zeros:\n%q", meter)
	}
}

// TestResumeRestoresUsageLine pins that resuming a session restores its
// Usage Line: session totals summed over the persisted model_response
// records, turn-level CH/context from the latest one.
func TestResumeRestoresUsageLine(t *testing.T) {
	workspace := t.TempDir()
	id := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(workspace, ".harness", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := "{\"type\":\"session\",\"data\":{\"Version\":2,\"Session\":{\"ID\":\"" + id +
		"\",\"CreatedAt\":\"2026-09-24T09:10:57.144039Z\"}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":1,\"RecordedAt\":\"2026-09-24T09:11:00Z\"," +
		"\"Kind\":\"input\",\"Data\":{\"ID\":\"c079e8cf-d55d-4ac3-b3f1-cacb2ffb3030\",\"Kind\":\"external\"," +
		"\"Payload\":\"work on the meter\"}}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":2,\"RecordedAt\":\"2026-09-24T09:11:30Z\"," +
		"\"Kind\":\"turn\",\"Data\":{\"ID\":\"t1\",\"PreviousTurnID\":\"\",\"Type\":\"regular\"}}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":3,\"RecordedAt\":\"2026-09-24T09:12:00Z\"," +
		"\"Kind\":\"model_response\",\"Data\":{\"TurnID\":\"t1\",\"Response\":{\"ID\":\"r1\",\"Stop\":\"end_turn\"," +
		"\"Output\":[],\"Usage\":{\"InputTokens\":2652,\"CachedInputTokens\":0,\"CacheWriteInputTokens\":0,\"OutputTokens\":100,\"ReasoningTokens\":0}}}}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":4,\"RecordedAt\":\"2026-09-24T09:12:30Z\"," +
		"\"Kind\":\"turn\",\"Data\":{\"ID\":\"t2\",\"PreviousTurnID\":\"t1\",\"Type\":\"regular\"}}}}\n" +
		"{\"type\":\"item\",\"data\":{\"Item\":{\"Sequence\":5,\"RecordedAt\":\"2026-09-24T09:13:00Z\"," +
		"\"Kind\":\"model_response\",\"Data\":{\"TurnID\":\"t2\",\"Response\":{\"ID\":\"r2\",\"Stop\":\"end_turn\"," +
		"\"Output\":[],\"Usage\":{\"InputTokens\":1000,\"CachedInputTokens\":200,\"CacheWriteInputTokens\":50,\"OutputTokens\":400,\"ReasoningTokens\":0}}}}}}\n"
	if err := os.WriteFile(filepath.Join(dir, id+".session.jsonl"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}

	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	current, _ := tea.Model(m).Update(sessionsMsg{entries: []sessionEntry{
		{id: id, title: "work on the meter", updated: "Sep 24 09:20"},
	}})
	m = current.(Model)
	current, _ = tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = current.(Model)
	lines := strings.Split(stripANSI(m.View()), "\n")
	meter := lines[len(lines)-1]
	// Sums: in 3652 → 3.7k, out 500; latest turn: cached 200 → CH20.0%.
	for _, want := range []string{"↑3.7k ↓500 R200 W50 CH20.0%", "0.1%/1.0M"} {
		if !strings.Contains(meter, want) {
			t.Fatalf("resumed meter missing %q:\n%q", want, meter)
		}
	}
}

// TestUsageIdentityRow pins the identity row above the meter: the
// workspace directory name, the git branch when there is one, and the
// session name (truncated with an ellipsis when it would overflow).
func TestUsageIdentityRow(t *testing.T) {
	workspace := t.TempDir()
	m := sizeModel(t, New(workspace, settings.Settings{Model: "glm-5.3-flash"}, nil), 100, 30)
	lines := strings.Split(stripANSI(m.View()), "\n")
	identity := lines[len(lines)-2]
	// No git repository: directory name alone, no dangling bullet.
	if want := filepath.Base(workspace); !strings.HasPrefix(identity, want) || strings.Contains(identity, "•") {
		t.Fatalf("identity row without a branch must be the bare directory name:\n%q", identity)
	}
	// After the first turn the session name appears; an overlong name
	// truncates with an ellipsis.
	current, _ := tea.Model(m).Update(usageMsg{in: 100, out: 10})
	m = current.(Model)
	m.textarea.SetValue("short prompt")
	m.resizeEditor()
	current, cmd := tea.Model(m).Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd
	m = current.(Model)
	identity = strings.Split(stripANSI(m.View()), "\n")[len(strings.Split(stripANSI(m.View()), "\n"))-2]
	if !strings.Contains(identity, " • short prompt") {
		t.Fatalf("identity row must carry the session name:\n%q", identity)
	}
	m.sessionName = strings.Repeat("x", 200)
	m.refresh()
	identity = strings.Split(stripANSI(m.View()), "\n")[len(strings.Split(stripANSI(m.View()), "\n"))-2]
	if !strings.Contains(identity, "…") || lipgloss.Width(identity) > 100 {
		t.Fatalf("overlong session name must truncate inside the width:\n%q", identity)
	}
}
