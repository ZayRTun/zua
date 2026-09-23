// Command zua-agent is a headless unreal-agent runner with added
// Read/Write/Edit file tools. It writes a meta event followed by session
// items as JSONL on stdout and exits when the task finishes.
package main

import (
	"context"
	"os"

	"unreal-agent-tui/internal/runner"
)

func main() {
	os.Exit(runner.Run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
