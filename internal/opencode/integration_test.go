package opencode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallAndRemovePluginPreserveOpenCodeConfig(t *testing.T) {
	configHome := setTestUserConfigHome(t, t.TempDir())
	binaryPath := writeTestBinary(t)

	openCodeConfig := filepath.Join(configHome, "opencode", "opencode.json")
	originalConfig := []byte("{\n  // keep comments and secrets byte-for-byte\n  \"mcp\": {\"context-bridge\": {\"enabled\": true}}\n}\n")
	writeTestFile(t, openCodeConfig, originalConfig)

	pluginPath, err := InstallOwnedPlugin(binaryPath)
	if err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	expectedPath := filepath.Join(configHome, "opencode", "plugins", "context-bridge.ts")
	if pluginPath != expectedPath {
		t.Fatalf("expected plugin path %q, got %q", expectedPath, pluginPath)
	}

	status, err := InspectIntegration()
	if err != nil {
		t.Fatalf("inspect installed integration: %v", err)
	}
	if status.State != StateOwnedCurrent {
		t.Fatalf("expected owned-current state, got %+v", status)
	}
	if status.BinaryPath != binaryPath || status.BinarySHA256 == "" {
		t.Fatalf("expected binary ownership to be recorded, got %+v", status)
	}
	manifestInfo, err := os.Stat(status.ManifestPath)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected manifest mode 0600, got %o", manifestInfo.Mode().Perm())
	}

	assertFileBytes(t, openCodeConfig, originalConfig)
	removed, removedPath, err := RemovePlugin()
	if err != nil {
		t.Fatalf("remove plugin: %v", err)
	}
	if !removed || removedPath != pluginPath {
		t.Fatalf("unexpected removal result: removed=%v path=%q", removed, removedPath)
	}
	assertMissing(t, pluginPath)
	assertMissing(t, status.ManifestPath)
	assertFileBytes(t, openCodeConfig, originalConfig)
}

func TestOpenCodePathsTrimAndCanonicalizeXDGConfigHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG config paths are a Linux contract")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "  "+filepath.Join(root, "nested", "..", "config")+"  ")

	configDir, err := OpenCodeConfigDir()
	if err != nil {
		t.Fatalf("resolve OpenCode config directory: %v", err)
	}
	if want := filepath.Join(root, "config", "opencode"); configDir != want {
		t.Fatalf("expected canonical OpenCode config directory %q, got %q", want, configDir)
	}
	manifestPath, err := IntegrationManifestPath()
	if err != nil {
		t.Fatalf("resolve integration manifest path: %v", err)
	}
	if want := filepath.Join(root, "config", "context-bridge", "opencode-integration.json"); manifestPath != want {
		t.Fatalf("expected canonical manifest path %q, got %q", want, manifestPath)
	}
}

func TestOpenCodePathsTreatWhitespaceOnlyXDGConfigHomeAsUnset(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG config defaults are a Linux contract")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "  \t  ")

	configDir, err := OpenCodeConfigDir()
	if err != nil {
		t.Fatalf("resolve OpenCode config directory: %v", err)
	}
	if want := filepath.Join(home, ".config", "opencode"); configDir != want {
		t.Fatalf("expected whitespace-only XDG config home to use %q, got %q", want, configDir)
	}
	manifestPath, err := IntegrationManifestPath()
	if err != nil {
		t.Fatalf("resolve integration manifest path: %v", err)
	}
	if want := filepath.Join(home, ".config", "context-bridge", "opencode-integration.json"); manifestPath != want {
		t.Fatalf("expected whitespace-only XDG manifest path %q, got %q", want, manifestPath)
	}
}

func TestOpenCodePathsRejectRelativeXDGConfigHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG config paths are a Linux contract")
	}
	t.Setenv("XDG_CONFIG_HOME", " relative-config ")

	if path, err := OpenCodeConfigDir(); err == nil || path != "" {
		t.Fatalf("expected relative XDG config home to fail closed, path=%q err=%v", path, err)
	}
	if path, err := IntegrationManifestPath(); err == nil || path != "" {
		t.Fatalf("expected relative XDG config home to fail manifest resolution, path=%q err=%v", path, err)
	}
	if path, err := cleanAbsolutePath("relative/plugin.ts"); err == nil || path != "" {
		t.Fatalf("expected relative path to be rejected instead of cwd-expanded, path=%q err=%v", path, err)
	}
}

func TestInstallRefusesForeignPluginAndLeavesItUntouched(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	foreign := []byte("export default { owner: 'someone-else' };\n")
	writeTestFile(t, pluginPath, foreign)

	if _, err := InstallPlugin(writeTestBinary(t)); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("expected foreign plugin conflict, got %v", err)
	}
	if _, _, err := RemovePlugin(); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("expected foreign plugin removal refusal, got %v", err)
	}
	assertFileBytes(t, pluginPath, foreign)
}

func TestInstallRefusesSymlinkPlugin(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	target := filepath.Join(t.TempDir(), "foreign.ts")
	foreign := []byte("foreign\n")
	writeTestFile(t, target, foreign)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatalf("create plugin dir: %v", err)
	}
	if err := os.Symlink(target, pluginPath); err != nil {
		t.Fatalf("create plugin symlink: %v", err)
	}

	if _, err := InstallPlugin(writeTestBinary(t)); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	assertFileBytes(t, target, foreign)
}

func TestModifiedOwnedPluginCannotBeOverwrittenOrRemoved(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	pluginPath, err := InstallPlugin(writeTestBinary(t))
	if err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	modified := []byte("user modification\n")
	writeTestFile(t, pluginPath, modified)

	if _, err := InstallPlugin(writeTestBinary(t)); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected modified plugin install refusal, got %v", err)
	}
	if _, _, err := RemovePlugin(); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected modified plugin removal refusal, got %v", err)
	}
	assertFileBytes(t, pluginPath, modified)
}

