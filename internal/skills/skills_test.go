package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	path := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: does " + name + "\n" + frontmatter + "---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDirsPrecedence(t *testing.T) {
	home := t.TempDir()
	// Repo root stops the ancestor walk.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "sub", "workspace")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	dirs := DefaultDirs(nested, home)

	wantHarness := filepath.Join(nested, ".harness", "skills")
	wantRepo := filepath.Join(root, ".agents", "skills")
	wantUser := filepath.Join(home, ".agents", "skills")

	found := map[string]bool{}
	for _, dir := range dirs {
		found[dir] = true
	}
	if !found[wantHarness] || !found[wantRepo] || !found[wantUser] {
		t.Fatalf("default dirs missing expected entries:\n%v", dirs)
	}
	// Repo root must be the last project ancestor (no walk past .git).
	for _, dir := range dirs {
		if dir == filepath.Join(filepath.Dir(root), ".agents", "skills") {
			t.Fatalf("ancestor walk went past the repo root:\n%v", dirs)
		}
	}
}

func TestDiscoverRecursiveAndFlags(t *testing.T) {
	base := t.TempDir()
	writeSkill(t, base, "alpha", "", "Alpha body.")
	// Nested skill (recursive discovery) with the user-only flag.
	nested := filepath.Join(base, "pack", "beta")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"),
		[]byte("---\nname: beta\ndescription: does beta\ndisable-model-invocation: true\n---\n\nBeta body.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Malformed: no description → skipped, as in pi.
	writeSkill(t, base, "broken", "", "")
	if err := os.Remove(filepath.Join(base, "broken", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "broken", "SKILL.md"), []byte("---\nname: broken\n---\nno description\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := Discover([]string{base})
	var names []string
	byName := map[string]Entry{}
	for _, entry := range entries {
		names = append(names, entry.Name)
		byName[entry.Name] = entry
	}
	if len(names) != 2 {
		t.Fatalf("expected alpha+beta (broken skipped), got %v", names)
	}
	if byName["alpha"].UserOnly || !byName["beta"].UserOnly {
		t.Fatalf("UserOnly flags wrong: %+v", entries)
	}
	if body, err := Body(byName["alpha"].Path); err != nil || body != "Alpha body." {
		t.Fatalf("body mismatch: %q (%v)", body, err)
	}
}

func TestDiscoverNameCollisionFirstWins(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeSkill(t, first, "dup", "", "First body.")
	writeSkill(t, second, "dup", "", "Second body.")

	entries := Discover([]string{first, second})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %+v", entries)
	}
	body, err := Body(entries[0].Path)
	if err != nil || body != "First body." {
		t.Fatalf("first dir should win: %q (%v)", body, err)
	}
}
