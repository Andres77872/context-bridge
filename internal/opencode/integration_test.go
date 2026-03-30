package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPluginWritesPatchedBinaryAndRemovePluginDeletesIt(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	pluginPath, err := InstallPlugin("/abs/path/context-bridge")
	if err != nil {
		t.Fatalf("install plugin: %v", err)
	}

	expectedPath := filepath.Join(configHome, "opencode", "plugins", "context-bridge.ts")
	if pluginPath != expectedPath {
		t.Fatalf("expected plugin path %q, got %q", expectedPath, pluginPath)
	}

	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin file: %v", err)
	}

	if !strings.Contains(string(data), `?? "/abs/path/context-bridge";`) {
		t.Fatalf("expected patched plugin to contain absolute binary path, got:\n%s", string(data))
	}

	removed, removedPath, err := RemovePlugin()
	if err != nil {
		t.Fatalf("remove plugin: %v", err)
	}
	if !removed {
		t.Fatalf("expected plugin removal to report success")
	}
	if removedPath != pluginPath {
		t.Fatalf("expected removed path %q, got %q", pluginPath, removedPath)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("expected plugin file to be removed, stat err=%v", err)
	}

	removed, removedPath, err = RemovePlugin()
	if err != nil {
		t.Fatalf("second remove plugin: %v", err)
	}
	if removed {
		t.Fatalf("expected second plugin removal to be a no-op")
	}
	if removedPath != pluginPath {
		t.Fatalf("expected noop remove path %q, got %q", pluginPath, removedPath)
	}
}

