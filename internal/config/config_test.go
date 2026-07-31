package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	path := mustResolveConfigPath(t, func(key string) (string, bool) {
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
	path := mustResolveConfigPath(t, func(string) (string, bool) { return "", false }, func() (string, error) {
		return "/home/user/.config", nil
	})

	expected := filepath.Join("/home/user/.config", "context-bridge", "config.json")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestResolveConfigDirUsesConfigPathOverrideDirectory(t *testing.T) {
	dir := mustResolveConfigDir(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_CONFIG" {
			return "/custom/path/config.json", true
		}
		return "", false
	}, nil)

	if dir != "/custom/path" {
		t.Fatalf("expected override directory %q, got %q", "/custom/path", dir)
	}
}

func TestResolveConfigDirUsesUserConfigDir(t *testing.T) {
	dir := mustResolveConfigDir(t, func(string) (string, bool) { return "", false }, func() (string, error) {
		return "/home/user/.config", nil
	})

	expected := filepath.Join("/home/user/.config", "context-bridge")
	if dir != expected {
		t.Fatalf("expected %q, got %q", expected, dir)
	}
}

func TestResolveDBPathUsesEnvVar(t *testing.T) {
	path := mustResolveDBPath(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_DB" {
			return "/custom/data/store.db", true
		}
		return "", false
	}, nil)

	if path != "/custom/data/store.db" {
		t.Fatalf("expected env override path, got %q", path)
	}
}

func TestResolveDBPathUsesXDGDataHome(t *testing.T) {
	path := mustResolveDBPath(t, func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/xdg/data", true
		}
		return "", false
	}, nil)

	expected := filepath.Join("/xdg/data", "context-bridge", "store.db")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestResolveDBPathFailsWhenHomeUnavailable(t *testing.T) {
	path, err := ResolveDBPath(func(string) (string, bool) { return "", false }, func() (string, error) {
		return "", os.ErrNotExist
	})
	if err == nil || path != "" {
		t.Fatalf("expected missing home to fail closed, path=%q err=%v", path, err)
	}
}

func TestPathResolversTrimAndCanonicalizeAbsoluteValues(t *testing.T) {
	root := t.TempDir()
	configPath := mustResolveConfigPath(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_CONFIG" {
			return "  " + filepath.Join(root, "nested", "..", "config.json") + "  ", true
		}
		return "", false
	}, nil)
	if want := filepath.Join(root, "config.json"); configPath != want {
		t.Fatalf("expected canonical config path %q, got %q", want, configPath)
	}

	dbPath := mustResolveDBPath(t, func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "  " + filepath.Join(root, "data", "..", "xdg-data") + "  ", true
		}
		return "", false
	}, nil)
	if want := filepath.Join(root, "xdg-data", "context-bridge", "store.db"); dbPath != want {
		t.Fatalf("expected canonical DB path %q, got %q", want, dbPath)
	}
}

func TestWhitespaceOnlyXDGValuesUseLinuxDefaults(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG config defaults are a Linux contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "  \t  ")
	t.Setenv("XDG_DATA_HOME", "  \t  ")
	t.Setenv("CONTEXT_BRIDGE_CONFIG", "  ")
	t.Setenv("CONTEXT_BRIDGE_DB", "  ")
	t.Setenv("CONTEXT_BRIDGE_SOCKET", "  ")

	configPath := mustResolveConfigPath(t, os.LookupEnv, os.UserConfigDir)
	if want := filepath.Join(home, ".config", "context-bridge", "config.json"); configPath != want {
		t.Fatalf("expected whitespace-only XDG_CONFIG_HOME to use %q, got %q", want, configPath)
	}
	socketPath := mustResolveSocketPath(t, os.LookupEnv, os.UserConfigDir)
	if want := filepath.Join(home, ".config", "context-bridge", "bridge.sock"); socketPath != want {
		t.Fatalf("expected whitespace-only XDG_CONFIG_HOME socket %q, got %q", want, socketPath)
	}
	dbPath := mustResolveDBPath(t, os.LookupEnv, os.UserHomeDir)
	if want := filepath.Join(home, ".local", "share", "context-bridge", "store.db"); dbPath != want {
		t.Fatalf("expected whitespace-only XDG_DATA_HOME to use %q, got %q", want, dbPath)
	}
}

