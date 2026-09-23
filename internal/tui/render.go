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
	// Per-block cache: between events only one or two blocks change, so most
	// renders are pure cache hits (keeps long sessions from re-wrapping
	// everything on every tool status).
	if b.rendered != "" && b.width == width {
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
		out = m.renderToolCard(b.tool, width)
	}
	b.rendered, b.width = out, width
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

func (m *Model) renderToolCard(card *toolCard, width int) string {
	if card == nil {
		return ""
	}
	symbol, style := "⚙", toolStyle
	switch card.status {
	case "ok":
		symbol, style = "✓", lipgloss.NewStyle()
	case "failed", "canceled":
		symbol, style = "✗", errorStyle
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
	oldLines := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(newText, "\n"), "\n")
	lines := hunkDiff(oldLines, newLines, width)
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

// hunkDiff computes a line-level diff (LCS-based) and renders it as hunks:
// changed lines with up to 2 lines of surrounding context, collapsing runs of
// unchanged lines.
func hunkDiff(oldLines, newLines []string, width int) []string {
	const maxInput = 400 // cap DP size; beyond this fall back to whole-block
	if len(oldLines) > maxInput || len(newLines) > maxInput {
		return formatChanges(oldLines, newLines, width)
	}

	// LCS table.
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

	// Walk to ops: unchanged, deleted (-), added (+).
	const (
		opSame = iota
		opDel
		opAdd
	)
	type op struct {
		kind int
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < rows && j < cols {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, op{opSame, oldLines[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{opDel, oldLines[i]})
			i++
		default:
			ops = append(ops, op{opAdd, newLines[j]})
			j++
		}
	}
	for ; i < rows; i++ {
		ops = append(ops, op{opDel, oldLines[i]})
	}
	for ; j < cols; j++ {
		ops = append(ops, op{opAdd, newLines[j]})
	}

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

// formatChanges is the fallback for very large edits: show all removals then
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

func (m Model) viewPicker() string {
	var out []string
	out = append(out, titleStyle.Render("resume session")+
		dimStyle.Render("  ↑/↓ select · Enter load · Esc cancel"))
	out = append(out, strings.Repeat("─", max(m.width, 1)))
	if len(m.sessions) == 0 {
		out = append(out, dimStyle.Render("no saved sessions"))
	}
	for index, entry := range m.sessions {
		line := "  " + entry.title + dimStyle.Render("  "+entry.updated)
		if index == m.pickIndex {
			out = append(out, statusStyle.Render("▸ "+line))
		} else {
			out = append(out, line)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

var _ textarea.Model
