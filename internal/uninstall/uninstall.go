package uninstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	ArtifactManifest   ArtifactKind = "ownership-manifest"
	ArtifactConfigFile ArtifactKind = "config-file"
	ArtifactDataFile   ArtifactKind = "data-file"
	ArtifactRuntime    ArtifactKind = "runtime-file"
)

type Artifact struct {
	Kind           ArtifactKind
	Path           string
	Exists         bool
	SharedParent   bool
	Description    string
	ExpectedSHA256 string
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
		for _, warning := range plan.Warnings {
			if _, err := fmt.Fprintf(opts.Stdout, "Warning: %s\n", warning); err != nil {
				return err
			}
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
	configPath, err := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve config path: %w", err)
	}
	dbPath, err := config.ResolveDBPath(os.LookupEnv, os.UserHomeDir)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve database path: %w", err)
	}
	socketPath, err := config.ResolveSocketPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve Unix socket path: %w", err)
	}

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

	status, err := opencode.InspectIntegration()
	if err != nil {
		return Plan{}, fmt.Errorf("inspect OpenCode integration: %w", err)
	}
	appendArtifact(Artifact{
		Kind:         ArtifactPluginFile,
		Path:         status.PluginPath,
		Exists:       status.PluginExists,
		SharedParent: true,
		Description:  "owned OpenCode adapter",
	})
	appendArtifact(Artifact{
		Kind:         ArtifactManifest,
		Path:         status.ManifestPath,
		Exists:       status.ManifestExists,
		SharedParent: true,
		Description:  "OpenCode integration ownership manifest",
	})
	if status.State == opencode.StateForeign || status.State == opencode.StateModified || status.State == opencode.StateUnsafe || status.State == opencode.StateInvalidManifest {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("OpenCode integration state is %s and will not be removed without verified ownership: %s", status.State, status.Reason))
	}

	if status.BinaryPath != "" {
		owned, expectedSHA, err := opencode.OwnedBinary(status.BinaryPath)
		if err != nil {
			return Plan{}, fmt.Errorf("inspect managed binary: %w", err)
		}
		if owned {
			if normalizePath(status.BinaryPath) == excludePath {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("Skipping bootstrap binary at %s.", status.BinaryPath))
			} else {
				appendArtifact(Artifact{
					Kind:           ArtifactBinary,
					Path:           status.BinaryPath,
					Exists:         true,
					SharedParent:   true,
					Description:    "manifest-owned context-bridge binary",
					ExpectedSHA256: expectedSHA,
				})
			}
		} else {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Binary %s no longer matches its ownership manifest and will be preserved.", status.BinaryPath))
		}
	}

	openCodeConfigDir, err := opencode.OpenCodeConfigDir()
	if err != nil {
		return Plan{}, fmt.Errorf("resolve OpenCode config directory: %w", err)
	}
	protectedPaths := map[string]bool{
		normalizePath(status.PluginPath):   true,
		normalizePath(status.ManifestPath): true,
		normalizePath(status.BinaryPath):   status.BinaryPath != "",
	}
	appendDataArtifact := func(artifact Artifact) {
		path := normalizePath(artifact.Path)
		if path == "" {
			return
		}
		if protectedPaths[path] || pathWithinResolved(path, openCodeConfigDir) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("Refusing to treat protected integration/OpenCode path %s as Context Bridge data.", path))
			return
		}
		appendArtifact(artifact)
	}

	if explicitEnvPath("CONTEXT_BRIDGE_CONFIG") {
		plan.Warnings = append(plan.Warnings, "CONTEXT_BRIDGE_CONFIG is an explicit override and will be preserved; remove it manually after verifying ownership.")
	} else {
		appendDataArtifact(configArtifact(configPath))
	}
	if explicitEnvPath("CONTEXT_BRIDGE_DB") {
		plan.Warnings = append(plan.Warnings, "CONTEXT_BRIDGE_DB is an explicit override and will be preserved; remove it manually after verifying ownership.")
	} else {
		for _, artifact := range dataArtifacts(dbPath) {
			appendDataArtifact(artifact)
		}
	}
	if explicitEnvPath("CONTEXT_BRIDGE_SOCKET") {
		plan.Warnings = append(plan.Warnings, "CONTEXT_BRIDGE_SOCKET is an explicit override and will not be removed directly; a running bridge may remove its own socket during shutdown.")
	} else {
		appendDataArtifact(Artifact{
			Kind:         ArtifactRuntime,
			Path:         socketPath,
			Exists:       pathExists(socketPath),
			SharedParent: true,
			Description:  "Context Bridge Unix socket",
		})
	}
	return plan, nil
}

