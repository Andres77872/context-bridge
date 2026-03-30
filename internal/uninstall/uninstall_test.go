package uninstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPlanDetectsReleaseAndGoInstallBinaries(t *testing.T) {
	fx := newUninstallFixture(t)

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	release := findArtifact(plan.Artifacts, ArtifactBinary, fx.releaseBinary)
	if release == nil {
		t.Fatalf("expected release binary artifact for %q", fx.releaseBinary)
	}
	if !release.Exists {
		t.Fatalf("expected release binary artifact to exist")
	}
	if !release.SharedParent {
		t.Fatalf("expected release binary to preserve shared parent directory")
	}

	staleGo := findArtifact(plan.Artifacts, ArtifactBinary, fx.goBinary)
	if staleGo == nil {
		t.Fatalf("expected stale go-install binary artifact for %q", fx.goBinary)
	}
	if !staleGo.Exists {
		t.Fatalf("expected stale go-install binary artifact to exist")
	}
	if !staleGo.SharedParent {
		t.Fatalf("expected stale go-install binary to preserve shared parent directory")
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("expected no warnings for dedicated uninstall fixture, got %v", plan.Warnings)
	}
	if findArtifact(plan.Artifacts, ArtifactMCPEntry, fx.openCodeConfigPath) == nil {
		t.Fatalf("expected MCP registration artifact for %q", fx.openCodeConfigPath)
	}
}

func TestRunFullModeRemovesFootprintAndPreservesSharedParents(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	err := Run(Options{
		Mode:      ModeFull,
		AssumeYes: true,
		Stdout:    &stdout,
		Stderr:    &stdout,
	})
	if err != nil {
		t.Fatalf("run full uninstall: %v", err)
	}
	if !strings.Contains(stdout.String(), "Uninstall complete (full).") {
		t.Fatalf("expected completion message, got %q", stdout.String())
	}

	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.goBinary)
	assertNotExists(t, fx.pluginPath)
	assertNotExists(t, fx.configDir)
	assertNotExists(t, fx.dataDir)

	assertExists(t, filepath.Dir(fx.releaseBinary))
	assertExists(t, filepath.Dir(fx.goBinary))
	assertExists(t, filepath.Dir(fx.pluginPath))
	assertExists(t, filepath.Dir(fx.openCodeConfigPath))
	assertExists(t, filepath.Dir(fx.configDir))
	assertExists(t, filepath.Dir(fx.dataDir))

	configData, err := os.ReadFile(fx.openCodeConfigPath)
	if err != nil {
		t.Fatalf("read OpenCode config after uninstall: %v", err)
	}
	if strings.Contains(string(configData), "context-bridge") {
		t.Fatalf("expected OpenCode config to remove only the context-bridge MCP entry, got:\n%s", string(configData))
	}
	if !strings.Contains(string(configData), "other") {
		t.Fatalf("expected sibling OpenCode MCP entries to remain, got:\n%s", string(configData))
	}
}

func TestRunPreserveDataKeepsConfigAndData(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	err := Run(Options{
		Mode:      ModePreserveData,
		AssumeYes: true,
		Stdout:    &stdout,
		Stderr:    &stdout,
	})
	if err != nil {
		t.Fatalf("run preserve-data uninstall: %v", err)
	}
	if !strings.Contains(stdout.String(), "Uninstall complete (preserve-data).") {
		t.Fatalf("expected preserve-data completion message, got %q", stdout.String())
	}

	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.goBinary)
	assertNotExists(t, fx.pluginPath)
	assertExists(t, fx.configDir)
	assertExists(t, fx.dataDir)
	assertExists(t, filepath.Join(fx.configDir, "config.json"))
	assertExists(t, filepath.Join(fx.dataDir, "store.db"))

	configData, err := os.ReadFile(fx.openCodeConfigPath)
	if err != nil {
		t.Fatalf("read OpenCode config after preserve-data uninstall: %v", err)
	}
	if strings.Contains(string(configData), "context-bridge") {
		t.Fatalf("expected preserve-data mode to remove the context-bridge MCP entry, got:\n%s", string(configData))
	}
	if !strings.Contains(string(configData), "other") {
		t.Fatalf("expected sibling OpenCode MCP entries to remain, got:\n%s", string(configData))
	}
}

