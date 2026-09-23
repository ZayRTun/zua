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

func TestCollapsedEntryMapping(t *testing.T) {
	cases := []struct {
		name string
		card *toolCard
		want string
	}{
		{
			name: "bash",
			card: &toolCard{callID: "c1", name: "Bash", argsRaw: `{"command":"go test ./...\nverbose"}`, status: "ok"},
			want: "⏺ Bash($ go test ./... verbose) ⎿ ok",
		},
		{
			name: "read with lines",
			card: &toolCard{callID: "c2", name: "Read", argsRaw: `{"path":"main.go","offset":"10","limit":"40"}`, status: "ok"},
			want: "⏺ Read: main.go (+40 lines) ⎿ ok",
		},
		{
			name: "read without lines",
			card: &toolCard{callID: "c3", name: "Read", argsRaw: `{"path":"main.go"}`, status: "ok"},
			want: "⏺ Read: main.go ⎿ ok",
		},
		{
			name: "write bytes",
			card: &toolCard{callID: "c4", name: "Write", argsRaw: `{"path":"hello.txt","content":"hi"}`, status: "ok"},
			want: "⏺ Write: hello.txt +2 B ⎿ ok",
		},
		{
			name: "edit counts",
			card: &toolCard{callID: "c5", name: "Edit", argsRaw: `{"path":"main.go","old_text":"a\nb\nc","new_text":"a\nX\nc\nd"}`, status: "ok"},
			want: "⏺ Edit: main.go +2/-1 ⎿ ok",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := stripANSI(collapsedEntry(testCase.card, 200))
			if got != testCase.want {
				t.Fatalf("collapsedEntry = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestCollapsedEntryFailureMarker(t *testing.T) {
	card := &toolCard{callID: "c1", name: "Bash", argsRaw: `{"command":"exit 3"}`, status: "failed", errText: "command exited with code 3"}
	line := stripANSI(collapsedEntry(card, 200))
	if !strings.Contains(line, "✗") {
		t.Fatalf("failed card missing ✗ marker: %q", line)
	}
	if !strings.Contains(line, "failed") || !strings.Contains(line, "code 3") {
		t.Fatalf("failed card missing outcome/error: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("collapsed entry must stay on one line: %q", line)
	}
}

func TestCollapsedEntryPending(t *testing.T) {
	card := &toolCard{callID: "c1", name: "Bash", argsRaw: `{"command":"sleep 5"}`, status: "running"}
	line := stripANSI(collapsedEntry(card, 200))
	if !strings.Contains(line, "running") {
		t.Fatalf("running card missing pending outcome: %q", line)
	}
}

func TestCountDiff(t *testing.T) {
	added, removed := countDiff("a\nb\nc", "a\nX\nc\nd")
	if added != 2 || removed != 1 {
		t.Fatalf("countDiff = +%d/-%d, want +2/-1", added, removed)
	}
	if added, removed := countDiff("same", "same"); added != 0 || removed != 0 {
		t.Fatalf("identical input = +%d/-%d, want 0/0", added, removed)
	}
	// Beyond the DP cap the fallback counts whole blocks.
	old := strings.Repeat("x\n", 500)
	added, removed = countDiff(old, "new")
	if added != 1 || removed != 500 {
		t.Fatalf("large fallback = +%d/-%d, want +1/-500", added, removed)
	}
}
