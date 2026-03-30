package uninstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"context-bridge/internal/config"
	"context-bridge/internal/opencode"
)

type Mode string

const (
	ModeFull         Mode = "full"
	ModePreserveData Mode = "preserve-data"
)

type ArtifactKind string

const (
	ArtifactBinary     ArtifactKind = "binary"
	ArtifactPluginFile ArtifactKind = "plugin-file"
	ArtifactMCPEntry   ArtifactKind = "mcp-entry"
	ArtifactConfigDir  ArtifactKind = "config-dir"
	ArtifactDataDir    ArtifactKind = "data-dir"
)

type Artifact struct {
	Kind         ArtifactKind
	Path         string
	Exists       bool
	SharedParent bool
	Description  string
}

type Plan struct {
	Artifacts []Artifact
	Warnings  []string
}

type Options struct {
	Mode        Mode
	DryRun      bool
	AssumeYes   bool
	ExcludePath string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.TrimSpace(value))
	switch mode {
	case "":
		return "", nil
	case ModeFull, ModePreserveData:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid uninstall mode %q: must be %q or %q", value, ModeFull, ModePreserveData)
	}
}

func Run(opts Options) error {
	mode, err := ParseMode(string(opts.Mode))
	if err != nil {
		return err
	}
	opts.Mode = mode
	opts.Stdin, opts.Stdout, opts.Stderr = normalizeIO(opts.Stdin, opts.Stdout, opts.Stderr)

	plan, err := BuildPlan(opts.ExcludePath)
	if err != nil {
		return err
	}

	if opts.DryRun {
		_, err := fmt.Fprint(opts.Stdout, RenderPlan(plan, opts.Mode))
		return err
	}

	if opts.AssumeYes {
		if opts.Mode == "" {
			return errors.New("--yes requires --mode=full or --mode=preserve-data")
		}
		if err := Execute(plan, opts.Mode); err != nil {
			return err
		}
		_, err := fmt.Fprintf(opts.Stdout, "Uninstall complete (%s).\n", opts.Mode)
		return err
	}

	if opts.Mode != "" {
		return errors.New("--mode is only supported with --yes; interactive uninstall chooses the mode in the confirmation prompt")
	}

	selectedMode, confirmed, err := Prompt(plan, opts.Stdin, opts.Stdout)
	if err != nil {
		return err
	}
	if !confirmed {
		_, err := fmt.Fprintln(opts.Stdout, "Uninstall canceled.")
		return err
	}

	if err := Execute(plan, selectedMode); err != nil {
		return err
	}

	_, err = fmt.Fprintf(opts.Stdout, "Uninstall complete (%s).\n", selectedMode)
	return err
}

func BuildPlan(excludePath string) (Plan, error) {
	plan := Plan{}
	seen := map[string]bool{}
	excludePath = normalizePath(excludePath)

	appendArtifact := func(artifact Artifact) {
		if artifact.Path == "" {
			return
		}
		artifact.Path = normalizePath(artifact.Path)
		key := string(artifact.Kind) + "::" + artifact.Path
		if seen[key] {
			return
		}
		seen[key] = true
		plan.Artifacts = append(plan.Artifacts, artifact)
	}

	if path, ok := installerBinaryPath(); ok {
		if normalizePath(path) == excludePath {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Skipping bootstrap binary at %s.", path))
		} else {
			appendArtifact(Artifact{
				Kind:         ArtifactBinary,
				Path:         path,
				Exists:       pathExists(path),
				SharedParent: true,
				Description:  "release install binary",
			})
		}
	}

	if path, ok := goInstallBinaryPath(); ok {
		if normalizePath(path) == excludePath {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Skipping bootstrap binary at %s.", path))
		} else {
			appendArtifact(Artifact{
				Kind:         ArtifactBinary,
				Path:         path,
				Exists:       pathExists(path),
				SharedParent: true,
				Description:  "stale go-install binary",
			})
		}
	}

	pluginPath, err := opencode.OpenCodePluginPath()
	if err != nil {
		return Plan{}, fmt.Errorf("resolve OpenCode plugin path: %w", err)
	}
	appendArtifact(Artifact{
		Kind:         ArtifactPluginFile,
		Path:         pluginPath,
		Exists:       pathExists(pluginPath),
		SharedParent: true,
		Description:  "OpenCode plugin file",
	})

	mcpPath, mcpExists, err := detectMCPRegistration()
	if err != nil {
		return Plan{}, err
	}
	if mcpPath != "" {
		appendArtifact(Artifact{
			Kind:         ArtifactMCPEntry,
			Path:         mcpPath,
			Exists:       mcpExists,
			SharedParent: true,
			Description:  "OpenCode MCP registration",
		})
	}

	configArtifact, configWarning := configArtifact()
	if configWarning != "" {
		plan.Warnings = append(plan.Warnings, configWarning)
	}
	appendArtifact(configArtifact)

	dataArtifact, dataWarning := dataArtifact()
	if dataWarning != "" {
		plan.Warnings = append(plan.Warnings, dataWarning)
	}
	appendArtifact(dataArtifact)

	return plan, nil
}

