package tui

import (
	"context-bridge/plugin"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type pluginInstalledMsg struct {
	err  error
	path string
}

func installPluginCmd() tea.Cmd {
	return func() tea.Msg {
		pluginDir, err := openCodePluginDir()
		if err != nil {
			return pluginInstalledMsg{err: fmt.Errorf("could not resolve plugin dir: %w", err)}
		}

		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			return pluginInstalledMsg{err: fmt.Errorf("could not create plugin dir: %w", err)}
		}

		data := patchBridgeBINLine(plugin.OpenCodePlugin, resolveContextBridgeCommand())
		pluginPath := filepath.Join(pluginDir, "context-bridge.ts")
		if err := os.WriteFile(pluginPath, data, 0644); err != nil {
			return pluginInstalledMsg{err: fmt.Errorf("could not write plugin file: %w", err)}
		}

		if err := injectOpenCodeMCP(); err != nil {
			// Non-fatal, just write to log or ignore
		}

		return pluginInstalledMsg{err: nil, path: pluginDir}
	}
}

func openCodeConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func openCodePluginDir() (string, error) {
	dir, err := openCodeConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugins"), nil
}

func resolveContextBridgeCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "context-bridge"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

func patchBridgeBINLine(src []byte, absBin string) []byte {
	const marker = `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";`

	var replacement string
	if absBin == "context-bridge" {
		replacement = marker
	} else {
		replacement = fmt.Sprintf(
			`const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? %q;`,
			absBin,
		)
	}

	return []byte(strings.Replace(string(src), marker, replacement, 1))
}

func injectOpenCodeMCP() error {
	dir, err := openCodeConfigDir()
	if err != nil {
		return err
	}
	configPath := filepath.Join(dir, "opencode.jsonc")
	if _, err := os.Stat(configPath); err != nil {
		configPath = filepath.Join(dir, "opencode.json")
	}

	var config map[string]json.RawMessage
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			config = make(map[string]json.RawMessage)
		} else {
			return fmt.Errorf("read config: %w", err)
		}
	} else {
		cleaned := stripJSONC(data)
		if err := json.Unmarshal(cleaned, &config); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}

	var mcpBlock map[string]json.RawMessage
	if raw, exists := config["mcp"]; exists {
		if err := json.Unmarshal(raw, &mcpBlock); err != nil {
			return fmt.Errorf("parse mcp block: %w", err)
		}
	} else {
		mcpBlock = make(map[string]json.RawMessage)
	}

	if _, exists := mcpBlock["context-bridge"]; exists {
		return nil
	}

	entry := map[string]interface{}{
		"type":    "local",
		"command": []string{resolveContextBridgeCommand(), "mcp"},
		"enabled": true,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	mcpBlock["context-bridge"] = json.RawMessage(entryJSON)

	mcpJSON, err := json.Marshal(mcpBlock)
	if err != nil {
		return err
	}
	config["mcp"] = json.RawMessage(mcpJSON)

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, output, 0644)
}

func stripJSONC(data []byte) []byte {
	var out []byte
	i := 0
	for i < len(data) {
		if data[i] == '"' {
			out = append(out, data[i])
			i++
			for i < len(data) && data[i] != '"' {
				if data[i] == '\\' && i+1 < len(data) {
					out = append(out, data[i], data[i+1])
					i += 2
					continue
				}
				out = append(out, data[i])
				i++
			}
			if i < len(data) {
				out = append(out, data[i])
				i++
			}
			continue
		}
		if i+1 < len(data) && data[i] == '/' && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(data) && data[i] == '/' && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			if i+1 < len(data) {
				i += 2
			} else {
				i = len(data)
			}
			continue
		}
		out = append(out, data[i])
		i++
	}
	return out
}
