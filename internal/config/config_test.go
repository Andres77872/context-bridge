package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SearchMode != SearchModeRegex {
		t.Fatalf("expected default search_mode %q, got %q", SearchModeRegex, cfg.SearchMode)
	}
}

func TestValidateAcceptsValidModes(t *testing.T) {
	for _, mode := range []SearchMode{SearchModeRegex, SearchModeFTS5} {
		cfg := Config{SearchMode: mode}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("mode %q should be valid, got error: %v", mode, err)
		}
	}
}

func TestValidateRejectsInvalidMode(t *testing.T) {
	cfg := Config{SearchMode: "ripgrep"}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("invalid mode should fail validation")
	}
}

func TestResolveConfigPathUsesEnvVar(t *testing.T) {
	path := ResolveConfigPath(func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_CONFIG" {
			return "/custom/path/config.json", true
		}
		return "", false
	}, nil)

	if path != "/custom/path/config.json" {
		t.Fatalf("expected env override path, got %q", path)
	}
}

func TestResolveConfigPathUsesUserConfigDir(t *testing.T) {
	path := ResolveConfigPath(func(string) (string, bool) { return "", false }, func() (string, error) {
		return "/home/user/.config", nil
	})

	expected := filepath.Join("/home/user/.config", "context-bridge", "config.json")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestLoadConfigReturnsDefaultsWhenFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	cfg, err := LoadConfig(path, false)
	if err != nil {
		t.Fatalf("missing default-path file should return defaults, got error: %v", err)
	}
	if cfg.SearchMode != SearchModeRegex {
		t.Fatalf("expected default mode, got %q", cfg.SearchMode)
	}
}

func TestLoadConfigErrorsWhenExplicitPathMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	_, err := LoadConfig(path, true)
	if err == nil {
		t.Fatalf("explicit missing file should error")
	}
}

func TestLoadConfigParsesValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{"search_mode":"fts5"}`)

	cfg, err := LoadConfig(path, false)
	if err != nil {
		t.Fatalf("valid config should load, got error: %v", err)
	}
	if cfg.SearchMode != SearchModeFTS5 {
		t.Fatalf("expected mode %q, got %q", SearchModeFTS5, cfg.SearchMode)
	}
}

func TestLoadConfigErrorsOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{invalid json}`)

	_, err := LoadConfig(path, false)
	if err == nil {
		t.Fatalf("invalid JSON should error")
	}
}

func TestLoadConfigErrorsOnUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{"search_mode":"regex","unknown_key":"value"}`)

	_, err := LoadConfig(path, false)
	if err == nil {
		t.Fatalf("unknown key should error")
	}
}

func TestLoadConfigErrorsOnInvalidMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{"search_mode":"ripgrep"}`)

	_, err := LoadConfig(path, false)
	if err == nil {
		t.Fatalf("invalid mode should error")
	}
}

func TestLoadConfigUsesDefaultModeWhenKeyMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{}`)

	cfg, err := LoadConfig(path, false)
	if err != nil {
		t.Fatalf("empty config should load defaults, got error: %v", err)
	}
	if cfg.SearchMode != SearchModeRegex {
		t.Fatalf("expected default mode %q, got %q", SearchModeRegex, cfg.SearchMode)
	}
}

func TestSearchModeDescription(t *testing.T) {
	tests := []struct {
		mode     SearchMode
		contains string
	}{
		{SearchModeRegex, "Regex"},
		{SearchModeFTS5, "FTS5"},
		{"unknown", "Search"},
	}

	for _, tt := range tests {
		desc := tt.mode.Description()
		if !containsStr(desc, tt.contains) {
			t.Fatalf("mode %q description should contain %q, got %q", tt.mode, tt.contains, desc)
		}
	}
}

func TestSaveConfigWritesValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{SearchMode: SearchModeFTS5}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config should succeed, got error: %v", err)
	}

	loaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load saved config should succeed, got error: %v", err)
	}
	if loaded.SearchMode != SearchModeFTS5 {
		t.Fatalf("expected mode %q, got %q", SearchModeFTS5, loaded.SearchMode)
	}
}

func TestSaveConfigCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "dir", "config.json")
	cfg := Config{SearchMode: SearchModeRegex}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config should create directories, got error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file should exist, got error: %v", err)
	}
}

func TestSaveConfigRejectsInvalidMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{SearchMode: "ripgrep"}

	err := SaveConfig(path, cfg)
	if err == nil {
		t.Fatalf("save should reject invalid mode")
	}
}

func TestSaveConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := Config{SearchMode: SearchModeFTS5}
	if err := SaveConfig(path, original); err != nil {
		t.Fatalf("save original: %v", err)
	}

	loaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load original: %v", err)
	}

	if err := SaveConfig(path, loaded); err != nil {
		t.Fatalf("save round-trip: %v", err)
	}

	loaded2, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load round-trip: %v", err)
	}

	if loaded2.SearchMode != original.SearchMode {
		t.Fatalf("round-trip corrupted config: expected %q, got %q", original.SearchMode, loaded2.SearchMode)
	}
}

func TestSaveConfigFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{SearchMode: SearchModeRegex}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected file permissions 0600, got %04o", info.Mode().Perm())
	}
}

func TestUpdateConfigModifiesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := Config{SearchMode: SearchModeRegex}
	if err := SaveConfig(path, original); err != nil {
		t.Fatalf("save original: %v", err)
	}

	err := UpdateConfig(path, true, func(cfg Config) Config {
		cfg.SearchMode = SearchModeFTS5
		return cfg
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	loaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load updated: %v", err)
	}
	if loaded.SearchMode != SearchModeFTS5 {
		t.Fatalf("expected updated mode %q, got %q", SearchModeFTS5, loaded.SearchMode)
	}
}

func TestUpdateConfigCreatesIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	err := UpdateConfig(path, false, func(cfg Config) Config {
		cfg.SearchMode = SearchModeFTS5
		return cfg
	})
	if err != nil {
		t.Fatalf("update config on missing file: %v", err)
	}

	loaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load created: %v", err)
	}
	if loaded.SearchMode != SearchModeFTS5 {
		t.Fatalf("expected mode %q, got %q", SearchModeFTS5, loaded.SearchMode)
	}
}

func TestUpdateConfigRejectsInvalidUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := Config{SearchMode: SearchModeRegex}
	if err := SaveConfig(path, original); err != nil {
		t.Fatalf("save original: %v", err)
	}

	err := UpdateConfig(path, true, func(cfg Config) Config {
		cfg.SearchMode = "invalid"
		return cfg
	})
	if err == nil {
		t.Fatalf("update should reject invalid mode")
	}

	loaded, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load after failed update: %v", err)
	}
	if loaded.SearchMode != SearchModeRegex {
		t.Fatalf("original file should be unchanged after failed update")
	}
}

func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