func RenderPlan(plan Plan, mode Mode) string {
	var b strings.Builder
	b.WriteString("Uninstall plan\n")
	if mode == "" {
		b.WriteString("Modes: full (default) or preserve-data\n")
	} else {
		fmt.Fprintf(&b, "Mode: %s\n", mode)
	}
	b.WriteString("\nArtifacts in scope:\n")
	for _, artifact := range plan.Artifacts {
		if !artifact.Exists {
			continue
		}
		note := ""
		if artifact.Kind == ArtifactConfigDir || artifact.Kind == ArtifactDataDir {
			if mode == ModePreserveData {
				note = " (preserved in preserve-data mode)"
			} else if mode == "" {
				note = " (full uninstall only)"
			}
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", artifact.Description, artifact.Path, note)
	}
	if len(plan.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, warning := range plan.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	return b.String()
}

func Execute(plan Plan, mode Mode) error {
	if mode != ModeFull && mode != ModePreserveData {
		return fmt.Errorf("execute uninstall: invalid mode %q", mode)
	}

	artifacts := filterArtifacts(plan.Artifacts, mode)
	currentExe := currentExecutablePath()

	var files []Artifact
	var mcpEntries []Artifact
	var dirs []Artifact
	var currentBinary []Artifact

	for _, artifact := range artifacts {
		if !artifact.Exists {
			continue
		}
		switch {
		case artifact.Kind == ArtifactMCPEntry:
			mcpEntries = append(mcpEntries, artifact)
		case artifact.Kind == ArtifactBinary && currentExe != "" && normalizePath(artifact.Path) == currentExe:
			currentBinary = append(currentBinary, artifact)
		case removesDirectory(artifact):
			dirs = append(dirs, artifact)
		default:
			files = append(files, artifact)
		}
	}

	for _, artifact := range files {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}

	for range mcpEntries {
		if _, _, err := opencode.RemoveMCPRegistration(); err != nil {
			return err
		}
	}

	for _, artifact := range dirs {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}

	for _, artifact := range currentBinary {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}

	return nil
}

func filterArtifacts(artifacts []Artifact, mode Mode) []Artifact {
	filtered := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if mode == ModePreserveData && (artifact.Kind == ArtifactConfigDir || artifact.Kind == ArtifactDataDir) {
			continue
		}
		filtered = append(filtered, artifact)
	}
	return filtered
}

func removesDirectory(artifact Artifact) bool {
	return (artifact.Kind == ArtifactConfigDir || artifact.Kind == ArtifactDataDir) && !artifact.SharedParent
}

func removeArtifactPath(artifact Artifact) error {
	if !artifact.Exists {
		return nil
	}
	if removesDirectory(artifact) {
		if err := os.RemoveAll(artifact.Path); err != nil {
			return fmt.Errorf("remove %s %s: %w", artifact.Description, artifact.Path, err)
		}
		return nil
	}
	if err := os.Remove(artifact.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove %s %s: %w", artifact.Description, artifact.Path, err)
	}
	return nil
}

func installerBinaryPath() (string, bool) {
	base := firstEnv("INSTALL_DIR", "XDG_BIN_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, ".local", "bin")
	}
	return filepath.Join(base, "context-bridge"), true
}

func goInstallBinaryPath() (string, bool) {
	base := strings.TrimSpace(os.Getenv("GOBIN"))
	if base == "" {
		gopath := strings.TrimSpace(os.Getenv("GOPATH"))
		if gopath != "" {
			parts := filepath.SplitList(gopath)
			if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
				base = filepath.Join(strings.TrimSpace(parts[0]), "bin")
			}
		}
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		base = filepath.Join(home, "go", "bin")
	}
	return filepath.Join(base, "context-bridge"), true
}

