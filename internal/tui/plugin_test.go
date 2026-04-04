package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPluginCmdPatchesResolvedBinaryCommand(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

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

	configPath := filepath.Join(configHome, "opencode", "opencode.json")
	config := readPluginConfig(t, configPath)
	mcp := decodePluginMCPBlock(t, config)
	entry := decodePluginMCPEntry(t, mcp["context-bridge"])
	command, ok := entry["command"].([]any)
	if !ok {
		t.Fatalf("expected command array, got %#v", entry["command"])
	}
	if len(command) != 2 || command[0] != resolvedCommand || command[1] != "mcp" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if entry["enabled"] != true {
		t.Fatalf("expected enabled=true, got %#v", entry["enabled"])
	}
}

func expectedBridgeBinLine(command string) string {
	if command == "context-bridge" {
		return `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";`
	}

	return fmt.Sprintf(
		`const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? %q;`,
		command,
	)
}

func readPluginConfig(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plugin config: %v", err)
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal plugin config: %v\ncontent:\n%s", err, string(data))
	}

	return config
}

func decodePluginMCPBlock(t *testing.T, config map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	var mcp map[string]json.RawMessage
	if err := json.Unmarshal(config["mcp"], &mcp); err != nil {
		t.Fatalf("unmarshal plugin mcp block: %v", err)
	}

	return mcp
}

func decodePluginMCPEntry(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()

	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal plugin mcp entry: %v", err)
	}

	return entry
}
