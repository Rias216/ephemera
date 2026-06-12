package tui

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates all command/config writes from the real user profile. TUI
// commands intentionally persist settings, so tests must never share the host's
// config directory or leave temporary workspace state behind.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "ephemera-tui-test-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated TUI config:", err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", root)
	_ = os.Setenv("APPDATA", root)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
