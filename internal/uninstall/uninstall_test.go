package uninstall

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/opencode"
)

func TestBuildPlanContainsOnlyManifestOwnedBinaryAndExactDataFiles(t *testing.T) {
	fx := newUninstallFixture(t)
	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	binary := findArtifact(plan.Artifacts, ArtifactBinary, fx.releaseBinary)
	if binary == nil || !binary.Exists || binary.ExpectedSHA256 == "" {
		t.Fatalf("expected manifest-owned binary artifact, got %+v", binary)
	}
	if findArtifact(plan.Artifacts, ArtifactBinary, fx.unownedBinary) != nil {
		t.Fatalf("unowned conventional-path binary must not enter the plan")
	}
	if findArtifact(plan.Artifacts, ArtifactPluginFile, fx.pluginPath) == nil {
		t.Fatalf("expected owned plugin artifact")
	}
	status, err := opencode.InspectIntegration()
	if err != nil {
		t.Fatalf("inspect integration: %v", err)
	}
	if findArtifact(plan.Artifacts, ArtifactManifest, status.ManifestPath) == nil {
		t.Fatalf("expected ownership manifest artifact")
	}
	if findArtifact(plan.Artifacts, ArtifactConfigFile, fx.configPath) == nil {
		t.Fatalf("expected exact config file artifact")
	}
	if findArtifact(plan.Artifacts, ArtifactDataFile, fx.dbPath) == nil {
		t.Fatalf("expected exact DB file artifact")
	}
	for _, artifact := range plan.Artifacts {
		if artifact.Path == fx.openCodeConfigPath {
			t.Fatalf("OpenCode config must never enter the uninstall plan")
		}
	}
}

func TestBuildPlanRejectsRelativeRuntimePaths(t *testing.T) {
	envNames := []string{"CONTEXT_BRIDGE_CONFIG", "CONTEXT_BRIDGE_DB", "CONTEXT_BRIDGE_SOCKET", "XDG_DATA_HOME"}
	if runtime.GOOS == "linux" {
		envNames = append(envNames, "XDG_CONFIG_HOME")
	}
	for _, envName := range envNames {
		t.Run(envName, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			if runtime.GOOS == "darwin" {
				t.Setenv("XDG_CONFIG_HOME", "")
			} else {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			}
			t.Setenv("CONTEXT_BRIDGE_CONFIG", "")
			t.Setenv("CONTEXT_BRIDGE_DB", "")
			t.Setenv("CONTEXT_BRIDGE_SOCKET", "")
			t.Setenv(envName, "relative/path")

			if plan, err := BuildPlan(""); err == nil || len(plan.Artifacts) != 0 {
				t.Fatalf("expected relative %s to fail before producing an uninstall plan, plan=%+v err=%v", envName, plan, err)
			}
		})
	}
}