func TestRunInteractiveCancelLeavesArtifactsUntouched(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	err := Run(Options{
		Stdin:  strings.NewReader("2\nno\n"),
		Stdout: &stdout,
		Stderr: &stdout,
	})
	if err != nil {
		t.Fatalf("run interactive cancel: %v", err)
	}
	if !strings.Contains(stdout.String(), "Uninstall canceled.") {
		t.Fatalf("expected cancel message, got %q", stdout.String())
	}

	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.goBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configDir)
	assertExists(t, fx.dataDir)

	configData, err := os.ReadFile(fx.openCodeConfigPath)
	if err != nil {
		t.Fatalf("read OpenCode config after cancel: %v", err)
	}
	if !strings.Contains(string(configData), "context-bridge") {
		t.Fatalf("expected cancel to leave OpenCode MCP registration untouched, got:\n%s", string(configData))
	}
}

func TestPromptBlankChoiceDefaultsToFullRemoval(t *testing.T) {
	plan := Plan{Artifacts: []Artifact{{
		Kind:        ArtifactBinary,
		Path:        "/tmp/context-bridge",
		Exists:      true,
		Description: "release install binary",
	}}}
	var stdout bytes.Buffer

	mode, confirmed, err := Prompt(plan, strings.NewReader("\nyes\n"), &stdout)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if mode != ModeFull {
		t.Fatalf("expected blank choice to default to %q, got %q", ModeFull, mode)
	}
	if !confirmed {
		t.Fatalf("expected typed confirmation to succeed")
	}

	output := stdout.String()
	if !strings.Contains(output, "1) Full removal (default)") {
		t.Fatalf("expected prompt to advertise full removal as default, got %q", output)
	}
	if !strings.Contains(output, "2) Preserve data") {
		t.Fatalf("expected prompt to advertise preserve-data mode, got %q", output)
	}
	if !strings.Contains(output, "Type 'yes' to confirm full uninstall") {
		t.Fatalf("expected prompt to confirm full uninstall after blank choice, got %q", output)
	}
}

func TestRunInteractiveBlankChoiceStillRequiresConfirmation(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	err := Run(Options{
		Stdin:  strings.NewReader("\nno\n"),
		Stdout: &stdout,
		Stderr: &stdout,
	})
	if err != nil {
		t.Fatalf("run interactive default-full cancel: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Type 'yes' to confirm full uninstall") {
		t.Fatalf("expected interactive flow to require explicit yes for full uninstall, got %q", output)
	}
	if !strings.Contains(output, "Uninstall canceled.") {
		t.Fatalf("expected cancel message, got %q", output)
	}

	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.goBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configDir)
	assertExists(t, fx.dataDir)
}

func TestRunFullModeLeavesUnrelatedEnvVarsAndShellSettingsUntouched(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	t.Setenv("UNRELATED_ENV", "keep-me")
	bashrc := filepath.Join(fx.home, ".bashrc")
	zshrc := filepath.Join(fx.home, ".zshrc")
	bashContent := "export KEEP_THIS=1\nalias ll='ls -lah'\n"
	zshContent := "export PATH=\"$HOME/bin:$PATH\"\nsetopt autocd\n"
	writeFixtureFile(t, bashrc, bashContent)
	writeFixtureFile(t, zshrc, zshContent)

	err := Run(Options{
		Mode:      ModeFull,
		AssumeYes: true,
		Stdout:    &stdout,
		Stderr:    &stdout,
	})
	if err != nil {
		t.Fatalf("run full uninstall: %v", err)
	}

	if got := os.Getenv("UNRELATED_ENV"); got != "keep-me" {
		t.Fatalf("expected unrelated env var to remain unchanged, got %q", got)
	}
	assertFileContent(t, bashrc, bashContent)
	assertFileContent(t, zshrc, zshContent)
}