func TestEnsureMCPRegistrationCreatesJSONConfigWhenMissing(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	if err := EnsureMCPRegistration([]string{"/abs/context-bridge", "mcp"}); err != nil {
		t.Fatalf("ensure mcp registration: %v", err)
	}

	configPath := filepath.Join(configHome, "opencode", "opencode.json")
	config := readOpenCodeConfig(t, configPath)
	mcp := decodeMCPBlock(t, config)

	entry := decodeMCPEntry(t, mcp["context-bridge"])
	if entry["type"] != "local" {
		t.Fatalf("expected type local, got %#v", entry["type"])
	}
	if entry["enabled"] != true {
		t.Fatalf("expected enabled=true, got %#v", entry["enabled"])
	}
	command, ok := entry["command"].([]any)
	if !ok {
		t.Fatalf("expected command array, got %#v", entry["command"])
	}
	if len(command) != 2 || command[0] != "/abs/context-bridge" || command[1] != "mcp" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if _, err := os.Stat(filepath.Join(configHome, "opencode", "opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatalf("expected opencode.jsonc to stay absent, stat err=%v", err)
	}
}

func TestEnsureMCPRegistrationPrefersJSONCAndPreservesSiblingEntries(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := filepath.Join(configHome, "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
		// existing config
		"theme": "dark",
		"mcp": {
			"other": {"type": "remote", "enabled": false}
		}
	}`), 0644); err != nil {
		t.Fatalf("write jsonc config: %v", err)
	}

	if err := EnsureMCPRegistration([]string{"/abs/context-bridge", "mcp"}); err != nil {
		t.Fatalf("ensure mcp registration: %v", err)
	}

	config := readOpenCodeConfig(t, configPath)
	if theme := decodeStringRaw(t, config["theme"]); theme != "dark" {
		t.Fatalf("expected theme to be preserved, got %q", theme)
	}

	mcp := decodeMCPBlock(t, config)
	if _, ok := mcp["other"]; !ok {
		t.Fatalf("expected sibling mcp entry to be preserved")
	}
	if _, ok := mcp["context-bridge"]; !ok {
		t.Fatalf("expected context-bridge mcp entry to be added")
	}
}

func TestEnsureMCPRegistrationPrefersJSONCOverJSONWhenBothExist(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	jsoncPath := filepath.Join(configHome, "opencode", "opencode.jsonc")
	writeJSONConfig(t, jsoncPath, `{
		"theme": "dark",
		"mcp": {
			"other": {"type": "remote", "enabled": false}
		}
	}`)

	jsonPath := filepath.Join(configHome, "opencode", "opencode.json")
	writeJSONConfig(t, jsonPath, `{
		"theme": "light"
	}`)

	if err := EnsureMCPRegistration([]string{"/abs/context-bridge", "mcp"}); err != nil {
		t.Fatalf("ensure mcp registration: %v", err)
	}

	jsoncConfig := readOpenCodeConfig(t, jsoncPath)
	if theme := decodeStringRaw(t, jsoncConfig["theme"]); theme != "dark" {
		t.Fatalf("expected jsonc theme to stay dark, got %q", theme)
	}
	jsoncMCP := decodeMCPBlock(t, jsoncConfig)
	if _, ok := jsoncMCP["other"]; !ok {
		t.Fatalf("expected sibling jsonc mcp entry to be preserved")
	}
	if _, ok := jsoncMCP["context-bridge"]; !ok {
		t.Fatalf("expected context-bridge entry to be added to jsonc config")
	}

	jsonConfig := readOpenCodeConfig(t, jsonPath)
	if theme := decodeStringRaw(t, jsonConfig["theme"]); theme != "light" {
		t.Fatalf("expected json fallback file to remain untouched, got %q", theme)
	}
	if _, ok := jsonConfig["mcp"]; ok {
		t.Fatalf("expected json fallback file to remain untouched")
	}
}

func TestRemoveMCPRegistrationRemovesOnlyContextBridgeEntry(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := filepath.Join(configHome, "opencode", "opencode.json")
	writeJSONConfig(t, configPath, `{
		"theme": "dark",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["context-bridge", "mcp"], "enabled": true},
			"other": {"type": "remote", "enabled": false}
		}
	}`)

	removed, removedPath, err := RemoveMCPRegistration()
	if err != nil {
		t.Fatalf("remove mcp registration: %v", err)
	}
	if !removed {
		t.Fatalf("expected mcp registration removal to report success")
	}
	if removedPath != configPath {
		t.Fatalf("expected config path %q, got %q", configPath, removedPath)
	}

	config := readOpenCodeConfig(t, configPath)
	if theme := decodeStringRaw(t, config["theme"]); theme != "dark" {
		t.Fatalf("expected theme to be preserved, got %q", theme)
	}
	mcp := decodeMCPBlock(t, config)
	if _, ok := mcp["context-bridge"]; ok {
		t.Fatalf("expected context-bridge entry to be removed")
	}
	if _, ok := mcp["other"]; !ok {
		t.Fatalf("expected sibling mcp entry to remain")
	}
}

func TestRemoveMCPRegistrationDeletesEmptyMCPBlock(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	configPath := filepath.Join(configHome, "opencode", "opencode.json")
	writeJSONConfig(t, configPath, `{
		"theme": "dark",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["context-bridge", "mcp"], "enabled": true}
		}
	}`)

	removed, _, err := RemoveMCPRegistration()
	if err != nil {
		t.Fatalf("remove mcp registration: %v", err)
	}
	if !removed {
		t.Fatalf("expected mcp registration removal to report success")
	}

	config := readOpenCodeConfig(t, configPath)
	if _, ok := config["mcp"]; ok {
		t.Fatalf("expected empty mcp block to be removed")
	}
	if theme := decodeStringRaw(t, config["theme"]); theme != "dark" {
		t.Fatalf("expected theme to remain, got %q", theme)
	}
}

func TestRemoveMCPRegistrationPrefersJSONCOverJSON(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	jsoncPath := filepath.Join(configHome, "opencode", "opencode.jsonc")
	writeJSONConfig(t, jsoncPath, `{
		"theme": "dark",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["context-bridge", "mcp"], "enabled": true},
			"other": {"type": "remote", "enabled": false}
		}
	}`)

	jsonPath := filepath.Join(configHome, "opencode", "opencode.json")
	writeJSONConfig(t, jsonPath, `{
		"theme": "light",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["old", "mcp"], "enabled": true}
		}
	}`)

	removed, removedPath, err := RemoveMCPRegistration()
	if err != nil {
		t.Fatalf("remove mcp registration: %v", err)
	}
	if !removed {
		t.Fatalf("expected mcp registration removal to report success")
	}
	if removedPath != jsoncPath {
		t.Fatalf("expected removal to target jsonc path %q, got %q", jsoncPath, removedPath)
	}

	jsoncConfig := readOpenCodeConfig(t, jsoncPath)
	jsoncMCP := decodeMCPBlock(t, jsoncConfig)
	if _, ok := jsoncMCP["context-bridge"]; ok {
		t.Fatalf("expected context-bridge entry to be removed from jsonc config")
	}
	if _, ok := jsoncMCP["other"]; !ok {
		t.Fatalf("expected sibling jsonc mcp entry to remain")
	}

	jsonConfig := readOpenCodeConfig(t, jsonPath)
	jsonMCP := decodeMCPBlock(t, jsonConfig)
	if _, ok := jsonMCP["context-bridge"]; !ok {
		t.Fatalf("expected json fallback file to remain untouched")
	}
}

func TestRemoveMCPRegistrationIsNoopWhenConfigMissing(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	removed, removedPath, err := RemoveMCPRegistration()
	if err != nil {
		t.Fatalf("remove missing mcp registration: %v", err)
	}
	if removed {
		t.Fatalf("expected missing config removal to be a no-op")
	}
	if removedPath != "" {
		t.Fatalf("expected empty removed path, got %q", removedPath)
	}
}

func readOpenCodeConfig(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal OpenCode config: %v\ncontent:\n%s", err, string(data))
	}

	return config
}

func decodeMCPBlock(t *testing.T, config map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	var mcp map[string]json.RawMessage
	if err := json.Unmarshal(config["mcp"], &mcp); err != nil {
		t.Fatalf("unmarshal mcp block: %v", err)
	}
	return mcp
}

func decodeMCPEntry(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()

	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal mcp entry: %v", err)
	}
	return entry
}

func decodeStringRaw(t *testing.T, raw json.RawMessage) string {
	t.Helper()

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal string value: %v", err)
	}
	return value
}

func writeJSONConfig(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