func TestRunFullRemovesOnlyOwnedAndExactFiles(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer
	if err := Run(Options{Mode: ModeFull, AssumeYes: true, Stdout: &stdout, Stderr: &stdout}); err != nil {
		t.Fatalf("run full uninstall: %v", err)
	}

	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.pluginPath)
	assertNotExists(t, fx.configPath)
	assertNotExists(t, fx.dbPath)
	assertExists(t, fx.unownedBinary)
	assertExists(t, fx.configSentinel)
	assertExists(t, fx.dataSentinel)
	assertExists(t, fx.configDir)
	assertExists(t, fx.dataDir)
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestRunPreserveDataKeepsConfigAndDataAndNeverTouchesOpenCodeConfig(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer
	if err := Run(Options{Mode: ModePreserveData, AssumeYes: true, Stdout: &stdout, Stderr: &stdout}); err != nil {
		t.Fatalf("run preserve-data uninstall: %v", err)
	}

	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
	assertExists(t, fx.unownedBinary)
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestPromptBlankChoiceDefaultsToPreserveData(t *testing.T) {
	plan := Plan{Artifacts: []Artifact{{Kind: ArtifactBinary, Path: "/tmp/context-bridge", Exists: true, Description: "binary"}}}
	var stdout bytes.Buffer
	mode, confirmed, err := Prompt(plan, strings.NewReader("\nyes\n"), &stdout)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if mode != ModePreserveData || !confirmed {
		t.Fatalf("expected confirmed preserve-data default, mode=%q confirmed=%v", mode, confirmed)
	}
	if !strings.Contains(stdout.String(), "Preserve data (default)") || !strings.Contains(stdout.String(), "confirm preserve-data uninstall") {
		t.Fatalf("unexpected prompt: %q", stdout.String())
	}
}

func TestRunInteractiveDefaultPreservesData(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer
	if err := Run(Options{Stdin: strings.NewReader("\nyes\n"), Stdout: &stdout, Stderr: &stdout}); err != nil {
		t.Fatalf("run interactive uninstall: %v", err)
	}
	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestModifiedPluginAbortsBeforeAnyRemoval(t *testing.T) {
	fx := newUninstallFixture(t)
	modified := []byte("user modified adapter\n")
	if err := os.WriteFile(fx.pluginPath, modified, 0o644); err != nil {
		t.Fatalf("modify plugin: %v", err)
	}

	err := Run(Options{Mode: ModeFull, AssumeYes: true, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected ownership failure, got %v", err)
	}
	assertFileBytes(t, fx.pluginPath, modified)
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestFullRemovalPreservesExplicitConfigOverrideAndItsTarget(t *testing.T) {
	fx := newUninstallFixture(t)
	target := filepath.Join(t.TempDir(), "user-config.json")
	writeFixtureFile(t, target, "user-owned target")
	symlink := filepath.Join(t.TempDir(), "configured-link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}
	t.Setenv("CONTEXT_BRIDGE_CONFIG", symlink)

	var output bytes.Buffer
	if err := Run(Options{Mode: ModeFull, AssumeYes: true, Stdout: &output, Stderr: &output}); err != nil {
		t.Fatalf("run full uninstall: %v", err)
	}
	assertExists(t, symlink)
	assertFileBytes(t, target, []byte("user-owned target"))
	assertExists(t, fx.dataSentinel)
	if !strings.Contains(output.String(), "explicit override") {
		t.Fatalf("expected explicit override warning, got %q", output.String())
	}
}

func TestFullRemovalNeverTreatsOpenCodeConfigAsContextBridgeData(t *testing.T) {
	fx := newUninstallFixture(t)
	t.Setenv("CONTEXT_BRIDGE_CONFIG", fx.openCodeConfigPath)
	t.Setenv("CONTEXT_BRIDGE_DB", filepath.Join(filepath.Dir(fx.openCodeConfigPath), "secrets.db"))
	secretDB := filepath.Join(filepath.Dir(fx.openCodeConfigPath), "secrets.db")
	writeFixtureFile(t, secretDB, "do not remove")

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if findArtifact(plan.Artifacts, ArtifactConfigFile, fx.openCodeConfigPath) != nil || findArtifact(plan.Artifacts, ArtifactDataFile, secretDB) != nil {
		t.Fatalf("protected OpenCode paths must not enter uninstall artifacts: %+v", plan.Artifacts)
	}
	if err := Execute(plan, ModeFull); err != nil {
		t.Fatalf("execute full plan: %v", err)
	}
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
	assertFileBytes(t, secretDB, []byte("do not remove"))
}

func TestFullRemovalRejectsOverrideThroughDirectorySymlinkIntoOpenCode(t *testing.T) {
	fx := newUninstallFixture(t)
	linkedParent := filepath.Join(t.TempDir(), "linked-opencode")
	if err := os.Symlink(filepath.Dir(fx.openCodeConfigPath), linkedParent); err != nil {
		t.Fatalf("create OpenCode directory symlink: %v", err)
	}
	override := filepath.Join(linkedParent, filepath.Base(fx.openCodeConfigPath))
	t.Setenv("CONTEXT_BRIDGE_CONFIG", override)

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if findArtifact(plan.Artifacts, ArtifactConfigFile, override) != nil {
		t.Fatalf("directory-symlink alias of OpenCode config must not enter uninstall plan: %+v", plan.Artifacts)
	}
	if err := Execute(plan, ModeFull); err != nil {
		t.Fatalf("execute full plan: %v", err)
	}
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestRunCancelLeavesEverythingUntouched(t *testing.T) {
	fx := newUninstallFixture(t)
	var stdout bytes.Buffer
	if err := Run(Options{Stdin: strings.NewReader("\nno\n"), Stdout: &stdout, Stderr: &stdout}); err != nil {
		t.Fatalf("run cancel: %v", err)
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
	assertFileBytes(t, fx.openCodeConfigPath, fx.openCodeConfig)
}

func TestExecuteFailurePreservesAdapterAndOwnershipManifestForRetry(t *testing.T) {
	fx := newUninstallFixture(t)
	status, err := opencode.InspectIntegration()
	if err != nil {
		t.Fatalf("inspect integration: %v", err)
	}
	if err := os.Remove(fx.configPath); err != nil {
		t.Fatalf("remove config fixture: %v", err)
	}
	if err := os.Mkdir(fx.configPath, 0o700); err != nil {
		t.Fatalf("replace config with directory: %v", err)
	}

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if err := Execute(plan, ModeFull); err == nil || !strings.Contains(err.Error(), "recursively remove directory") {
		t.Fatalf("expected data removal failure, got %v", err)
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, status.ManifestPath)
}

func TestExecuteRefusesWhileCompatibleBridgeIsRunning(t *testing.T) {
	fx := newUninstallFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"context-bridge","protocol":1,"version":"test"}`))
	}))
	defer srv.Close()
	t.Setenv("CONTEXT_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if err := Execute(plan, ModeFull); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("expected running bridge refusal, got %v", err)
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestExecuteRefusesWhenUnixSocketHealthProbeTimesOut(t *testing.T) {
	fx := newUninstallFixture(t)
	socketPath := filepath.Join(fx.configDir, "bridge.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on hanging Unix socket: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	err = Execute(plan, ModeFull)
	if err == nil || !strings.Contains(err.Error(), "could not verify Unix socket") {
		t.Fatalf("expected fail-closed Unix socket timeout, got %v", err)
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case err := <-acceptErr:
		t.Fatalf("accept hanging Unix connection: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Unix health probe never connected")
	}
	assertExists(t, socketPath)
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestWaitForBridgeSocketRemovalRequiresPathDisappearance(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "bridge.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix listener: %v", err)
	}
	defer os.Remove(socketPath)

	expected, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("inspect stale Unix socket: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 25 * time.Millisecond,
	}
	err = waitForBridgeSocketRemoval(client, socketPath, expected, 25*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not remove its Unix socket") {
		t.Fatalf("expected existing refused socket to block shutdown completion, got %v", err)
	}
	assertExists(t, socketPath)
}

func TestExecuteRefusesWhenExplicitTCPHealthProbeTimesOut(t *testing.T) {
	fx := newUninstallFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on hanging TCP address: %v", err)
	}
	defer listener.Close()
	t.Setenv("CONTEXT_BRIDGE_ADDR", listener.Addr().String())

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	err = Execute(plan, ModeFull)
	if err == nil || !strings.Contains(err.Error(), "could not verify explicit TCP address") {
		t.Fatalf("expected fail-closed TCP timeout, got %v", err)
	}

	select {
	case conn := <-accepted:
		_ = conn.Close()
	case err := <-acceptErr:
		t.Fatalf("accept hanging TCP connection: %v", err)
	case <-time.After(time.Second):
		t.Fatal("TCP health probe never connected")
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestExecuteChecksExplicitTCPAfterUnixShutdownCompletes(t *testing.T) {
	fx := newUninstallFixture(t)
	socketPath := filepath.Join(fx.configDir, "bridge.sock")
	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	unixListener.SetUnlinkOnClose(false)

	var unixServer *http.Server
	unixServer = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true,"service":"context-bridge","protocol":1,"version":"test"}`))
		case "/shutdown":
			_, _ = w.Write([]byte(`{"ok":true,"shutdown":true}`))
			go func() {
				time.Sleep(10 * time.Millisecond)
				_ = unixServer.Close()
				_ = os.Remove(socketPath)
			}()
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = unixServer.Serve(unixListener) }()
	defer unixServer.Close()
	defer os.Remove(socketPath)

	tcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"context-bridge","protocol":1,"version":"test"}`))
	}))
	defer tcpServer.Close()
	t.Setenv("CONTEXT_BRIDGE_ADDR", strings.TrimPrefix(tcpServer.URL, "http://"))

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	err = Execute(plan, ModeFull)
	if err == nil || !strings.Contains(err.Error(), "still running on explicit TCP address") {
		t.Fatalf("expected explicit TCP daemon to block uninstall after Unix shutdown, got %v", err)
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestExecuteDoesNotFollowExplicitTCPHealthRedirects(t *testing.T) {
	fx := newUninstallFixture(t)
	followed := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"context-bridge","protocol":1,"version":"test"}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	t.Setenv("CONTEXT_BRIDGE_ADDR", strings.TrimPrefix(redirect.URL, "http://"))

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	err = Execute(plan, ModeFull)
	if err == nil || !strings.Contains(err.Error(), "occupied by an incompatible service") {
		t.Fatalf("expected redirecting TCP endpoint to block uninstall, got %v", err)
	}
	select {
	case <-followed:
		t.Fatal("explicit TCP health probe followed a redirect")
	default:
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestExecuteRemovesStaleUnixSocketAndOwnedArtifacts(t *testing.T) {
	fx := newUninstallFixture(t)
	socketPath := filepath.Join(fx.configDir, "bridge.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix listener: %v", err)
	}
	assertExists(t, socketPath)

	plan, err := BuildPlan("")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	if err := Execute(plan, ModeFull); err != nil {
		t.Fatalf("remove stale Unix socket and owned artifacts: %v", err)
	}
	assertNotExists(t, socketPath)
	assertNotExists(t, fx.releaseBinary)
	assertNotExists(t, fx.pluginPath)
	assertNotExists(t, fx.configPath)
	assertNotExists(t, fx.dbPath)
}

func TestExecuteRejectsRelativeSocketOverrideBeforeRemoval(t *testing.T) {
	fx := newUninstallFixture(t)
	t.Setenv("CONTEXT_BRIDGE_SOCKET", "relative/bridge.sock")

	if _, err := BuildPlan(""); err == nil || !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("expected relative socket override rejection, got %v", err)
	}
	assertExists(t, fx.releaseBinary)
	assertExists(t, fx.pluginPath)
	assertExists(t, fx.configPath)
	assertExists(t, fx.dbPath)
}

func TestRunValidatesExplicitModeAndAssumeYesPairing(t *testing.T) {
	newUninstallFixture(t)
	if err := Run(Options{AssumeYes: true, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err == nil || err.Error() != "--yes requires --mode=full or --mode=preserve-data" {
		t.Fatalf("unexpected --yes validation error: %v", err)
	}
	if err := Run(Options{Mode: ModeFull, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "--mode is only supported with --yes") {
		t.Fatalf("unexpected --mode validation error: %v", err)
	}
}

type uninstallFixture struct {
	home               string
	releaseBinary      string
	unownedBinary      string
	pluginPath         string
	openCodeConfigPath string
	openCodeConfig     []byte
	configDir          string
	configPath         string
	configSentinel     string
	dataDir            string
	dbPath             string
	dataSentinel       string
}

func newUninstallFixture(t *testing.T) uninstallFixture {
	t.Helper()
	// A short base keeps fixture Unix socket paths under the sun_path limit,
	// which t.TempDir() can exceed for long test names.
	home, err := os.MkdirTemp("", "cb-uninstall-")
	if err != nil {
		t.Fatalf("create fixture home: %v", err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatalf("secure fixture home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	configHome := filepath.Join(home, "xdg-config")
	dataHome := filepath.Join(home, "xdg-data")
	t.Setenv("HOME", home)
	if runtime.GOOS == "darwin" {
		t.Setenv("XDG_CONFIG_HOME", "")
		configHome = filepath.Join(home, "Library", "Application Support")
	} else {
		t.Setenv("XDG_CONFIG_HOME", configHome)
	}
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("CONTEXT_BRIDGE_CONFIG", "")
	t.Setenv("CONTEXT_BRIDGE_DB", "")
	t.Setenv("CONTEXT_BRIDGE_SOCKET", "")
	t.Setenv("CONTEXT_BRIDGE_ADDR", "")
	t.Setenv("CONTEXT_BRIDGE_PORT", "")

	fx := uninstallFixture{
		home:               home,
		releaseBinary:      filepath.Join(home, ".local", "bin", "context-bridge"),
		unownedBinary:      filepath.Join(home, "go", "bin", "context-bridge"),
		openCodeConfigPath: filepath.Join(configHome, "opencode", "opencode.json"),
		openCodeConfig:     []byte("{\n  // must remain byte-identical\n  \"secret\": \"keep\",\n  \"mcp\": {\"context-bridge\": {\"enabled\": true}}\n}\n"),
		configDir:          filepath.Join(configHome, "context-bridge"),
		dataDir:            filepath.Join(dataHome, "context-bridge"),
	}
	fx.configPath = filepath.Join(fx.configDir, "config.json")
	fx.configSentinel = filepath.Join(fx.configDir, "user-owned.txt")
	fx.dbPath = filepath.Join(fx.dataDir, "store.db")
	fx.dataSentinel = filepath.Join(fx.dataDir, "user-owned.txt")

	writeFixtureFile(t, fx.releaseBinary, "release binary")
	if err := os.Chmod(fx.releaseBinary, 0o755); err != nil {
		t.Fatalf("make release binary executable: %v", err)
	}
	writeFixtureFile(t, fx.unownedBinary, "unowned binary")
	writeFixtureFile(t, fx.openCodeConfigPath, string(fx.openCodeConfig))
	writeFixtureFile(t, fx.configPath, `{"search_mode":"regex"}`)
	writeFixtureFile(t, fx.configSentinel, "keep config sibling")
	writeFixtureFile(t, fx.dbPath, "sqlite")
	writeFixtureFile(t, fx.dataSentinel, "keep data sibling")

	pluginPath, err := opencode.InstallOwnedPlugin(fx.releaseBinary)
	if err != nil {
		t.Fatalf("install owned fixture plugin: %v", err)
	}
	fx.pluginPath = pluginPath
	return fx
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %q to exist, stat err=%v", path, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to be absent, stat err=%v", path, err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected content in %q: %q", path, got)
	}
}
