package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type SearchMode string

const (
	SearchModeRegex SearchMode = "regex"
	SearchModeFTS5  SearchMode = "fts5"
)

type Config struct {
	SearchMode SearchMode `json:"search_mode"`
}

// DBLocation records whether Context Bridge owns the database parent
// directory. Explicit CONTEXT_BRIDGE_DB paths are caller-managed: Context
// Bridge validates their parent but never changes its permissions.
type DBLocation struct {
	Path             string
	ManagedDirectory bool
}

func DefaultConfig() Config {
	return Config{SearchMode: SearchModeRegex}
}

func (c Config) Validate() error {
	switch c.SearchMode {
	case SearchModeRegex, SearchModeFTS5:
		return nil
	default:
		return fmt.Errorf("invalid search_mode %q: must be %q or %q", c.SearchMode, SearchModeRegex, SearchModeFTS5)
	}
}

func ResolveConfigPath(lookupEnv func(string) (string, bool), userConfigDir, userHomeDir func() (string, error)) (string, error) {
	if path, ok := nonBlankEnv(lookupEnv, "CONTEXT_BRIDGE_CONFIG"); ok {
		return canonicalAbsolutePath("CONTEXT_BRIDGE_CONFIG", path)
	}

	base, err := resolveUserConfigDir(lookupEnv, userConfigDir, userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "context-bridge", "config.json"), nil
}

func ResolveConfigDir(lookupEnv func(string) (string, bool), userConfigDir, userHomeDir func() (string, error)) (string, error) {
	path, err := ResolveConfigPath(lookupEnv, userConfigDir, userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func ResolveDBPath(lookupEnv func(string) (string, bool), userHomeDir func() (string, error)) (string, error) {
	location, err := ResolveDBLocation(lookupEnv, userHomeDir)
	if err != nil {
		return "", err
	}
	return location.Path, nil
}

func ResolveDBLocation(lookupEnv func(string) (string, bool), userHomeDir func() (string, error)) (DBLocation, error) {
	if path, ok := nonBlankEnv(lookupEnv, "CONTEXT_BRIDGE_DB"); ok {
		resolved, err := canonicalAbsolutePath("CONTEXT_BRIDGE_DB", path)
		if err != nil {
			return DBLocation{}, err
		}
		return DBLocation{Path: resolved}, nil
	}

	var base string
	if path, ok := nonBlankEnv(lookupEnv, "XDG_DATA_HOME"); ok {
		var err error
		base, err = canonicalAbsolutePath("XDG_DATA_HOME", path)
		if err != nil {
			return DBLocation{}, err
		}
	} else {
		home, err := userHomeDir()
		if err != nil {
			return DBLocation{}, fmt.Errorf("resolve user home directory: %w", err)
		}
		base, err = canonicalAbsolutePath("user home directory", home)
		if err != nil {
			return DBLocation{}, err
		}
		base = filepath.Join(base, ".local", "share")
	}

	return DBLocation{
		Path:             filepath.Join(base, "context-bridge", "store.db"),
		ManagedDirectory: true,
	}, nil
}

func ResolveDataDir(lookupEnv func(string) (string, bool), userHomeDir func() (string, error)) (string, error) {
	path, err := ResolveDBPath(lookupEnv, userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func ResolveSocketPath(lookupEnv func(string) (string, bool), userConfigDir, userHomeDir func() (string, error)) (string, error) {
	if path, ok := nonBlankEnv(lookupEnv, "CONTEXT_BRIDGE_SOCKET"); ok {
		return canonicalAbsolutePath("CONTEXT_BRIDGE_SOCKET", path)
	}
	base, err := resolveUserConfigDir(lookupEnv, userConfigDir, userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "context-bridge", "bridge.sock"), nil
}

func resolveUserConfigDir(lookupEnv func(string) (string, bool), userConfigDir, userHomeDir func() (string, error)) (string, error) {
	// os.UserConfigDir follows XDG_CONFIG_HOME on Linux, but inspecting it here
	// lets us trim and reject a relative value before any caller can reinterpret
	// it against its own working directory. macOS keeps its native OS default.
	if runtime.GOOS == "linux" {
		if raw, ok := lookupEnv("XDG_CONFIG_HOME"); ok {
			path := strings.TrimSpace(raw)
			if path != "" {
				return canonicalAbsolutePath("XDG_CONFIG_HOME", path)
			}
			if raw != "" {
				if userHomeDir == nil {
					return "", errors.New("resolve user home directory for blank XDG_CONFIG_HOME: resolver is unavailable")
				}
				home, err := userHomeDir()
				if err != nil {
					return "", fmt.Errorf("resolve user home directory for blank XDG_CONFIG_HOME: %w", err)
				}
				home, err = canonicalAbsolutePath("user home directory", home)
				if err != nil {
					return "", err
				}
				return filepath.Join(home, ".config"), nil
			}
		}
	}
	base, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return canonicalAbsolutePath("user config directory", base)
}

func nonBlankEnv(lookupEnv func(string) (string, bool), name string) (string, bool) {
	value, ok := lookupEnv(name)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func canonicalAbsolutePath(name, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s path is empty", name)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path: %q", name, path)
	}
	return filepath.Clean(path), nil
}

func LoadConfig(path string, explicit bool) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		if explicit {
			return Config{}, errors.New("explicit config path is empty")
		}
		return cfg, nil
	}
	if !filepath.IsAbs(path) {
		return Config{}, fmt.Errorf("config path must be absolute: %s", path)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Config{}, fmt.Errorf("config file %s must be a regular file, not a symlink", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("inspect config %s: %w", path, err)
	}

	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if explicit {
			return Config{}, fmt.Errorf("config file %s does not exist", path)
		}
		return cfg, nil
	case err != nil:
		return Config{}, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode config %s: trailing JSON data is not allowed", path)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	if path == "" {
		return errors.New("config path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("config path must be absolute: %s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return fmt.Errorf("config directory %s must be a real directory, not a symlink", dir)
	}
	if targetInfo, err := os.Lstat(path); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("config file %s must be a regular file, not a symlink", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect config file: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	f, err := os.CreateTemp(dir, ".context-bridge-config-*")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temp config file: %w", err)
	}

	_, err = f.Write(data)
	if err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp config file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp config file: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("secure config file: %w", err)
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open config directory for sync: %w", err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}

	return nil
}

func UpdateConfig(path string, explicit bool, updateFn func(Config) Config) error {
	cfg, err := LoadConfig(path, explicit)
	if err != nil {
		return err
	}

	updated := updateFn(cfg)
	return SaveConfig(path, updated)
}

func (m SearchMode) Description() string {
	switch m {
	case SearchModeRegex:
		return "Search across all captured outputs for the current session context using Regex."
	case SearchModeFTS5:
		return "Search across all captured outputs for the current session context using FTS5 full-text search."
	default:
		return "Search across all captured outputs for the current session context."
	}
}