func RenderPlan(plan Plan, mode Mode) string {
	var b strings.Builder
	b.WriteString("Uninstall plan\n")
	if mode == "" {
		b.WriteString("Modes: preserve-data (default) or full\n")
	} else {
		fmt.Fprintf(&b, "Mode: %s\n", mode)
	}
	b.WriteString("\nArtifacts in scope:\n")
	for _, artifact := range plan.Artifacts {
		if !artifact.Exists {
			continue
		}
		note := ""
		if artifact.Kind == ArtifactConfigFile || artifact.Kind == ArtifactDataFile {
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
	removePlugin := false
	for _, artifact := range artifacts {
		if (artifact.Kind == ArtifactPluginFile || artifact.Kind == ArtifactManifest) && artifact.Exists {
			removePlugin = true
			break
		}
	}
	if err := ensureBridgeStopped(); err != nil {
		return err
	}
	if removePlugin {
		if err := opencode.ValidatePluginRemoval(); err != nil {
			return fmt.Errorf("validate owned OpenCode integration before uninstall: %w", err)
		}
	}
	currentExe := currentExecutablePath()
	var dataFiles []Artifact
	var binaries []Artifact
	var currentBinary []Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == ArtifactPluginFile || artifact.Kind == ArtifactManifest || !artifact.Exists {
			continue
		}
		if artifact.Kind == ArtifactBinary && currentExe != "" && normalizePath(artifact.Path) == currentExe {
			currentBinary = append(currentBinary, artifact)
			continue
		}
		if artifact.Kind == ArtifactBinary {
			binaries = append(binaries, artifact)
			continue
		}
		dataFiles = append(dataFiles, artifact)
	}

	for _, artifact := range dataFiles {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}
	for _, artifact := range binaries {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}
	for _, artifact := range currentBinary {
		if err := removeArtifactPath(artifact); err != nil {
			return err
		}
	}
	if removePlugin {
		if _, _, err := opencode.RemovePlugin(); err != nil {
			return fmt.Errorf("remove owned OpenCode integration: %w", err)
		}
	}
	return nil
}

func filterArtifacts(artifacts []Artifact, mode Mode) []Artifact {
	filtered := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if mode == ModePreserveData && (artifact.Kind == ArtifactConfigFile || artifact.Kind == ArtifactDataFile) {
			continue
		}
		filtered = append(filtered, artifact)
	}
	return filtered
}

func removeArtifactPath(artifact Artifact) error {
	info, err := os.Lstat(artifact.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect %s %s: %w", artifact.Description, artifact.Path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to recursively remove directory %s", artifact.Path)
	}
	if artifact.Kind == ArtifactRuntime && info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket runtime path %s", artifact.Path)
	}
	if artifact.ExpectedSHA256 != "" {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to remove managed binary with unsafe file type: %s", artifact.Path)
		}
		return quarantineRemoveArtifact(artifact)
	}
	if err := os.Remove(artifact.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s %s: %w", artifact.Description, artifact.Path, err)
	}
	return nil
}

func configArtifact(configPath string) Artifact {
	return Artifact{
		Kind:         ArtifactConfigFile,
		Path:         configPath,
		Exists:       pathExists(configPath),
		SharedParent: true,
		Description:  "resolved Context Bridge config file",
	}
}

func dataArtifacts(dbPath string) []Artifact {
	paths := []struct {
		path        string
		description string
	}{
		{dbPath, "resolved Context Bridge data store"},
		{dbPath + "-wal", "resolved Context Bridge SQLite WAL"},
		{dbPath + "-shm", "resolved Context Bridge SQLite shared-memory file"},
	}
	artifacts := make([]Artifact, 0, len(paths))
	for _, item := range paths {
		artifacts = append(artifacts, Artifact{
			Kind:         ArtifactDataFile,
			Path:         item.path,
			Exists:       pathExists(item.path),
			SharedParent: true,
			Description:  item.description,
		})
	}
	return artifacts
}

func explicitEnvPath(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}

