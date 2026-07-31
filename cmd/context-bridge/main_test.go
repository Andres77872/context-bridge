package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"context-bridge/internal/uninstall"
)

func TestRunIntegrationLifecycleNeverWritesOpenCodeConfig(t *testing.T) {
	originalStdout := commandStdout
	t.Cleanup(func() { commandStdout = originalStdout })

	testHome := t.TempDir()
	configHome := testHome
	if runtime.GOOS == "darwin" {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", testHome)
		configHome = filepath.Join(testHome, "Library", "Application Support")
	} else {
		t.Setenv("XDG_CONFIG_HOME", configHome)
	}
	configPath := filepath.Join(configHome, "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	originalConfig := []byte("{ invalid jsonc that integration must never parse }\n")
	if err := os.WriteFile(configPath, originalConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	commandStdout = &stdout
	if err := run([]string{"integration", "install"}); err != nil {
		t.Fatalf("install integration: %v", err)
	}
	if err := run([]string{"integration", "status"}); err != nil {
		t.Fatalf("status integration: %v", err)
	}
	if !strings.Contains(stdout.String(), "state: owned-current") {
		t.Fatalf("expected owned integration status, got %q", stdout.String())
	}
	if err := run([]string{"integration", "uninstall"}); err != nil {
		t.Fatalf("uninstall integration: %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(originalConfig) {
		t.Fatalf("expected OpenCode config to remain byte-identical, got %q", got)
	}
}

func TestRunRoutesUninstallSubcommandToNativeEngine(t *testing.T) {
	originalRunUninstall := runUninstall
	originalStdin := commandStdin
	originalStdout := commandStdout
	originalStderr := commandStderr
	t.Cleanup(func() {
		runUninstall = originalRunUninstall
		commandStdin = originalStdin
		commandStdout = originalStdout
		commandStderr = originalStderr
	})

	var captured uninstall.Options
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	commandStdin = strings.NewReader("")
	commandStdout = &stdout
	commandStderr = &stderr
	t.Setenv("CONTEXT_BRIDGE_UNINSTALL_EXCLUDE_PATH", "/tmp/bootstrap/context-bridge")

	runUninstall = func(opts uninstall.Options) error {
		captured = opts
		return nil
	}

	if err := run([]string{"uninstall", "--dry-run"}); err != nil {
		t.Fatalf("run uninstall dry-run: %v", err)
	}

	if !captured.DryRun {
		t.Fatalf("expected uninstall dry-run flag to be forwarded")
	}
	if captured.Mode != "" {
		t.Fatalf("expected interactive uninstall to leave mode unset, got %q", captured.Mode)
	}
	if captured.AssumeYes {
		t.Fatalf("expected --yes to remain false")
	}
	if captured.ExcludePath != "/tmp/bootstrap/context-bridge" {
		t.Fatalf("expected exclude path to be forwarded, got %q", captured.ExcludePath)
	}
	if captured.Stdin != commandStdin {
		t.Fatalf("expected cmdUninstall to forward command stdin")
	}
	if captured.Stdout != commandStdout {
		t.Fatalf("expected cmdUninstall to forward command stdout")
	}
	if captured.Stderr != commandStderr {
		t.Fatalf("expected cmdUninstall to forward command stderr")
	}
}

func TestRunRoutesUninstallExplicitModeAndYes(t *testing.T) {
	originalRunUninstall := runUninstall
	t.Cleanup(func() {
		runUninstall = originalRunUninstall
	})

	var captured uninstall.Options
	runUninstall = func(opts uninstall.Options) error {
		captured = opts
		return nil
	}

	if err := run([]string{"uninstall", "--mode=preserve-data", "--yes"}); err != nil {
		t.Fatalf("run uninstall non-interactive: %v", err)
	}

	if captured.Mode != uninstall.ModePreserveData {
		t.Fatalf("expected explicit mode %q, got %q", uninstall.ModePreserveData, captured.Mode)
	}
	if !captured.AssumeYes {
		t.Fatalf("expected --yes to be forwarded")
	}
	if captured.DryRun {
		t.Fatalf("expected dry-run to remain false")
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7438", "127.7.8.9:7438", "[::1]:7438", "localhost:7438"} {
		if err := validateLoopbackAddr(addr); err != nil {
			t.Errorf("expected %q to be accepted: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:7438", ":7438", "192.0.2.1:7438", "example.com:7438", "127.0.0.1"} {
		if err := validateLoopbackAddr(addr); err == nil {
			t.Errorf("expected %q to be rejected", addr)
		}
	}
}

func TestValidateSocketPathRequiresAbsolutePath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "bridge.sock")
	got, err := validateSocketPath("  " + want + "  ")
	if err != nil {
		t.Fatalf("expected absolute socket path to be accepted: %v", err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("expected normalized socket path %q, got %q", filepath.Clean(want), got)
	}
	for _, path := range []string{"", "bridge.sock", "context-bridge/bridge.sock"} {
		if _, err := validateSocketPath(path); err == nil {
			t.Errorf("expected %q to be rejected", path)
		}
	}
}

func TestCleanupServeResourcesClosesStoreBeforeRemovingSocket(t *testing.T) {
	var events []string
	cleanupServeResources(&recordingCloser{events: &events}, func() {
		events = append(events, "socket")
	})
	if got := strings.Join(events, ","); got != "store,socket" {
		t.Fatalf("unexpected cleanup order: %s", got)
	}
}

// privateSocketDir returns a per-test directory restricted to the owner so it
// satisfies bridgeListener's 0700 socket-directory requirement under any umask.
func privateSocketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("secure socket directory: %v", err)
	}
	return dir
}

func TestBridgeListenerRefusesToReplaceSocketAfterIndeterminateDialError(t *testing.T) {
	originalDial := dialUnixSocket
	t.Cleanup(func() { dialUnixSocket = originalDial })

	socketPath := filepath.Join(privateSocketDir(t), "bridge.sock")
	live, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	defer live.Close()
	dialUnixSocket = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	}

	listener, cleanup, err := bridgeListener("", socketPath)
	cleanup()
	if listener != nil || err == nil || !strings.Contains(err.Error(), "could not verify existing Unix socket") {
		t.Fatalf("expected indeterminate socket probe to fail closed, listener=%v err=%v", listener, err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("existing Unix socket was removed after indeterminate probe: %v", err)
	}
}

func TestBridgeListenerReplacesDefinitivelyStaleSocket(t *testing.T) {
	socketPath := filepath.Join(privateSocketDir(t), "bridge.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale Unix socket: %v", err)
	}

	listener, cleanup, err := bridgeListener("", socketPath)
	if err != nil {
		t.Fatalf("replace definitively stale Unix socket: %v", err)
	}
	if listener == nil {
		t.Fatal("expected replacement Unix listener")
	}
	cleanup()
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("replacement socket cleanup failed: %v", err)
	}
}

func TestBridgeListenerDefersSocketUnlinkUntilOwnedCleanup(t *testing.T) {
	socketPath := filepath.Join(privateSocketDir(t), "bridge.sock")
	listener, cleanup, err := bridgeListener("", socketPath)
	if err != nil {
		t.Fatalf("create Unix listener: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close Unix listener: %v", err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("listener close unlinked socket before owned cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("owned cleanup did not unlink socket: %v", err)
	}
}

type recordingCloser struct {
	events *[]string
}

func (c *recordingCloser) Close() error {
	*c.events = append(*c.events, "store")
	return nil
}