func TestRunFullModePerformsNoShellCleanupWithoutProjectOwnedDefinitions(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer

	profile := filepath.Join(fx.home, ".profile")
	configFish := filepath.Join(fx.home, ".config", "fish", "config.fish")
	profileContent := "export EDITOR=vim\nexport LANG=en_US.UTF-8\n"
	fishContent := "set -gx FZF_DEFAULT_OPTS '--height 40%'\n"
	writeFixtureFile(t, profile, profileContent)
	writeFixtureFile(t, configFish, fishContent)

	err := Run(Options{
		Mode:      ModeFull,
		AssumeYes: true,
		Stdout:    &stdout,
		Stderr:    &stdout,
	})
	if err != nil {
		t.Fatalf("run full uninstall: %v", err)
	}

	assertFileContent(t, profile, profileContent)
	assertFileContent(t, configFish, fishContent)
}

func TestRunFullModeUsesOnlyResolvedOverridePaths(t *testing.T) {
	fx, defaultConfigDir, defaultDataDir := newOverrideUninstallFixture(t)
	var stdout bytes.Buffer

	err := Run(Options{
		Mode:      ModeFull,
		AssumeYes: true,
		Stdout:    &stdout,
		Stderr:    &stdout,
	})
	if err != nil {
		t.Fatalf("run full uninstall with overrides: %v", err)
	}

	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.goBinary)
	assertNotExists(t, fx.pluginPath)
	assertNotExists(t, filepath.Join(fx.configDir, "config.json"))
	assertNotExists(t, filepath.Join(fx.dataDir, "store.db"))
	assertExists(t, fx.configDir)
	assertExists(t, fx.dataDir)
	assertExists(t, defaultConfigDir)
	assertExists(t, defaultDataDir)
	assertExists(t, filepath.Join(defaultConfigDir, "config.json"))
	assertExists(t, filepath.Join(defaultDataDir, "store.db"))
}

