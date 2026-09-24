package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ---- transcript rendering ----

func (m *Model) renderTranscript() string {
	width := max(m.viewport.Width-1, 20)
	parts := make([]string, 0, len(m.blocks))
	for index := range m.blocks {
		if line := m.renderBlock(&m.blocks[index], width); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (m *Model) renderBlock(b *block, width int) string {
	// The welcome Header renders live on every pass (its stats line must
	// reflect /reload), so it bypasses the per-block cache.
	if b.kind == blockHeader {
		return m.renderHeader()
	}
	// Per-block cache: between events only one or two blocks change, so most
	// renders are pure cache hits (keeps long sessions from re-wrapping
	// everything on every tool status). The key includes the verbose flag so
	// the ctrl+O toggle re-renders dual-rendered blocks exactly once.
	if b.rendered != "" && b.width == width && b.verbose == m.verbose {
		return b.rendered
	}
	var out string
	switch b.kind {
	case blockMeta:
		out = dimStyle.Render(wrap("· "+b.text, width))
	case blockUser:
		out = userStyle.Render(wrap("❯ "+b.text, width))
	case blockAssistant:
		out = m.renderMarkdown(b, width)
	case blockReasoning:
		out = dimStyle.Render(wrap("· "+b.text, width))
	case blockError:
		out = errorStyle.Render(wrap("✗ "+b.text, width))
	case blockDivider:
		out = dimStyle.Render("── " + b.text + " " + strings.Repeat("─", max(width-len(b.text)-4, 3)))
	case blockTool:
		// Tool cards are dual-rendered: one-line Collapsed Entry by default,
		// full card in the Verbose Transcript (ctrl+O).
		if m.verbose {
			out = m.renderToolCard(b.tool, width)
		} else {
			out = collapsedEntry(b.tool, width)
		}
	}
	b.rendered, b.width, b.verbose = out, width, m.verbose
	return out
}

var markdownCache []*glamour.TermRenderer

// glamourStyleName is resolved ONCE at startup from the background detection
// bubbletea already performed in its package init (lipgloss caches the result
// in a sync.Once, so this issues no new terminal query). Glamour's
// WithAutoStyle would call termenv.HasDarkBackground() on every renderer
// creation — i.e. mid-session, while bubbletea owns stdin in raw mode. Those
// OSC 11 / DSR query replies race with user keystrokes and leak into the
// input as garbage ("[40;1R", "rgb:0000/0000/0000", stray "\"), so they
// must never happen after startup.
var glamourStyleName = func() string {
	if lipgloss.HasDarkBackground() {
		return "dark"
	}
	return "light"
}()

func (m *Model) renderMarkdown(b *block, width int) string {
	// Already cached by renderBlock's generic path; kept as the renderer.
	if b.rendered != "" && b.width == width {
		return b.rendered
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(width-1, 20)),
		glamour.WithStandardStyle(glamourStyleName),
	)
	if err != nil {
		b.rendered, b.width = wrap(b.text, width), width
		return b.rendered
	}
	rendered, err := renderer.Render(b.text)
	if err != nil {
		b.rendered, b.width = wrap(b.text, width), width
		return b.rendered
	}
	b.rendered, b.width = strings.TrimRight(rendered, "\n"), width
	return b.rendered
}

// toolStatusStyle maps a tool-card status to its color: accent for success,
// error red for failure, dim gray otherwise. Shared by verbose Tool Cards and
// Collapsed Entries so the palette can never drift between the two.
func toolStatusStyle(status string) lipgloss.Style {
	switch status {
	case "ok":
		return okStyle
	case "failed", "canceled":
		return errorStyle
	}
	return toolStyle
}

func (m *Model) renderToolCard(card *toolCard, width int) string {
	if card == nil {
		return ""
	}
	symbol, style := "⚙", toolStatusStyle(card.status)
	switch card.status {
	case "ok":
		symbol = "✓"
	case "failed", "canceled":
		symbol = "✗"
	}

	var lines []string
	args := parseArgs(card.argsRaw)
	switch card.name {
	case "Bash":
		lines = append(lines, style.Render(symbol+" Bash")+" "+toolStyle.Render("$ "+collapse(argString(args, "command"), width-12)))
	case "Read":
		detail := argString(args, "path")
		if offset := argString(args, "offset"); offset != "" {
			detail += fmt.Sprintf(" (from line %s", offset)
			if limit := argString(args, "limit"); limit != "" {
				detail += ", " + limit + " lines"
			}
			detail += ")"
		}
		lines = append(lines, style.Render(symbol+" Read")+" "+toolStyle.Render(detail))
	case "Write":
		lines = append(lines, style.Render(symbol+" Write")+" "+toolStyle.Render(argString(args, "path")))
		if card.status == "ok" || card.status == "running" {
			lines = append(lines, previewLines(argString(args, "content"), width)...)
		}
	case "Edit":
		lines = append(lines, style.Render(symbol+" Edit")+" "+toolStyle.Render(argString(args, "path")))
		if card.status == "ok" || card.status == "running" {
			lines = append(lines, renderDiff(argString(args, "old_text"), argString(args, "new_text"), width)...)
		}
	default:
		lines = append(lines, style.Render(symbol+" "+orDefault(card.name, "tool")))
	}
	if card.errText != "" {
		lines = append(lines, errorStyle.Render(wrap("  "+collapse(card.errText, width-4), width)))
	}
	if card.outText != "" && (card.status == "ok" || card.status == "failed") {
		lines = append(lines, outputLines(card.outText, width)...)
		if card.exitCode != 0 {
			lines = append(lines, errorStyle.Render(fmt.Sprintf("  exit %d", card.exitCode)))
		}
	}
	return strings.Join(lines, "\n")
}

// outputLines renders the tail of a tool's captured output.
func outputLines(output string, width int) []string {
	all := strings.Split(strings.TrimRight(output, "\n"), "\n")
	const maxOutput = 6
	shown := all
	if len(all) > maxOutput {
		shown = all[len(all)-maxOutput:]
	}
	var lines []string
	if len(all) > maxOutput {
		lines = append(lines, toolStyle.Render(fmt.Sprintf("  ⋯ %d earlier lines", len(all)-maxOutput)))
	}
	for _, line := range shown {
		lines = append(lines, dimStyle.Render(wrap("  | "+line, width)))
	}
	return lines
}

func renderDiff(oldText, newText string, width int) []string {
	lines := hunkDiff(splitDiffLines(oldText), splitDiffLines(newText), width)
	const maxDiffLines = 24
	if len(lines) > maxDiffLines {
		hidden := len(lines) - maxDiffLines
		lines = append(lines[:maxDiffLines], toolStyle.Render(fmt.Sprintf("  … %d more changed lines", hidden)))
	}
	if len(lines) == 0 {
		lines = append(lines, toolStyle.Render("  (no changes)"))
	}
	return lines
}

// diffOp kinds classify one step of the LCS edit script.
const (
	opSame = iota
	opDel
	opAdd
)

type diffOp struct {
	kind int
	text string
}

// splitDiffLines splits diff input into lines the way every diff renderer
// expects (trailing newline dropped, empty text is one empty line).
func splitDiffLines(text string) []string {
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

// diffOpsMaxInput caps the DP table size; inputs beyond it use the
// whole-block fallback (all removals, then all additions).
const diffOpsMaxInput = 400

// diffOps walks the LCS table for two line slices and returns the edit
// script: unchanged, deleted (-), and added (+) ops in order. Inputs beyond
// the DP cap produce the fallback script.
func diffOps(oldLines, newLines []string) []diffOp {
	if len(oldLines) > diffOpsMaxInput || len(newLines) > diffOpsMaxInput {
		ops := make([]diffOp, 0, len(oldLines)+len(newLines))
		for _, line := range oldLines {
			ops = append(ops, diffOp{opDel, line})
		}
		for _, line := range newLines {
			ops = append(ops, diffOp{opAdd, line})
		}
		return ops
	}

	rows, cols := len(oldLines), len(newLines)
	table := make([][]int, rows+1)
	for i := range table {
		table[i] = make([]int, cols+1)
	}
	for i := rows - 1; i >= 0; i-- {
		for j := cols - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < rows && j < cols {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{opSame, oldLines[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, diffOp{opDel, oldLines[i]})
			i++
		default:
			ops = append(ops, diffOp{opAdd, newLines[j]})
			j++
		}
	}
	for ; i < rows; i++ {
		ops = append(ops, diffOp{opDel, oldLines[i]})
	}
	for ; j < cols; j++ {
		ops = append(ops, diffOp{opAdd, newLines[j]})
	}
	return ops
}

// hunkDiff computes a line-level diff (LCS-based) and renders it as hunks:
// changed lines with up to 2 lines of surrounding context, collapsing runs of
// unchanged lines.
func hunkDiff(oldLines, newLines []string, width int) []string {
	if len(oldLines) > diffOpsMaxInput || len(newLines) > diffOpsMaxInput {
		return formatChanges(oldLines, newLines, width)
	}
	ops := diffOps(oldLines, newLines)

	// Emit hunks: up to 2 context lines around each change, gaps collapsed.
	const context = 2
	var changes []int
	for index, entry := range ops {
		if entry.kind != opSame {
			changes = append(changes, index)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	clamp := func(value, low, high int) int { return min(max(value, low), high) }
	var ranges [][2]int
	start := clamp(changes[0]-context, 0, len(ops))
	end := clamp(changes[0]+context+1, 0, len(ops))
	for _, change := range changes[1:] {
		if change-context <= end {
			end = clamp(change+context+1, 0, len(ops))
		} else {
			ranges = append(ranges, [2]int{start, end})
			start = clamp(change-context, 0, len(ops))
			end = clamp(change+context+1, 0, len(ops))
		}
	}
	ranges = append(ranges, [2]int{start, end})

	var lines []string
	for rangeIndex, rng := range ranges {
		if rangeIndex > 0 {
			lines = append(lines, toolStyle.Render("  ⋯"))
		}
		for index := rng[0]; index < rng[1]; index++ {
			switch ops[index].kind {
			case opDel:
				lines = append(lines, diffDelStyle.Render(wrap("- "+ops[index].text, width)))
			case opAdd:
				lines = append(lines, diffAddStyle.Render(wrap("+ "+ops[index].text, width)))
			default:
				lines = append(lines, dimStyle.Render(wrap("  "+ops[index].text, width)))
			}
		}
	}
	return lines
}

// countDiff returns the +added/-removed line counts of an edit, computed from
// the decoded arguments only (no file I/O). Inputs beyond the DP cap count as
// whole-block replacement.
func countDiff(oldText, newText string) (added, removed int) {
	for _, op := range diffOps(splitDiffLines(oldText), splitDiffLines(newText)) {
		switch op.kind {
		case opAdd:
			added++
		case opDel:
			removed++
		}
	}
	return added, removed
}

// ---- Collapsed Entries ----

// collapsedEntry renders one tool card as the single-line receipt form:
// ⏺ Tool(args) ⎿ outcome. Failed calls keep a visible error marker; the
// detail half is computed from the already-decoded arguments (no I/O).
func collapsedEntry(card *toolCard, width int) string {
	if card == nil {
		return ""
	}
	symbol, style := "⏺", toolStatusStyle(card.status)
	outcome, outcomeStyle := card.status, dimStyle
	switch card.status {
	case "failed", "canceled":
		symbol = "✗"
		outcome, outcomeStyle = card.status, errorStyle
	case "", "running":
		outcome, outcomeStyle = "running…", dimStyle
	}

	args := parseArgs(card.argsRaw)
	var detail string
	switch card.name {
	case "Bash":
		detail = "($ " + collapse(argString(args, "command"), max(width-16, 10)) + ")"
	case "Read":
		detail = ": " + argString(args, "path")
		if limit := argString(args, "limit"); limit != "" {
			detail += " (+" + limit + " lines)"
		}
	case "Write":
		detail = ": " + argString(args, "path") + " +" + humanBytes(len(argString(args, "content")))
	case "Edit":
		added, removed := countDiff(argString(args, "old_text"), argString(args, "new_text"))
		detail = fmt.Sprintf(": %s +%d/-%d", argString(args, "path"), added, removed)
	}

	line := style.Render(symbol+" "+orDefault(card.name, "tool")) + toolStyle.Render(detail)
	line += dimStyle.Render(" ⎿ ") + outcomeStyle.Render(outcome)
	if card.status == "failed" || card.status == "canceled" {
		reason := card.errText
		if reason == "" && card.exitCode != 0 {
			reason = fmt.Sprintf("exit %d", card.exitCode)
		}
		if reason != "" {
			line += errorStyle.Render(": " + collapse(reason, max(width-24, 10)))
		}
	}
	return line
}

// humanBytes renders a byte count the way the Write collapsed entry shows it.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return trimFloat(float64(n)/(1<<20)) + " MB"
	case n >= 1<<10:
		return trimFloat(float64(n)/(1<<10)) + " kB"
	default:
		return itoa(int64(n)) + " B"
	}
}

// all additions.
func formatChanges(oldLines, newLines []string, width int) []string {
	var lines []string
	for _, line := range oldLines {
		lines = append(lines, diffDelStyle.Render(wrap("- "+line, width)))
	}
	for _, line := range newLines {
		lines = append(lines, diffAddStyle.Render(wrap("+ "+line, width)))
	}
	return lines
}

func previewLines(content string, width int) []string {
	if content == "" {
		return nil
	}
	all := strings.Split(strings.TrimRight(content, "\n"), "\n")
	const maxPreview = 8
	shown := all
	if len(all) > maxPreview {
		shown = all[:maxPreview]
	}
	lines := make([]string, 0, len(shown)+1)
	for _, line := range shown {
		lines = append(lines, toolStyle.Render(wrap("  │ "+line, width)))
	}
	if len(all) > maxPreview {
		lines = append(lines, toolStyle.Render(fmt.Sprintf("  … %d more lines", len(all)-maxPreview)))
	}
	return lines
}

func parseArgs(raw string) map[string]any {
	args := map[string]any{}
	if raw == "" {
		return args
	}
	data := []byte(raw)
	// The harness marshals Arguments as a JSON string containing JSON
	// (encoding/json/v2 + jsontext), so unwrap that outer quote first.
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(`"`)) {
		var quoted string
		if err := json.Unmarshal(data, &quoted); err == nil {
			data = []byte(quoted)
		}
	}
	_ = json.Unmarshal(data, &args)
	return args
}

func argString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	return lipgloss.NewStyle().Width(width).Render(text)
}

// ---- session picker ----

// viewPicker renders the launcher in the Command Menu's slot above the
// Composer: a title row, a scrolling window of sessions capped at
// maxPickerRows around the selection, and a (k/total) footer. Height is
// capped and every row is clipped to the terminal width.
func (m Model) viewPicker() string {
	const maxPickerRows = 8
	var out []string
	// A blank line separates the picker from the transcript it floats over,
	// the same framing the Command Menu gets.
	out = append(out, "")
	out = append(out, titleStyle.Render("resume session")+
		dimStyle.Render("  ↑/↓ select · Enter load · Esc cancel"))
	out = append(out, strings.Repeat("─", max(m.width, 1)))
	if len(m.sessions) == 0 {
		out = append(out, dimStyle.Render("no saved sessions"))
	}
	// Scrolling window: keep the selection in view when the list outgrows
	// the cap, like the Command Menu's viewport behavior.
	start := 0
	if len(m.sessions) > maxPickerRows {
		start = m.pickIndex - maxPickerRows/2
		if start < 0 {
			start = 0
		}
		if maxStart := len(m.sessions) - maxPickerRows; start > maxStart {
			start = maxStart
		}
	}
	for index := start; index < len(m.sessions) && index < start+maxPickerRows; index++ {
		entry := m.sessions[index]
		line := "  " + entry.title + dimStyle.Render("  "+entry.updated)
		if index == m.pickIndex {
			out = append(out, statusStyle.Render("▸ "+line))
		} else {
			out = append(out, line)
		}
	}
	selected := clampIndex(m.pickIndex, len(m.sessions)) // rendering must not write state
	footer := fmt.Sprintf("(%d/%d)  Enter load · Esc cancel", selected+1, len(m.sessions))
	out = append(out, dimStyle.Render(footer))
	for index := range out {
		out[index] = m.truncateToWidth(out[index])
	}
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

var _ textarea.Model