func TestPathResolversRejectRelativeInputs(t *testing.T) {
	noEnv := func(string) (string, bool) { return "", false }
	relativeConfigDir := func() (string, error) { return "relative-config", nil }
	relativeHomeDir := func() (string, error) { return "relative-home", nil }
	tests := []struct {
		name    string
		resolve func() (string, error)
	}{
		{
			name: "config override",
			resolve: func() (string, error) {
				return ResolveConfigPath(func(key string) (string, bool) {
					return "relative/config.json", key == "CONTEXT_BRIDGE_CONFIG"
				}, nil, nil)
			},
		},
		{
			name: "database override",
			resolve: func() (string, error) {
				return ResolveDBPath(func(key string) (string, bool) {
					return "relative/store.db", key == "CONTEXT_BRIDGE_DB"
				}, nil)
			},
		},
		{
			name: "XDG data home",
			resolve: func() (string, error) {
				return ResolveDBPath(func(key string) (string, bool) {
					return "relative-data", key == "XDG_DATA_HOME"
				}, nil)
			},
		},
		{
			name: "socket override",
			resolve: func() (string, error) {
				return ResolveSocketPath(func(key string) (string, bool) {
					return "relative.sock", key == "CONTEXT_BRIDGE_SOCKET"
				}, nil, nil)
			},
		},
		{name: "user config directory", resolve: func() (string, error) {
			return ResolveConfigPath(noEnv, relativeConfigDir, nil)
		}},
		{name: "user home directory", resolve: func() (string, error) {
			return ResolveDBPath(noEnv, relativeHomeDir)
		}},
	}
	if runtime.GOOS == "linux" {
		tests = append(tests, struct {
			name    string
			resolve func() (string, error)
		}{
			name: "XDG config home",
			resolve: func() (string, error) {
				return ResolveConfigPath(func(key string) (string, bool) {
					return "relative-config", key == "XDG_CONFIG_HOME"
				}, nil, nil)
			},
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := tt.resolve()
			if err == nil || path != "" {
				t.Fatalf("expected relative path to fail closed, path=%q err=%v", path, err)
			}
		})
	}
}

func TestResolveSocketPathUsesPrivateConfigNamespace(t *testing.T) {
	base := t.TempDir()
	path := mustResolveSocketPath(t, func(string) (string, bool) { return "", false }, func() (string, error) {
		return base, nil
	})
	if want := filepath.Join(base, "context-bridge", "bridge.sock"); path != want {
		t.Fatalf("expected socket path %q, got %q", want, path)
	}

	override := filepath.Join(base, "custom.sock")
	path = mustResolveSocketPath(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_SOCKET" {
			return override, true
		}
		return "", false
	}, func() (string, error) { return base, nil })
	if path != override {
		t.Fatalf("expected socket override %q, got %q", override, path)
	}
}

func TestResolveSocketPathIgnoresBlankOverride(t *testing.T) {
	base := t.TempDir()
	path := mustResolveSocketPath(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_SOCKET" {
			return "  \t", true
		}
		return "", false
	}, func() (string, error) { return base, nil })

	want := filepath.Join(base, "context-bridge", "bridge.sock")
	if path != want {
		t.Fatalf("expected blank socket override to resolve to %q, got %q", want, path)
	}
}

func TestResolveDataDirUsesDBOverrideDirectory(t *testing.T) {
	dir := mustResolveDataDir(t, func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_DB" {
			return "/custom/data/store.db", true
		}
		return "", false
	}, nil)

	if dir != "/custom/data" {
		t.Fatalf("expected override directory %q, got %q", "/custom/data", dir)
	}
}

func TestResolveDataDirUsesXDGDataHome(t *testing.T) {
	dir := mustResolveDataDir(t, func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/xdg/data", true
		}
		return "", false
	}, nil)

	expected := filepath.Join("/xdg/data", "context-bridge")
	if dir != expected {
		t.Fatalf("expected %q, got %q", expected, dir)
	}
}

func TestResolveDataDirFallsBackToUserHome(t *testing.T) {
	dir := mustResolveDataDir(t, func(string) (string, bool) { return "", false }, func() (string, error) {
		return "/home/user", nil
	})

	expected := filepath.Join("/home/user", ".local", "share", "context-bridge")
	if dir != expected {
		t.Fatalf("expected %q, got %q", expected, dir)
	}
}

func TestResolveUninstallScopeUsesOnlyExplicitOverridePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config-default"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share-default"))
	t.Setenv("CONTEXT_BRIDGE_CONFIG", filepath.Join(home, "override-config", "config.json"))
	t.Setenv("CONTEXT_BRIDGE_DB", filepath.Join(home, "override-data", "store.db"))

	gotConfigDir := mustResolveConfigDir(t, os.LookupEnv, os.UserConfigDir)
	gotDataDir := mustResolveDataDir(t, os.LookupEnv, os.UserHomeDir)

	wantConfigDir := filepath.Join(home, "override-config")
	wantDataDir := filepath.Join(home, "override-data")
	defaultConfigDir := filepath.Join(home, ".config-default", "context-bridge")
	defaultDataDir := filepath.Join(home, ".local", "share-default", "context-bridge")

	if gotConfigDir != wantConfigDir {
		t.Fatalf("expected resolved config dir %q, got %q", wantConfigDir, gotConfigDir)
	}
	if gotDataDir != wantDataDir {
		t.Fatalf("expected resolved data dir %q, got %q", wantDataDir, gotDataDir)
	}
	if gotConfigDir == defaultConfigDir {
		t.Fatalf("resolved config dir should not fall back to default XDG target %q", defaultConfigDir)
	}
	if gotDataDir == defaultDataDir {
		t.Fatalf("resolved data dir should not fall back to default XDG target %q", defaultDataDir)
	}
}