func TestRecognizedUnmanagedAdapterIsAdopted(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	legacy, err := os.ReadFile(filepath.Join("testdata", "legacy-context-bridge.ts"))
	if err != nil {
		t.Fatalf("read legacy plugin fixture: %v", err)
	}
	normalizedSHA, recognized := normalizedPluginSHA256(legacy)
	if !recognized || normalizedSHA != legacyPluginSHA256 {
		t.Fatalf("legacy fixture hash drifted: recognized=%v sha=%s", recognized, normalizedSHA)
	}
	writeTestFile(t, pluginPath, legacy)

	status, err := InspectIntegration()
	if err != nil {
		t.Fatalf("inspect legacy integration: %v", err)
	}
	if status.State != StateLegacyAdoptable {
		t.Fatalf("expected legacy-adoptable state, got %+v", status)
	}
	if _, err := InstallPlugin(writeTestBinary(t)); err != nil {
		t.Fatalf("adopt legacy plugin: %v", err)
	}
	status, err = InspectIntegration()
	if err != nil || status.State != StateOwnedCurrent {
		t.Fatalf("expected adopted plugin to be owned-current, status=%+v err=%v", status, err)
	}
}

func TestOwnedBinaryRequiresManifestHashMatch(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	binaryPath := writeTestBinary(t)
	if _, err := InstallOwnedPlugin(binaryPath); err != nil {
		t.Fatalf("install plugin: %v", err)
	}

	owned, expectedSHA, err := OwnedBinary(binaryPath)
	if err != nil || !owned || expectedSHA == "" {
		t.Fatalf("expected owned binary, owned=%v sha=%q err=%v", owned, expectedSHA, err)
	}
	writeTestFile(t, binaryPath, []byte("modified binary\n"))
	owned, _, err = OwnedBinary(binaryPath)
	if err != nil {
		t.Fatalf("inspect modified binary: %v", err)
	}
	if owned {
		t.Fatal("expected modified binary to lose ownership match")
	}
}

func TestOwnedBinarySurvivesOrphanedAdapterForUninstallRetry(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	binaryPath := writeTestBinary(t)
	pluginPath, err := InstallOwnedPlugin(binaryPath)
	if err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	if err := os.Remove(pluginPath); err != nil {
		t.Fatalf("remove adapter to create orphaned state: %v", err)
	}

	owned, expectedSHA, err := OwnedBinary(binaryPath)
	if err != nil || !owned || expectedSHA == "" {
		t.Fatalf("expected orphaned manifest to retain binary ownership, owned=%v sha=%q err=%v", owned, expectedSHA, err)
	}
}

func TestManualInstallReferencesButDoesNotOwnBinary(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	binaryPath := writeTestBinary(t)
	pluginPath, err := InstallPlugin(binaryPath)
	if err != nil {
		t.Fatalf("install manual integration: %v", err)
	}
	status, err := InspectIntegration()
	if err != nil {
		t.Fatalf("inspect manual integration: %v", err)
	}
	if status.BinaryPath != "" || status.BinarySHA256 != "" {
		t.Fatalf("manual integration must not claim package-managed binary ownership: %+v", status)
	}
	pluginData, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read installed adapter: %v", err)
	}
	if !strings.Contains(string(pluginData), binaryPath) {
		t.Fatalf("adapter must still reference the validated binary path")
	}
}

func TestBareCommandInstallNeverPatchesEmptyExecutable(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	pluginPath, err := InstallPlugin("context-bridge")
	if err != nil {
		t.Fatalf("install bare command integration: %v", err)
	}
	pluginData, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read installed adapter: %v", err)
	}
	if !strings.Contains(string(pluginData), bridgeBINMarker) {
		t.Fatalf("bare command install must preserve PATH resolution marker")
	}
}

func TestRemoveMissingPluginIsNoop(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	removed, path, err := RemovePlugin()
	if err != nil {
		t.Fatalf("remove missing plugin: %v", err)
	}
	if removed || !strings.HasSuffix(path, filepath.Join("plugins", "context-bridge.ts")) {
		t.Fatalf("unexpected missing-plugin result: removed=%v path=%q", removed, path)
	}
}

func TestInstallRejectsUnsafeBinaryPathsBeforeWriting(t *testing.T) {
	setTestUserConfigHome(t, t.TempDir())
	nonExecutable := filepath.Join(t.TempDir(), "context-bridge")
	writeTestFile(t, nonExecutable, []byte("not executable\n"))
	symlink := filepath.Join(t.TempDir(), "context-bridge-link")
	if err := os.Symlink(nonExecutable, symlink); err != nil {
		t.Fatalf("create binary symlink: %v", err)
	}

	for _, path := range []string{"relative/context-bridge", filepath.Join(t.TempDir(), "missing"), nonExecutable, symlink} {
		if _, err := InstallPlugin(path); err == nil {
			t.Errorf("expected unsafe binary path %q to be rejected", path)
		}
	}
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		t.Fatalf("resolve plugin path: %v", err)
	}
	assertMissing(t, pluginPath)
}

func setTestUserConfigHome(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", root)
		return filepath.Join(root, "Library", "Application Support")
	}
	t.Setenv("XDG_CONFIG_HOME", root)
	return root
}

func writeTestBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "context-bridge")
	writeTestFile(t, path, []byte("test context-bridge binary\n"))
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("make test binary executable: %v", err)
	}
	return path
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected content for %s:\n%s", path, got)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errorsIsNotExist(err) {
		t.Fatalf("expected %s to be absent, stat err=%v", path, err)
	}
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
