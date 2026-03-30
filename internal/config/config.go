package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type SearchMode string

const (
	SearchModeRegex SearchMode = "regex"
	SearchModeFTS5  SearchMode = "fts5"
)

type Config struct {
	SearchMode SearchMode `json:"search_mode"`
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

func ResolveConfigPath(lookupEnv func(string) (string, bool), userConfigDir func() (string, error)) string {
	if path, ok := lookupEnv("CONTEXT_BRIDGE_CONFIG"); ok && path != "" {
		return path
	}

	base, err := userConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "context-bridge", "config.json")
}

func LoadConfig(path string, explicit bool) (Config, error) {
	cfg := DefaultConfig()

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

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
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
