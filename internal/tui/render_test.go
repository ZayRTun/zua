package tui

import (
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes terminal styling so tests can assert on plain text.
func stripANSI(text string) string {
	return ansiPattern.ReplaceAllString(text, "")
}

// rendered strips ANSI styling so tests can assert on plain text.
func rendered(lines []string) []string {
	out := make([]string, len(lines))
	for index, line := range lines {
		out[index] = stripANSI(line)
	}
	return out
}

func TestHunkDiffMinimalEdit(t *testing.T) {
	oldLines := strings.Split("a\nb\nc\nd\ne", "\n")
	newLines := strings.Split("a\nb\nX\nd\ne", "\n")
	lines := rendered(hunkDiff(oldLines, newLines, 80))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "- c") || !strings.Contains(joined, "+ X") {
		t.Fatalf("diff missing the c→X change:\n%s", joined)
	}
	if !strings.Contains(joined, "  b") || !strings.Contains(joined, "  d") {
		t.Fatalf("diff missing context lines:\n%s", joined)
	}
	if strings.Contains(joined, "- a") || strings.Contains(joined, "+ e") {
		t.Fatalf("unchanged lines rendered as changes:\n%s", joined)
	}
}

func TestHunkDiffInsertAndDelete(t *testing.T) {
	if lines := hunkDiff([]string{"a"}, []string{"a"}, 80); len(lines) != 0 {
		t.Fatalf("identical inputs: got %v, want no hunks", lines)
	}
	lines := rendered(hunkDiff([]string{"a", "b"}, []string{"a", "b", "c", "d"}, 80))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "+ c") || !strings.Contains(joined, "+ d") {
		t.Fatalf("insert: got\n%s", joined)
	}
	lines = rendered(hunkDiff([]string{"a", "b", "c"}, []string{"a"}, 80))
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "- b") || !strings.Contains(joined, "- c") {
		t.Fatalf("delete: got\n%s", joined)
	}
}

func TestRenderDiffNoChanges(t *testing.T) {
	lines := renderDiff("same\nlines", "same\nlines", 80)
	if len(lines) != 1 || !strings.Contains(stripANSI(lines[0]), "no changes") {
		t.Fatalf("identical edit: got %v, want a (no changes) placeholder", lines)
	}
}

func TestRenderDiffLargeEditFallsBack(t *testing.T) {
	oldLines := make([]string, 0, 500)
	for i := range 500 {
		oldLines = append(oldLines, "old"+string(rune('a'+i%26)))
	}
	lines := renderDiff(strings.Join(oldLines, "\n"), "new", 80)
	if len(lines) == 0 {
		t.Fatal("large edit rendered nothing")
	}
	if !strings.Contains(stripANSI(lines[0]), "- old") {
		t.Fatalf("fallback should list removals first, got %q", stripANSI(lines[0]))
	}
}

func TestStderrTailKeepsLastLines(t *testing.T) {
	var buffer stderrTail
	for i := range maxStderrLines + 10 {
		buffer.Write([]byte("line " + itoa(int64(i)) + "\n"))
	}
	tail := buffer.Tail(6)
	if !strings.Contains(tail, "line "+itoa(int64(maxStderrLines+9))) {
		t.Fatalf("tail missing newest line: %q", tail)
	}
	if strings.Contains(tail, "line 0\n") {
		t.Fatalf("tail should have dropped oldest lines: %q", tail)
	}
	if !strings.HasPrefix(tail, "…") {
		t.Fatalf("tail should note hidden lines: %q", tail)
	}
}
