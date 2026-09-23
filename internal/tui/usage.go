package tui

import (
	"fmt"
	"strconv"
	"strings"

	"unreal-agent-tui/internal/catalog"
)

// autoCompactionVerified reports whether the harness's auto-compaction is
// confirmed enabled for runs — the gate behind the Usage Line's (auto)
// segment. The harness can represent compaction turns
// (session.TurnCompaction) but nothing schedules them automatically, so
// this is false and (auto) never renders. Flip this only after verifying
// the harness actually auto-compacts mid-session.
const autoCompactionVerified = false

// usageLine renders the Usage Line below the Composer (glossary: Usage
// Line): session-spanning token totals, latest-turn cache hit, catalog
// cost, context percentage, and the `- model • thinking level` tail. While
// a Turn runs, the line is prefixed with the spinner and its randomized
// gerund verb. Segments depending on catalog data are omitted for
// catalog-missing models — never invented.
func (m Model) usageLine() string {
	var segments []string
	if m.sessionIn > 0 || m.sessionOut > 0 {
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
	}
	if entry, ok := catalog.Lookup(m.cfg.Model); ok {
		if m.sessionIn > 0 || m.sessionOut > 0 {
			cost := entry.Cost(catalog.Usage{
				Input:      m.sessionIn,
				Cached:     m.sessionCached,
				CacheWrite: m.sessionCacheWrite,
				Output:     m.sessionOut,
			})
			segments = append(segments, formatCost(cost))
		}
		if m.turnIn > 0 && entry.ContextWindow > 0 {
			pct, level := m.usageContext(m.turnIn)
			segment := fmt.Sprintf("%.1f%%/%s", pct, formatTokens(entry.ContextWindow))
			switch level {
			case "error":
				segment = errorStyle.Render(segment)
			case "warn":
				segment = warnStyle.Render(segment)
			}
			segments = append(segments, segment)
		}
		if autoCompactionVerified {
			segments = append(segments, "(auto)")
		}
	}
	line := strings.Join(segments, " ")
	if line != "" {
		line += " "
	}
	line += "- " + orDefault(m.cfg.Model, "(default model)") +
		" • " + orDefault(m.cfg.ThinkingLevel, "high")
	if m.running {
		line = statusStyle.Render(m.spinner.View()) + " " +
			dimStyle.Render(orDefault(m.turnVerb, "working…")) + " " + line
	} else if m.loading {
		line = statusStyle.Render(m.spinner.View()+" loading sessions…") + " " + line
	}
	return m.truncateToWidth(line)
}

// usageContext returns the latest turn's share of the catalog context
// window as (percent, level): level "warn" past 70%, "error" past 90%, ""
// (plain) below. Ascii profiles degrade the colors themselves.
func (m Model) usageContext(promptTokens int64) (float64, string) {
	entry, ok := catalog.Lookup(m.cfg.Model)
	if !ok || entry.ContextWindow <= 0 || promptTokens <= 0 {
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
