package tui

import (
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
	want := "↑1.1M ↓30k R650k W50k CH50.0% $0.095 60.0%/1.0M - glm-5.3-flash • high"
	if !strings.Contains(view, want) {
		t.Fatalf("usage line missing %q:\n%s", want, view)
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
