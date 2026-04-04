package opencode

import (
	"context-bridge/plugin"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func OpenCodeConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".config", "opencode"), nil
}

func OpenCodePluginPath() (string, error) {
	configDir, err := OpenCodeConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(configDir, "plugins", "context-bridge.ts"), nil
}

func InstallPlugin(absBin string) (string, error) {
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		return "", fmt.Errorf("create plugin dir: %w", err)
	}

	data := patchBridgeBINLine(plugin.OpenCodePlugin, absBin)
	if err := os.WriteFile(pluginPath, data, 0644); err != nil {
		return "", fmt.Errorf("write plugin file: %w", err)
	}

	return pluginPath, nil
}

func RemovePlugin() (bool, string, error) {
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		return false, "", err
	}

	if err := os.Remove(pluginPath); err != nil {
		if os.IsNotExist(err) {
			return false, pluginPath, nil
		}
		return false, pluginPath, fmt.Errorf("remove plugin file: %w", err)
	}

	return true, pluginPath, nil
}

func EnsureMCPRegistration(command []string) error {
	configPath, config, err := loadOpenCodeConfig(true)
	if err != nil {
		return err
	}

	mcpBlock, err := decodeRawMap(config, "mcp")
	if err != nil {
		return err
	}

	if _, exists := mcpBlock["context-bridge"]; exists {
		return nil
	}

	entryJSON, err := json.Marshal(map[string]any{
		"type":    "local",
		"command": command,
		"enabled": true,
	})
	if err != nil {
		return fmt.Errorf("marshal mcp entry: %w", err)
	}
	mcpBlock["context-bridge"] = json.RawMessage(entryJSON)

	config["mcp"], err = marshalRawMap(mcpBlock)
	if err != nil {
		return err
	}

	return writeOpenCodeConfig(configPath, config)
}

func RemoveMCPRegistration() (bool, string, error) {
	configPath, config, err := loadOpenCodeConfig(false)
	if err != nil {
		return false, "", err
	}
	if configPath == "" {
		return false, "", nil
	}

	mcpBlock, err := decodeRawMap(config, "mcp")
	if err != nil {
		return false, configPath, err
	}

	if _, exists := mcpBlock["context-bridge"]; !exists {
		return false, configPath, nil
	}

	delete(mcpBlock, "context-bridge")
	if len(mcpBlock) == 0 {
		delete(config, "mcp")
	} else {
		config["mcp"], err = marshalRawMap(mcpBlock)
		if err != nil {
			return false, configPath, err
		}
	}

	if err := writeOpenCodeConfig(configPath, config); err != nil {
		return false, configPath, err
	}

	return true, configPath, nil
}

func loadOpenCodeConfig(createIfMissing bool) (string, map[string]json.RawMessage, error) {
	configDir, err := OpenCodeConfigDir()
	if err != nil {
		return "", nil, err
	}

	jsoncPath := filepath.Join(configDir, "opencode.jsonc")
	jsonPath := filepath.Join(configDir, "opencode.json")

	configPath := jsonPath
	if _, err := os.Stat(jsoncPath); err == nil {
		configPath = jsoncPath
	} else if err != nil && !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("stat config %s: %w", jsoncPath, err)
	} else if _, err := os.Stat(jsonPath); err == nil {
		configPath = jsonPath
	} else if err != nil && !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("stat config %s: %w", jsonPath, err)
	} else if !createIfMissing {
		return "", nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) && createIfMissing {
			return configPath, map[string]json.RawMessage{}, nil
		}
		return "", nil, fmt.Errorf("read config: %w", err)
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONC(data), &config); err != nil {
		return "", nil, fmt.Errorf("parse config: %w", err)
	}

	return configPath, config, nil
}

func writeOpenCodeConfig(path string, config map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func decodeRawMap(config map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, exists := config[key]
	if !exists {
		return map[string]json.RawMessage{}, nil
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parse %s block: %w", key, err)
	}

	if decoded == nil {
		return map[string]json.RawMessage{}, nil
	}

	return decoded, nil
}

func marshalRawMap(value map[string]json.RawMessage) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal map: %w", err)
	}

	return json.RawMessage(encoded), nil
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
