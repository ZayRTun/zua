package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the agent binary at an instant-exit stub for every test
// in this package: turns start (running=true) but no real agent process
// runs, so spawned-turn tests stay hermetic — no async session writes
// racing t.TempDir cleanup. Runner e2e coverage lives in internal/runner.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "zua-agent-stub")
	if err != nil {
		panic(err)
	}
	stub := filepath.Join(dir, "zua-agent")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		panic(err)
	}
	os.Setenv("ZUA_AGENT_PATH", stub)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