func TestRunValidatesExplicitModeAndAssumeYesPairing(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{
			name:    "yes without mode is rejected",
			opts:    Options{AssumeYes: true, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}},
			wantErr: "--yes requires --mode=full or --mode=preserve-data",
		},
		{
			name:    "mode without yes is rejected",
			opts:    Options{Mode: ModeFull, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}},
			wantErr: "--mode is only supported with --yes; interactive uninstall chooses the mode in the confirmation prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newUninstallFixture(t)

			err := Run(tt.opts)
			if err == nil {
				t.Fatalf("expected error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

type uninstallFixture struct {
	home               string
	releaseBinary      string
	goBinary           string
	pluginPath         string
	openCodeConfigPath string
	configDir          string
	dataDir            string
}

func newUninstallFixture(t *testing.T) uninstallFixture {
	t.Helper()

	home := t.TempDir()
	configHome := filepath.Join(home, "xdg-config")
	dataHome := filepath.Join(home, "xdg-data")
	gobin := filepath.Join(home, "gobin")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("GOBIN", gobin)
	t.Setenv("GOPATH", "")
	t.Setenv("INSTALL_DIR", "")
	t.Setenv("XDG_BIN_HOME", "")
	t.Setenv("CONTEXT_BRIDGE_CONFIG", "")
	t.Setenv("CONTEXT_BRIDGE_DB", "")

	fx := uninstallFixture{
		home:               home,
		releaseBinary:      filepath.Join(home, ".local", "bin", "context-bridge"),
		goBinary:           filepath.Join(gobin, "context-bridge"),
		pluginPath:         filepath.Join(configHome, "opencode", "plugins", "context-bridge.ts"),
		openCodeConfigPath: filepath.Join(configHome, "opencode", "opencode.json"),
		configDir:          filepath.Join(configHome, "context-bridge"),
		dataDir:            filepath.Join(dataHome, "context-bridge"),
	}

	writeFixtureFile(t, fx.releaseBinary, "release binary")
	writeFixtureFile(t, fx.goBinary, "stale go binary")
	writeFixtureFile(t, fx.pluginPath, "plugin")
	writeFixtureFile(t, filepath.Join(fx.configDir, "config.json"), `{"search_mode":"regex"}`)
	writeFixtureFile(t, filepath.Join(fx.dataDir, "store.db"), "sqlite")
	writeFixtureFile(t, fx.openCodeConfigPath, `{
		"theme": "dark",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["context-bridge", "mcp"], "enabled": true},
			"other": {"type": "remote", "enabled": false}
		}
	}`)

	return fx
}

func newOverrideUninstallFixture(t *testing.T) (uninstallFixture, string, string) {
	t.Helper()

	home := t.TempDir()
	configHome := filepath.Join(home, "xdg-config-default")
	dataHome := filepath.Join(home, "xdg-data-default")
	gobin := filepath.Join(home, "gobin")
	overrideConfigFile := filepath.Join(home, "override-config", "config.json")
	overrideDBPath := filepath.Join(home, "override-data", "store.db")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("GOBIN", gobin)
	t.Setenv("GOPATH", "")
	t.Setenv("INSTALL_DIR", "")
	t.Setenv("XDG_BIN_HOME", "")
	t.Setenv("CONTEXT_BRIDGE_CONFIG", overrideConfigFile)
	t.Setenv("CONTEXT_BRIDGE_DB", overrideDBPath)

	fx := uninstallFixture{
		home:               home,
		releaseBinary:      filepath.Join(home, ".local", "bin", "context-bridge"),
		goBinary:           filepath.Join(gobin, "context-bridge"),
		pluginPath:         filepath.Join(configHome, "opencode", "plugins", "context-bridge.ts"),
		openCodeConfigPath: filepath.Join(configHome, "opencode", "opencode.json"),
		configDir:          filepath.Dir(overrideConfigFile),
		dataDir:            filepath.Dir(overrideDBPath),
	}

	writeFixtureFile(t, fx.releaseBinary, "release binary")
	writeFixtureFile(t, fx.goBinary, "stale go binary")
	writeFixtureFile(t, fx.pluginPath, "plugin")
	writeFixtureFile(t, filepath.Join(fx.configDir, "config.json"), `{"search_mode":"regex"}`)
	writeFixtureFile(t, filepath.Join(fx.dataDir, "store.db"), "sqlite")
	writeFixtureFile(t, fx.openCodeConfigPath, `{
		"theme": "dark",
		"mcp": {
			"context-bridge": {"type": "local", "command": ["context-bridge", "mcp"], "enabled": true},
			"other": {"type": "remote", "enabled": false}
		}
	}`)

	defaultConfigDir := filepath.Join(configHome, "context-bridge")
	defaultDataDir := filepath.Join(dataHome, "context-bridge")
	writeFixtureFile(t, filepath.Join(defaultConfigDir, "config.json"), `{"search_mode":"fts5"}`)
	writeFixtureFile(t, filepath.Join(defaultDataDir, "store.db"), "stale sqlite")

	return fx, defaultConfigDir, defaultDataDir
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func findArtifact(artifacts []Artifact, kind ArtifactKind, path string) *Artifact {
	for i := range artifacts {
		if artifacts[i].Kind == kind && artifacts[i].Path == normalizePath(path) {
			return &artifacts[i]
		}
	}
	return nil
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %q to exist, stat err=%v", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to be removed, stat err=%v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("expected %q to remain unchanged, got %q", want, string(data))
	}
}
