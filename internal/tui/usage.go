package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"unreal-agent-tui/internal/catalog"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// autoCompactionVerified reports whether the harness's auto-compaction is
// confirmed enabled for runs — the gate behind the Usage Line's (auto)
// segment. The harness can represent compaction turns
// (session.TurnCompaction) but nothing schedules them automatically, so
// this is false and (auto) never renders. Flip this only after verifying
// the harness actually auto-compacts mid-session.
const autoCompactionVerified = false

// usageLine renders the Usage Line below the Composer (glossary: Usage
// Line), two rows in the pi-style footer layout: the identity row —
// workspace directory name, git branch, session name truncated to fit —
// and the meter row — session-spanning token totals, latest-turn cache
// hit, catalog cost, context percentage on the left with the
// `- model • thinking level` tail right-aligned. Fresh sessions show the
// zero segments rather than hiding them; segments depending on catalog
// data are omitted for catalog-missing models — never invented. The line
// is a meter, not a status area — the running Turn's spinner and gerund
// verb render in the transcript instead.
func (m Model) usageLine() string {
	return lipgloss.JoinVertical(lipgloss.Left, m.usageIdentity(), m.usageMeter())
}

// usageIdentity renders the identity row: directory name, git branch, and
// the session's name truncated so the row never overflows the width. With
// no session name yet (fresh boot, no turns) only the directory and
// branch show — no dangling bullet.
func (m Model) usageIdentity() string {
	identity := filepath.Base(m.workspace)
	if m.branch != "" {
		identity += " (" + m.branch + ")"
	}
	if m.sessionName == "" {
		return m.truncateToWidth(identity)
	}
	remaining := m.width - lipgloss.Width(identity) - lipgloss.Width(" • ")
	if remaining < 4 { // no room for a title: drop it whole
		return m.truncateToWidth(identity)
	}
	title := truncate.StringWithTail(m.sessionName, uint(remaining), "…")
	return m.truncateToWidth(identity + " • " + title)
}

// usageMeter renders the meter row: token/cost/context segments on the
// left, the `- model • thinking level` tail right-aligned to the terminal
// width.
func (m Model) usageMeter() string {
	var segments []string
	segments = append(segments,
		"↑"+formatTokens(m.sessionIn),
		"↓"+formatTokens(m.sessionOut))
	if m.sessionCached > 0 {
		segments = append(segments, "R"+formatTokens(m.sessionCached))
	}
	if m.sessionCacheWrite > 0 {
		segments = append(segments, "W"+formatTokens(m.sessionCacheWrite))
	}
	if m.turnCached > 0 && m.turnIn > 0 {
		segments = append(segments, fmt.Sprintf("CH%.1f%%", float64(m.turnCached)/float64(m.turnIn)*100))
	}
	entry, catalogKnown := catalog.Lookup(m.cfg.Model)
	if catalogKnown {
		cost := entry.Cost(catalog.Usage{
			Input:      m.sessionIn,
			Cached:     m.sessionCached,
			CacheWrite: m.sessionCacheWrite,
			Output:     m.sessionOut,
		})
		segments = append(segments, formatCost(cost))
		if entry.ContextWindow > 0 {
			pct, level := m.usageContext(entry, m.turnIn)
			segment := fmt.Sprintf("%.1f%%/%s", pct, formatTokens(entry.ContextWindow))
			switch level {
			case "error":
				segment = errorStyle.Render(segment)
			case "warn":
				segment = warnStyle.Render(segment)
			}
			segments = append(segments, segment)
		}
	}
	// The (auto) segment is independent of the catalog: a catalog-missing
	// model must still be able to render it once the gate is real.
	if autoCompactionVerified {
		segments = append(segments, "(auto)")
	}
	left := strings.Join(segments, " ")
	if m.loading {
		left = statusStyle.Render(m.spinner.View()+" loading sessions…") + " " + left
	}
	tail := "- " + orDefault(m.cfg.Model, "(default model)") +
		" • " + orDefault(m.cfg.ThinkingLevel, "high")
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(tail)
	if pad >= 2 { // one space minimum between meter and tail, tail flush right
		return left + strings.Repeat(" ", pad) + tail
	}
	// Narrow terminal: clip the meter so the tail still fits flush right.
	left = truncate.String(left, uint(max(m.width-lipgloss.Width(tail)-1, 1)))
	return left + " " + tail
}

// usageContext returns the prompt tokens' share of the entry's context
// window as (percent, level): level "warn" past 70%, "error" past 90%, ""
// (plain) below. Ascii profiles degrade the colors themselves.
func (m Model) usageContext(entry catalog.Model, promptTokens int64) (float64, string) {
	if entry.ContextWindow <= 0 || promptTokens <= 0 {
		return 0, ""
	}
	pct := float64(promptTokens) / float64(entry.ContextWindow) * 100
	var level string
	switch {
	case pct > 90:
		level = "error"
	case pct > 70:
		level = "warn"
	}
	return pct, level
}

// formatTokens formats a token count compactly: raw below 1k, one-decimal k
// below 10k, rounded k below 1M, then one-decimal M.
func formatTokens(n int64) string {
	switch {
	case n < 1_000:
		return strconv.FormatInt(n, 10)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// formatCost formats a dollar cost with three decimals.
func formatCost(cost float64) string {
	return fmt.Sprintf("$%.3f", cost)
}