func ensureBridgeStopped() error {
	socketPath, err := config.ResolveSocketPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return fmt.Errorf("resolve Unix socket path: %w", err)
	}
	if socketPath != "" {
		socketInfo, err := os.Lstat(socketPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect Unix socket %s before uninstall: %w", socketPath, err)
			}
		} else if socketInfo.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("Unix socket path %s is not a socket; refusing to remove artifacts", socketPath)
		}
		if errors.Is(err, os.ErrNotExist) {
			return ensureExplicitTCPStopped()
		}

		transport := &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 500 * time.Millisecond}
		reachable, compatible, err := probeUnixBridge(client, socketPath)
		if err != nil {
			return err
		}
		if reachable && !compatible {
			return fmt.Errorf("Unix socket %s is occupied by an incompatible service; refusing to remove it", socketPath)
		}
		if compatible {
			request, err := http.NewRequest(http.MethodPost, "http://context-bridge/shutdown", strings.NewReader(`{}`))
			if err != nil {
				return err
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				return fmt.Errorf("request graceful bridge shutdown: %w", err)
			}
			var acknowledged struct {
				OK       bool `json:"ok"`
				Shutdown bool `json:"shutdown"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&acknowledged)
			_ = response.Body.Close()
			if decodeErr != nil || response.StatusCode != http.StatusOK || !acknowledged.OK || !acknowledged.Shutdown {
				return fmt.Errorf("bridge at %s refused graceful shutdown", socketPath)
			}
			if err := waitForBridgeSocketRemoval(client, socketPath, socketInfo, 3*time.Second); err != nil {
				return err
			}
			return ensureExplicitTCPStopped()
		}
	}
	return ensureExplicitTCPStopped()
}

func waitForBridgeSocketRemoval(client *http.Client, socketPath string, expected os.FileInfo, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastProbeErr error
	for time.Now().Before(deadline) {
		current, err := os.Lstat(socketPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Unix socket %s during graceful shutdown: %w", socketPath, err)
		}
		if current.Mode()&os.ModeSocket == 0 || !os.SameFile(expected, current) {
			return fmt.Errorf("Unix socket %s changed ownership during shutdown", socketPath)
		}

		reachable, compatible, err := probeUnixBridge(client, socketPath)
		if err != nil {
			lastProbeErr = err
		} else {
			lastProbeErr = nil
			if reachable && !compatible {
				return fmt.Errorf("Unix socket %s changed ownership during shutdown", socketPath)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastProbeErr != nil {
		return fmt.Errorf("could not confirm graceful bridge shutdown before uninstall timeout: %w", lastProbeErr)
	}
	return fmt.Errorf("context-bridge at %s did not remove its Unix socket before uninstall timeout", socketPath)
}

func ensureExplicitTCPStopped() error {
	addr := strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_ADDR"))
	if addr == "" {
		port := strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_PORT"))
		if port == "" {
			return nil
		}
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(port) == "" {
		return nil
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil
		}
	}

	client := &http.Client{
		Timeout: 300 * time.Millisecond,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	reachable, compatible, err := probeBridge(client, "http://"+addr+"/health")
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil
		}
		return fmt.Errorf("could not verify explicit TCP address %s is stopped; refusing to remove artifacts: %w", addr, err)
	}
	if reachable && !compatible {
		return fmt.Errorf("explicit TCP address %s is occupied by an incompatible service", addr)
	}
	if compatible {
		return fmt.Errorf("context-bridge serve is still running on explicit TCP address %s; stop that process before uninstalling", addr)
	}
	return nil
}

func probeUnixBridge(client *http.Client, socketPath string) (reachable, compatible bool, err error) {
	reachable, compatible, err = probeBridge(client, "http://context-bridge/health")
	if err == nil {
		return reachable, compatible, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return false, false, nil
	}
	return false, false, fmt.Errorf("could not verify Unix socket %s is stopped; refusing to remove artifacts: %w", socketPath, err)
}

func probeBridge(client *http.Client, healthURL string) (reachable, compatible bool, err error) {
	response, err := client.Get(healthURL)
	if err != nil {
		return false, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return true, false, nil
	}
	var health struct {
		OK       bool   `json:"ok"`
		Service  string `json:"service"`
		Protocol int    `json:"protocol"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if err := decoder.Decode(&health); err != nil {
		return true, false, nil
	}
	return true, health.OK && health.Service == "context-bridge" && health.Protocol == 1, nil
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
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func pathWithin(path, root string) bool {
	path = normalizePath(path)
	root = normalizePath(root)
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathWithinResolved(path, root string) bool {
	if pathWithin(path, root) {
		return true
	}
	resolvedPath, pathErr := resolveParentSymlinks(path)
	resolvedRoot, rootErr := filepath.EvalSymlinks(normalizePath(root))
	if pathErr != nil || rootErr != nil {
		return false
	}
	return pathWithin(resolvedPath, resolvedRoot)
}

func resolveParentSymlinks(path string) (string, error) {
	path = normalizePath(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func quarantineRemoveArtifact(artifact Artifact) error {
	dir := filepath.Dir(artifact.Path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(artifact.Path)+".remove-*")
	if err != nil {
		return fmt.Errorf("create quarantine for %s: %w", artifact.Path, err)
	}
	quarantinePath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(quarantinePath)
		return err
	}
	if err := os.Remove(quarantinePath); err != nil {
		return err
	}
	if err := os.Rename(artifact.Path, quarantinePath); err != nil {
		return fmt.Errorf("quarantine %s: %w", artifact.Path, err)
	}

	actualSHA, hashErr := fileSHA256(quarantinePath)
	if hashErr != nil || actualSHA != artifact.ExpectedSHA256 {
		restoreErr := restoreQuarantinedArtifact(artifact.Path, quarantinePath)
		return fmt.Errorf("refusing to remove changed managed binary %s (verify=%v restore=%v)", artifact.Path, hashErr, restoreErr)
	}
	if err := os.Remove(quarantinePath); err != nil {
		restoreErr := restoreQuarantinedArtifact(artifact.Path, quarantinePath)
		return fmt.Errorf("remove quarantined binary %s: %v (restore=%v)", artifact.Path, err, restoreErr)
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func restoreQuarantinedArtifact(path, quarantinePath string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("target was recreated; preserved quarantined file at %s", quarantinePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(quarantinePath, path)
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
