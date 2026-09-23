// Package skills implements pi-compatible skill discovery (Agent Skills
// spec locations, recursive SKILL.md scan, disable-model-invocation flag)
// shared by the zua TUI and the zua-agent runner.
package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one discovered skill.
type Entry struct {
	Name        string
	Description string
	Path        string // SKILL.md path
	UserOnly    bool   // disable-model-invocation: true → not a model tool
}

// DefaultDirs returns the directories zua scans for skills, in precedence
// order (first wins on name collisions, mirroring pi):
//
//  1. <workspace>/.harness/skills  — harness-native location
//  2. .agents/skills from the workspace up through ancestors, stopping at the
//     repository root (pi's project skills)
//  3. ~/.agents/skills — pi's user skills
//
// Extra dirs (from -skills) are appended by the caller.
func DefaultDirs(workspace, home string) []string {
	var dirs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		if resolved, err := filepath.Abs(dir); err == nil {
			dir = resolved
		}
		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}

	add(filepath.Join(workspace, ".harness", "skills"))

	// Project .agents/skills: walk ancestors, stop at the repo root (after
	// including it) or at the home directory boundary.
	absolute := workspace
	if resolved, err := filepath.Abs(workspace); err == nil {
		absolute = resolved
	}
	current := absolute
	for {
		add(filepath.Join(current, ".agents", "skills"))
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || (home != "" && current == home) {
			break
		}
		current = parent
	}

	if home != "" {
		add(filepath.Join(home, ".agents", "skills"))
	}
	return dirs
}

// Discover recursively scans the directories for SKILL.md files and returns
// the entries sorted by name. Missing directories are skipped; malformed
// skills (no description) are ignored, as in pi. Name collisions keep the
// first discovered skill.
func Discover(dirs []string) []Entry {
	seenNames := map[string]bool{}
	var entries []Entry
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable subtrees
			}
			if entry.IsDir() && entry.Name() == "node_modules" {
				return fs.SkipDir
			}
			if entry.IsDir() || entry.Name() != "SKILL.md" {
				return nil
			}
			skill, ok := loadEntry(path)
			if !ok || seenNames[skill.Name] {
				return nil
			}
			seenNames[skill.Name] = true
			entries = append(entries, skill)
			return nil
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// loadEntry parses one SKILL.md into an Entry; ok=false means malformed
// (missing description) — pi skips those too.
func loadEntry(path string) (Entry, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, false
	}
	fields := parseFrontmatter(string(contents))
	name := fields["name"]
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	description := fields["description"]
	if description == "" {
		return Entry{}, false
	}
	return Entry{
		Name:        name,
		Description: description,
		Path:        path,
		UserOnly:    isTruthy(fields["disable-model-invocation"]),
	}, true
}

// Body returns the SKILL.md content without the frontmatter block.
func Body(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(contents)
	if !strings.HasPrefix(text, "---") {
		return strings.TrimSpace(text), nil
	}
	lines := strings.Split(text, "\n")
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			return strings.TrimSpace(strings.Join(lines[index+1:], "\n")), nil
		}
	}
	return strings.TrimSpace(text), nil
}

// parseFrontmatter extracts key: value pairs from a SKILL.md YAML frontmatter
// block (between leading --- fences). Values are single-line, unquoted.
func parseFrontmatter(contents string) map[string]string {
	fields := map[string]string{}
	lines := strings.Split(contents, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fields
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			fields[key] = value
		}
	}
	return fields
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1":
		return true
	}
	return false
}
