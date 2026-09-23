// Command zua is a terminal UI for the unreal-agent harness, in the
// style of a pair-programming CLI: type a request, watch the agent read,
// edit, and run things in your project.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"unreal-agent-tui/internal/tui"
)

func main() {
	workspace := flag.String("workspace", ".", "workspace directory the agent operates in")
	model := flag.String("model", "", "model id (default: provider default or UNREAL_TUI_MODEL)")
	provider := flag.String("provider", "", "llm provider: openai, commandcode, openrouter, fireworks, ollama")
	skillDirs := flag.String("skills", "", "comma-separated extra skill directories (beyond <workspace>/.harness/skills)")
	flag.Parse()

	var extras []string
	if *skillDirs != "" {
		for _, dir := range strings.Split(*skillDirs, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				extras = append(extras, dir)
			}
		}
	}

	// Alt screen + no mouse capture: terminals (Ghostty, iTerm2, …) then
	// translate wheel scroll into arrow keys, which Update routes to the
	// transcript viewport — and text selection keeps working.
	program := tea.NewProgram(
		tui.New(*workspace, *provider, *model, extras),
		tea.WithAltScreen(),
		tea.WithFilter(tui.CSIFilter),
	)
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