func TestResolveUninstallScopeUsesOnlyXDGOverridePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config-override"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data-override"))
	t.Setenv("CONTEXT_BRIDGE_CONFIG", "")
	t.Setenv("CONTEXT_BRIDGE_DB", "")

	gotConfigDir := mustResolveConfigDir(t, os.LookupEnv, os.UserConfigDir)
	gotDataDir := mustResolveDataDir(t, os.LookupEnv, os.UserHomeDir)

	wantConfigDir := filepath.Join(home, "xdg-config-override", "context-bridge")
	if runtime.GOOS == "darwin" {
		wantConfigDir = filepath.Join(home, "Library", "Application Support", "context-bridge")
	}
	wantDataDir := filepath.Join(home, "xdg-data-override", "context-bridge")
	defaultConfigDir := filepath.Join(home, ".config", "context-bridge")
	defaultDataDir := filepath.Join(home, ".local", "share", "context-bridge")

	if gotConfigDir != wantConfigDir {
		t.Fatalf("expected resolved config dir %q, got %q", wantConfigDir, gotConfigDir)
	}
	if gotDataDir != wantDataDir {
		t.Fatalf("expected resolved data dir %q, got %q", wantDataDir, gotDataDir)
	}
	if gotConfigDir == defaultConfigDir {
		t.Fatalf("resolved config dir should not add default XDG cleanup target %q", defaultConfigDir)
	}
	if gotDataDir == defaultDataDir {
		t.Fatalf("resolved data dir should not add default XDG cleanup target %q", defaultDataDir)
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

// TestSearchModePersistence verifies search_mode persists across save/load cycles.
func TestSearchModePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Test FTS5 mode persistence
	fts5Cfg := Config{SearchMode: SearchModeFTS5}
	if err := SaveConfig(path, fts5Cfg); err != nil {
		t.Fatalf("save FTS5 config: %v", err)
	}

	loadedFTS5, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load FTS5 config: %v", err)
	}
	if loadedFTS5.SearchMode != SearchModeFTS5 {
		t.Fatalf("FTS5 mode not persisted: expected %q, got %q", SearchModeFTS5, loadedFTS5.SearchMode)
	}

	// Test regex mode persistence
	regexCfg := Config{SearchMode: SearchModeRegex}
	if err := SaveConfig(path, regexCfg); err != nil {
		t.Fatalf("save regex config: %v", err)
	}

	loadedRegex, err := LoadConfig(path, true)
	if err != nil {
		t.Fatalf("load regex config: %v", err)
	}
	if loadedRegex.SearchMode != SearchModeRegex {
		t.Fatalf("regex mode not persisted: expected %q, got %q", SearchModeRegex, loadedRegex.SearchMode)
	}
}