func detectMCPRegistration() (string, bool, error) {
	configDir, err := opencode.OpenCodeConfigDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve OpenCode config dir: %w", err)
	}

	paths := []string{
		filepath.Join(configDir, "opencode.jsonc"),
		filepath.Join(configDir, "opencode.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", false, fmt.Errorf("read OpenCode config %s: %w", path, err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(stripJSONC(data), &raw); err != nil {
			return "", false, fmt.Errorf("parse OpenCode config %s: %w", path, err)
		}

		entry, ok := raw["mcp"]
		if !ok {
			return path, false, nil
		}

		var mcp map[string]json.RawMessage
		if err := json.Unmarshal(entry, &mcp); err != nil {
			return "", false, fmt.Errorf("parse OpenCode mcp block %s: %w", path, err)
		}
		_, exists := mcp["context-bridge"]
		return path, exists, nil
	}

	return "", false, nil
}

func configArtifact() (Artifact, string) {
	configPath := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir)
	if configPath == "" {
		return Artifact{}, ""
	}

	if looksLikeProjectDir(filepath.Dir(configPath)) {
		dir := filepath.Dir(configPath)
		return Artifact{
			Kind:         ArtifactConfigDir,
			Path:         dir,
			Exists:       pathExists(dir),
			SharedParent: false,
			Description:  "context-bridge config directory",
		}, ""
	}

	return Artifact{
		Kind:         ArtifactConfigDir,
		Path:         configPath,
		Exists:       pathExists(configPath),
		SharedParent: true,
		Description:  "resolved config file",
	}, fmt.Sprintf("Resolved config path %s is outside a dedicated context-bridge directory, so full uninstall removes only that file.", configPath)
}

func dataArtifact() (Artifact, string) {
	dbPath := config.ResolveDBPath(os.LookupEnv, os.UserHomeDir)
	if dbPath == "" {
		return Artifact{}, ""
	}

	if looksLikeProjectDir(filepath.Dir(dbPath)) {
		dir := filepath.Dir(dbPath)
		return Artifact{
			Kind:         ArtifactDataDir,
			Path:         dir,
			Exists:       pathExists(dir),
			SharedParent: false,
			Description:  "context-bridge data directory",
		}, ""
	}

	return Artifact{
		Kind:         ArtifactDataDir,
		Path:         dbPath,
		Exists:       pathExists(dbPath),
		SharedParent: true,
		Description:  "resolved data store",
	}, fmt.Sprintf("Resolved data path %s is outside a dedicated context-bridge directory, so full uninstall removes only that file.", dbPath)
}

func normalizeIO(stdin io.Reader, stdout, stderr io.Writer) (io.Reader, io.Writer, io.Writer) {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return stdin, stdout, stderr
}

func currentExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return normalizePath(exe)
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func looksLikeProjectDir(path string) bool {
	return strings.EqualFold(filepath.Base(path), "context-bridge")
}

func stripJSONC(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
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
