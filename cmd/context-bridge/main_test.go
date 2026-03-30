package main

import (
	"bytes"
	"strings"
	"testing"

	"context-bridge/internal/uninstall"
)

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