// TestSearchModePreservedOnOtherConfigChanges verifies search_mode isn't lost
// when other config values change (simulates user changing unrelated settings).
func TestSearchModePreservedOnOtherConfigChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Start with FTS5 mode
	original := Config{SearchMode: SearchModeFTS5}
	if err := SaveConfig(path, original); err != nil {
		t.Fatalf("save original: %v", err)
	}

	// "Modify another config value" - currently only search_mode exists,
	// but this test ensures the pattern is correct for future config additions
	err := UpdateConfig(path, true, func(cfg Config) Config {
		// Keep search_mode unchanged
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
		t.Fatalf("search_mode should be preserved: expected %q, got %q", SearchModeFTS5, loaded.SearchMode)
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

// TestSaveConfigRawJSONContainsSearchMode verifies the saved config file
// contains the literal JSON field "search_mode" with correct value.
// This provides raw file evidence for verification.
func TestSaveConfigRawJSONContainsSearchMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      SearchMode
		wantField string
	}{
		{
			name:      "FTS5 mode config",
			mode:      SearchModeFTS5,
			wantField: `"search_mode": "fts5"`,
		},
		{
			name:      "Regex mode config",
			mode:      SearchModeRegex,
			wantField: `"search_mode": "regex"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			cfg := Config{SearchMode: tt.mode}

			if err := SaveConfig(path, cfg); err != nil {
				t.Fatalf("save config: %v", err)
			}

			// Read raw file bytes
			rawContent, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config file: %v", err)
			}

			rawJSON := string(rawContent)

			// Verify the JSON contains the search_mode field with correct value
			if !strings.Contains(rawJSON, tt.wantField) {
				t.Errorf("saved config JSON should contain %q, got:\n%s", tt.wantField, rawJSON)
			}

			// Verify it's valid JSON structure
			if !strings.Contains(rawJSON, "{") || !strings.Contains(rawJSON, "}") {
				t.Errorf("saved config should be valid JSON object, got:\n%s", rawJSON)
			}
		})
	}
}

// TestSaveConfigJSONFormatIsCorrect verifies the JSON structure matches expected format.
func TestSaveConfigJSONFormatIsCorrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Config{SearchMode: SearchModeFTS5}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	rawContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	// Expected JSON format with indentation
	expectedFormat := `{
  "search_mode": "fts5"
}`
	// Normalize whitespace for comparison
	rawJSON := strings.TrimSpace(string(rawContent))
	expectedNorm := strings.TrimSpace(expectedFormat)

	if rawJSON != expectedNorm {
		t.Errorf("JSON format mismatch.\nExpected:\n%s\nGot:\n%s", expectedNorm, rawJSON)
	}
}

func TestResolveDBLocationMarksDefaultDirectoryManaged(t *testing.T) {
	home := t.TempDir()
	location, err := ResolveDBLocation(func(string) (string, bool) { return "", false }, func() (string, error) {
		return home, nil
	})
	if err != nil {
		t.Fatalf("ResolveDBLocation: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "context-bridge", "store.db"); location.Path != want {
		t.Fatalf("expected default database path %q, got %q", want, location.Path)
	}
	if !location.ManagedDirectory {
		t.Fatal("expected default database directory to be managed")
	}
}

func TestResolveDBLocationKeepsExplicitDirectoryCallerManaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	location, err := ResolveDBLocation(func(key string) (string, bool) {
		if key == "CONTEXT_BRIDGE_DB" {
			return path, true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatalf("ResolveDBLocation: %v", err)
	}
	if location.Path != path {
		t.Fatalf("expected explicit database path %q, got %q", path, location.Path)
	}
	if location.ManagedDirectory {
		t.Fatal("expected explicit database directory to remain caller-managed")
	}
}

func TestLoadConfigRejectsRelativePath(t *testing.T) {
	if _, err := LoadConfig("config.json", false); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected relative path rejection, got %v", err)
	}
}

func TestLoadConfigRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"search_mode":"regex"} {"search_mode":"fts5"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(path, true); err == nil || !strings.Contains(err.Error(), "trailing JSON data") {
		t.Fatalf("expected trailing JSON rejection, got %v", err)
	}
}

func TestSaveConfigRejectsRelativePath(t *testing.T) {
	if err := SaveConfig("config.json", DefaultConfig()); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected relative path rejection, got %v", err)
	}
}

func TestSaveConfigRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveConfig(link, DefaultConfig()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "unchanged" {
		t.Fatalf("symlink target changed: content=%q err=%v", content, err)
	}
}

func mustResolveConfigPath(t *testing.T, lookupEnv func(string) (string, bool), userConfigDir func() (string, error)) string {
	t.Helper()
	path, err := ResolveConfigPath(lookupEnv, userConfigDir, os.UserHomeDir)
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	return path
}

func mustResolveConfigDir(t *testing.T, lookupEnv func(string) (string, bool), userConfigDir func() (string, error)) string {
	t.Helper()
	path, err := ResolveConfigDir(lookupEnv, userConfigDir, os.UserHomeDir)
	if err != nil {
		t.Fatalf("resolve config directory: %v", err)
	}
	return path
}

func mustResolveDBPath(t *testing.T, lookupEnv func(string) (string, bool), userHomeDir func() (string, error)) string {
	t.Helper()
	path, err := ResolveDBPath(lookupEnv, userHomeDir)
	if err != nil {
		t.Fatalf("resolve database path: %v", err)
	}
	return path
}

func mustResolveDataDir(t *testing.T, lookupEnv func(string) (string, bool), userHomeDir func() (string, error)) string {
	t.Helper()
	path, err := ResolveDataDir(lookupEnv, userHomeDir)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	return path
}

func mustResolveSocketPath(t *testing.T, lookupEnv func(string) (string, bool), userConfigDir func() (string, error)) string {
	t.Helper()
	path, err := ResolveSocketPath(lookupEnv, userConfigDir, os.UserHomeDir)
	if err != nil {
		t.Fatalf("resolve socket path: %v", err)
	}
	return path
}
