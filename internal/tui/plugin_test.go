package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"context-bridge/internal/opencode"
)

func TestInstallPluginCmdPatchesResolvedBinaryCommand(t *testing.T) {
	testHome := t.TempDir()
	configHome := testHome
	if runtime.GOOS == "darwin" {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", testHome)
		configHome = filepath.Join(testHome, "Library", "Application Support")
	} else {
		t.Setenv("XDG_CONFIG_HOME", configHome)
	}

	msg, ok := installPluginCmd()().(pluginInstalledMsg)
	if !ok {
		t.Fatalf("expected pluginInstalledMsg, got %T", installPluginCmd()())
	}
	if msg.err != nil {
		t.Fatalf("install plugin command failed: %v", msg.err)
	}

	expectedDir := filepath.Join(configHome, "opencode", "plugins")
	if msg.path != expectedDir {
		t.Fatalf("expected plugin dir %q, got %q", expectedDir, msg.path)
	}

	pluginPath := filepath.Join(expectedDir, "context-bridge.ts")
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin file: %v", err)
	}

	resolvedCommand := resolveContextBridgeCommand()
	expectedLine := expectedBridgeBinLine(resolvedCommand)
	if !strings.Contains(string(data), expectedLine) {
		t.Fatalf("expected plugin file to contain %q, got:\n%s", expectedLine, string(data))
	}
	if resolvedCommand != "context-bridge" && strings.Contains(string(data), `?? "context-bridge";`) {
		t.Fatalf("expected plugin file to replace the default fallback command, got:\n%s", string(data))
	}

	status, err := opencode.InspectIntegration()
	if err != nil {
		t.Fatalf("inspect integration: %v", err)
	}
	if status.State != opencode.StateOwnedCurrent {
		t.Fatalf("expected TUI install to create an owned adapter, got %+v", status)
	}
	if _, err := os.Stat(filepath.Join(configHome, "opencode", "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("expected TUI install not to create or rewrite opencode.json, stat err=%v", err)
	}
}

func expectedBridgeBinLine(command string) string {
	if command == "context-bridge" {
		return `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";`
	}

	return fmt.Sprintf(
		`const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? %q;`,
		command,
	)
}
