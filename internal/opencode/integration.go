package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"context-bridge/plugin"
)

type IntegrationState string

const (
	StateAbsent           IntegrationState = "absent"
	StateOwnedCurrent     IntegrationState = "owned-current"
	StateOwnedStale       IntegrationState = "owned-stale"
	StateLegacyAdoptable  IntegrationState = "legacy-adoptable"
	StateForeign          IntegrationState = "foreign"
	StateModified         IntegrationState = "modified"
	StateOrphanedManifest IntegrationState = "orphaned-manifest"
	StateInvalidManifest  IntegrationState = "invalid-manifest"
	StateUnsafe           IntegrationState = "unsafe"

	manifestSchemaVersion = 1
	manifestOwner         = "context-bridge"
	legacyPluginSHA256    = "dc50ccea88e3550ad0ab10187d1f7002b95f8d6aa8d203b15252f7c7760b4cdb"
	bridgeBINMarker       = `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";`
	legacyBridgeBINPrefix = `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? `
	ownedBridgeBINPrefix  = `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? `
)

type IntegrationStatus struct {
	State              IntegrationState `json:"state"`
	PluginPath         string           `json:"plugin_path"`
	ManifestPath       string           `json:"manifest_path"`
	PluginExists       bool             `json:"plugin_exists"`
	ManifestExists     bool             `json:"manifest_exists"`
	PluginSHA256       string           `json:"plugin_sha256,omitempty"`
	ManifestFileSHA256 string           `json:"manifest_file_sha256,omitempty"`
	BinaryPath         string           `json:"binary_path,omitempty"`
	BinarySHA256       string           `json:"binary_sha256,omitempty"`
	Reason             string           `json:"reason,omitempty"`
}

type ownershipManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Owner         string `json:"owner"`
	PluginPath    string `json:"plugin_path"`
	PluginSHA256  string `json:"plugin_sha256"`
	BinaryPath    string `json:"binary_path,omitempty"`
	BinarySHA256  string `json:"binary_sha256,omitempty"`
}

func OpenCodeConfigDir() (string, error) {
	configDir, err := userConfigDir(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "opencode"), nil
}

func OpenCodePluginPath() (string, error) {
	configDir, err := OpenCodeConfigDir()
	if err != nil {
		return "", err
	}
	return cleanAbsolutePath(filepath.Join(configDir, "plugins", "context-bridge.ts"))
}

func IntegrationManifestPath() (string, error) {
	configDir, err := userConfigDir(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return cleanAbsolutePath(filepath.Join(configDir, "context-bridge", "opencode-integration.json"))
}

func InspectIntegration() (IntegrationStatus, error) {
	pluginPath, err := OpenCodePluginPath()
	if err != nil {
		return IntegrationStatus{}, err
	}
	manifestPath, err := IntegrationManifestPath()
	if err != nil {
		return IntegrationStatus{}, err
	}

	status := IntegrationStatus{PluginPath: pluginPath, ManifestPath: manifestPath}
	pluginData, pluginExists, unsafeReason, err := readRegularFile(pluginPath)
	if err != nil {
		return status, err
	}
	status.PluginExists = pluginExists
	if pluginExists {
		status.PluginSHA256 = hashBytes(pluginData)
	}
	if unsafeReason != "" {
		status.State = StateUnsafe
		status.Reason = unsafeReason
		return status, nil
	}

	manifestData, manifestExists, unsafeReason, err := readRegularFile(manifestPath)
	if err != nil {
		return status, err
	}
	status.ManifestExists = manifestExists
	if manifestExists {
		status.ManifestFileSHA256 = hashBytes(manifestData)
	}
	if unsafeReason != "" {
		status.State = StateUnsafe
		status.Reason = unsafeReason
		return status, nil
	}

	if !manifestExists {
		switch {
		case !pluginExists:
			status.State = StateAbsent
		case isRecognizedPlugin(pluginData):
			status.State = StateLegacyAdoptable
			status.Reason = "recognized pre-ownership adapter can be adopted"
		default:
			status.State = StateForeign
			status.Reason = "plugin exists without a valid Context Bridge ownership manifest"
		}
		return status, nil
	}

	manifest, reason := decodeManifest(manifestData, pluginPath)
	if reason != "" {
		status.State = StateInvalidManifest
		status.Reason = reason
		return status, nil
	}
	status.BinaryPath = manifest.BinaryPath
	status.BinarySHA256 = manifest.BinarySHA256

	if !pluginExists {
		status.State = StateOrphanedManifest
		status.Reason = "ownership manifest exists but the adapter is absent"
		return status, nil
	}
	normalizedSHA, recognized := normalizedPluginSHA256(pluginData)
	currentSHA, currentRecognized := normalizedPluginSHA256(plugin.OpenCodePlugin)
	if status.PluginSHA256 != manifest.PluginSHA256 {
		if recognized && currentRecognized && normalizedSHA == currentSHA {
			status.State = StateOwnedStale
			status.Reason = "recognized owned adapter is newer than its manifest; reinstall repairs ownership metadata"
			return status, nil
		}
		status.State = StateModified
		status.Reason = "adapter hash no longer matches its ownership manifest"
		return status, nil
	}

	if recognized && currentRecognized && normalizedSHA == currentSHA {
		status.State = StateOwnedCurrent
		return status, nil
	}
	status.State = StateOwnedStale
	status.Reason = "owned adapter differs from the currently embedded adapter"
	return status, nil
}

func InstallPlugin(command string) (string, error) {
	return installPlugin(command, false)
}

func InstallOwnedPlugin(command string) (string, error) {
	return installPlugin(command, true)
}

func installPlugin(command string, ownBinary bool) (string, error) {
	binaryCommand, binaryPath, binarySHA, err := inspectInstallBinary(command, ownBinary)
	if err != nil {
		return "", err
	}
	status, err := InspectIntegration()
	if err != nil {
		return "", err
	}
	switch status.State {
	case StateAbsent, StateOwnedCurrent, StateOwnedStale, StateLegacyAdoptable, StateOrphanedManifest:
		// Safe states for install or adoption.
	default:
		return "", fmt.Errorf("refusing to install OpenCode adapter in state %q: %s", status.State, status.Reason)
	}

	desired, err := patchedPlugin(binaryCommand)
	if err != nil {
		return "", err
	}
	manifest := ownershipManifest{
		SchemaVersion: manifestSchemaVersion,
		Owner:         manifestOwner,
		PluginPath:    status.PluginPath,
		PluginSHA256:  hashBytes(desired),
		BinaryPath:    binaryPath,
		BinarySHA256:  binarySHA,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode integration manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')

	previousPlugin, previousPluginExists, _, err := readRegularFile(status.PluginPath)
	if err != nil {
		return "", err
	}
	previousManifest, previousManifestExists, _, err := readRegularFile(status.ManifestPath)
	if err != nil {
		return "", err
	}
	if err := verifyObservedState(status); err != nil {
		return "", err
	}

	if err := atomicWriteObserved(status.PluginPath, desired, 0o644, status.PluginExists, status.PluginSHA256); err != nil {
		return "", fmt.Errorf("install OpenCode adapter: %w", err)
	}
	if err := atomicWriteObserved(status.ManifestPath, manifestData, 0o600, status.ManifestExists, status.ManifestFileSHA256); err != nil {
		rollbackErr := restoreObservedFile(status.PluginPath, hashBytes(desired), previousPlugin, previousPluginExists, 0o644)
		manifestRollbackErr := restoreObservedFile(status.ManifestPath, hashBytes(manifestData), previousManifest, previousManifestExists, 0o600)
		if rollbackErr != nil || manifestRollbackErr != nil {
			return "", fmt.Errorf("write integration manifest: %v; rollback adapter: %v; rollback manifest: %v", err, rollbackErr, manifestRollbackErr)
		}
		return "", fmt.Errorf("write integration manifest: %w", err)
	}

	return status.PluginPath, nil
}

func RemovePlugin() (bool, string, error) {
	status, err := InspectIntegration()
	if err != nil {
		return false, "", err
	}
	switch status.State {
	case StateAbsent:
		return false, status.PluginPath, nil
	case StateOrphanedManifest:
		if err := quarantineRemoveFile(status.ManifestPath, status.ManifestFileSHA256); err != nil {
			return false, status.PluginPath, fmt.Errorf("remove orphaned integration manifest: %w", err)
		}
		return false, status.PluginPath, nil
	case StateOwnedCurrent, StateOwnedStale, StateLegacyAdoptable:
		// Revalidated immediately below.
	default:
		return false, status.PluginPath, fmt.Errorf("refusing to remove OpenCode adapter in state %q: %s", status.State, status.Reason)
	}

	if err := verifyObservedState(status); err != nil {
		return false, status.PluginPath, err
	}
	if err := quarantineRemoveFile(status.PluginPath, status.PluginSHA256); err != nil {
		return false, status.PluginPath, fmt.Errorf("remove owned OpenCode adapter: %w", err)
	}
	if status.ManifestExists {
		if err := quarantineRemoveFile(status.ManifestPath, status.ManifestFileSHA256); err != nil {
			return true, status.PluginPath, fmt.Errorf("remove integration manifest: %w", err)
		}
	}
	return true, status.PluginPath, nil
}

// ValidatePluginRemoval proves that an adapter or ownership manifest currently
// exists in a removable state without mutating either file. Uninstall uses this
// as a fail-closed preflight before touching data or the managed binary.
func ValidatePluginRemoval() error {
	status, err := InspectIntegration()
	if err != nil {
		return err
	}
	switch status.State {
	case StateOwnedCurrent, StateOwnedStale, StateLegacyAdoptable, StateOrphanedManifest:
		return verifyObservedState(status)
	default:
		return fmt.Errorf("refusing removal preflight in state %q: %s", status.State, status.Reason)
	}
}

func OwnedBinary(path string) (bool, string, error) {
	status, err := InspectIntegration()
	if err != nil {
		return false, "", err
	}
	if status.State != StateOwnedCurrent && status.State != StateOwnedStale && status.State != StateOrphanedManifest {
		return false, "", nil
	}
	cleanPath, err := cleanAbsolutePath(path)
	if err != nil {
		return false, "", err
	}
	if cleanPath != status.BinaryPath || status.BinarySHA256 == "" {
		return false, "", nil
	}
	data, exists, unsafeReason, err := readRegularFile(cleanPath)
	if err != nil {
		return false, "", err
	}
	if unsafeReason != "" {
		return false, "", fmt.Errorf("unsafe managed binary: %s", unsafeReason)
	}
	if !exists || hashBytes(data) != status.BinarySHA256 {
		return false, "", nil
	}
	return true, status.BinarySHA256, nil
}

func decodeManifest(data []byte, pluginPath string) (ownershipManifest, string) {
	var manifest ownershipManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ownershipManifest{}, fmt.Sprintf("invalid ownership manifest JSON: %v", err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return ownershipManifest{}, fmt.Sprintf("unsupported ownership manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.Owner != manifestOwner {
		return ownershipManifest{}, "ownership manifest has an unexpected owner"
	}
	cleanManifestPath, err := cleanAbsolutePath(manifest.PluginPath)
	if err != nil || cleanManifestPath != pluginPath {
		return ownershipManifest{}, "ownership manifest targets a different adapter path"
	}
	if len(manifest.PluginSHA256) != sha256.Size*2 {
		return ownershipManifest{}, "ownership manifest has an invalid adapter hash"
	}
	if _, err := hex.DecodeString(manifest.PluginSHA256); err != nil {
		return ownershipManifest{}, "ownership manifest has an invalid adapter hash"
	}
	if (manifest.BinaryPath == "") != (manifest.BinarySHA256 == "") {
		return ownershipManifest{}, "ownership manifest has incomplete binary ownership data"
	}
	if manifest.BinaryPath != "" {
		cleanBinaryPath, err := cleanAbsolutePath(manifest.BinaryPath)
		if err != nil || cleanBinaryPath != manifest.BinaryPath {
			return ownershipManifest{}, "ownership manifest has an invalid binary path"
		}
		if len(manifest.BinarySHA256) != sha256.Size*2 {
			return ownershipManifest{}, "ownership manifest has an invalid binary hash"
		}
		if _, err := hex.DecodeString(manifest.BinarySHA256); err != nil {
			return ownershipManifest{}, "ownership manifest has an invalid binary hash"
		}
	}
	return manifest, ""
}

func patchedPlugin(absBin string) ([]byte, error) {
	if strings.Count(string(plugin.OpenCodePlugin), bridgeBINMarker) != 1 {
		return nil, errors.New("embedded OpenCode adapter has an unexpected BRIDGE_BIN declaration")
	}
	replacement := bridgeBINMarker
	if absBin != "context-bridge" {
		replacement = fmt.Sprintf(`%s%q;`, ownedBridgeBINPrefix, absBin)
	}
	return []byte(strings.Replace(string(plugin.OpenCodePlugin), bridgeBINMarker, replacement, 1)), nil
}

func isRecognizedPlugin(data []byte) bool {
	normalizedSHA, ok := normalizedPluginSHA256(data)
	if !ok {
		return false
	}
	currentSHA, currentOK := normalizedPluginSHA256(plugin.OpenCodePlugin)
	return (currentOK && normalizedSHA == currentSHA) || normalizedSHA == legacyPluginSHA256
}

func normalizedPluginSHA256(data []byte) (string, bool) {
	lines := strings.Split(string(data), "\n")
	found := -1
	for i, line := range lines {
		prefix := ""
		switch {
		case strings.HasPrefix(line, legacyBridgeBINPrefix):
			prefix = legacyBridgeBINPrefix
		case strings.HasPrefix(line, ownedBridgeBINPrefix):
			prefix = ownedBridgeBINPrefix
		default:
			continue
		}
		if found != -1 || !strings.HasSuffix(line, ";") {
			return "", false
		}
		quoted := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";")
		value, err := strconv.Unquote(quoted)
		if err != nil || strings.TrimSpace(value) == "" {
			return "", false
		}
		found = i
	}
	if found == -1 {
		return "", false
	}
	lines[found] = bridgeBINMarker
	return hashBytes([]byte(strings.Join(lines, "\n"))), true
}

func inspectInstallBinary(path string, ownBinary bool) (string, string, string, error) {
	path = strings.TrimSpace(path)
	if path == "context-bridge" {
		if ownBinary {
			return "", "", "", errors.New("an installer-owned binary must use an absolute path")
		}
		return path, "", "", nil
	}
	if !filepath.IsAbs(path) {
		return "", "", "", errors.New("context-bridge binary must be the bare command or an absolute path")
	}
	cleanPath := filepath.Clean(path)
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect context-bridge binary %s: %w", cleanPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", "", fmt.Errorf("context-bridge binary must be a regular non-symlink file: %s", cleanPath)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", "", "", fmt.Errorf("context-bridge binary is not executable: %s", cleanPath)
	}
	if !ownBinary {
		return cleanPath, "", "", nil
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", "", "", fmt.Errorf("read context-bridge binary %s: %w", cleanPath, err)
	}
	return cleanPath, cleanPath, hashBytes(data), nil
}

func verifyObservedState(status IntegrationStatus) error {
	pluginData, pluginExists, unsafeReason, err := readRegularFile(status.PluginPath)
	if err != nil {
		return err
	}
	if unsafeReason != "" {
		return fmt.Errorf("adapter changed to an unsafe file: %s", unsafeReason)
	}
	if pluginExists != status.PluginExists || (pluginExists && hashBytes(pluginData) != status.PluginSHA256) {
		return errors.New("adapter changed while the integration operation was in progress")
	}

	manifestData, manifestExists, unsafeReason, err := readRegularFile(status.ManifestPath)
	if err != nil {
		return err
	}
	if unsafeReason != "" {
		return fmt.Errorf("manifest changed to an unsafe file: %s", unsafeReason)
	}
	if manifestExists != status.ManifestExists || (manifestExists && hashBytes(manifestData) != status.ManifestFileSHA256) {
		return errors.New("ownership manifest changed while the integration operation was in progress")
	}
	return nil
}

func readRegularFile(path string) ([]byte, bool, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, "", nil
		}
		return nil, false, "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, true, fmt.Sprintf("%s is a symlink", path), nil
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Sprintf("%s is not a regular file", path), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, true, "", fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, "", nil
}

func atomicWriteObserved(path string, data []byte, mode os.FileMode, expectedExists bool, expectedSHA string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := verifyFileObservation(path, expectedExists, expectedSHA); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func verifyFileObservation(path string, expectedExists bool, expectedSHA string) error {
	data, exists, unsafeReason, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if unsafeReason != "" {
		return errors.New(unsafeReason)
	}
	if exists != expectedExists || (exists && hashBytes(data) != expectedSHA) {
		return fmt.Errorf("%s changed while the integration operation was in progress", path)
	}
	return nil
}

func restoreObservedFile(path, desiredSHA string, previous []byte, previousExists bool, mode os.FileMode) error {
	current, currentExists, unsafeReason, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if unsafeReason != "" {
		return errors.New(unsafeReason)
	}
	if currentExists == previousExists && (!currentExists || hashBytes(current) == hashBytes(previous)) {
		return nil
	}
	if !currentExists || hashBytes(current) != desiredSHA {
		return fmt.Errorf("refusing rollback because %s changed after installation", path)
	}
	if previousExists {
		return atomicWriteObserved(path, previous, mode, true, desiredSHA)
	}
	return quarantineRemoveFile(path, desiredSHA)
}

func quarantineRemoveFile(path, expectedSHA string) error {
	if expectedSHA == "" {
		return fmt.Errorf("refusing to remove %s without an expected hash", path)
	}
	_, exists, unsafeReason, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if unsafeReason != "" {
		return errors.New(unsafeReason)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".remove-*")
	if err != nil {
		return err
	}
	quarantinePath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(quarantinePath)
		return err
	}
	if err := os.Remove(quarantinePath); err != nil {
		return err
	}
	if err := os.Rename(path, quarantinePath); err != nil {
		return fmt.Errorf("quarantine %s: %w", path, err)
	}

	quarantined, quarantinedExists, quarantinedUnsafe, readErr := readRegularFile(quarantinePath)
	if readErr != nil || !quarantinedExists || quarantinedUnsafe != "" || hashBytes(quarantined) != expectedSHA {
		restoreErr := restoreQuarantinedFile(path, quarantinePath)
		return fmt.Errorf("refusing to remove changed file %s (inspect=%v unsafe=%q restore=%v)", path, readErr, quarantinedUnsafe, restoreErr)
	}
	if err := os.Remove(quarantinePath); err != nil {
		restoreErr := restoreQuarantinedFile(path, quarantinePath)
		return fmt.Errorf("remove quarantined file %s: %v (restore=%v)", path, err, restoreErr)
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func restoreQuarantinedFile(path, quarantinePath string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("target was recreated; preserved quarantined file at %s", quarantinePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(quarantinePath, path)
}

func cleanAbsolutePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}
	return filepath.Clean(path), nil
}

func userConfigDir(lookupEnv func(string) (string, bool), resolve, resolveHome func() (string, error)) (string, error) {
	// Match os.UserConfigDir's native macOS behavior. On Linux, inspect
	// XDG_CONFIG_HOME directly so padded absolute values are canonicalized and
	// relative values cannot be resolved against the cwd.
	if runtime.GOOS == "linux" {
		if raw, ok := lookupEnv("XDG_CONFIG_HOME"); ok {
			path := strings.TrimSpace(raw)
			if path != "" {
				path, err := cleanAbsolutePath(path)
				if err != nil {
					return "", fmt.Errorf("XDG_CONFIG_HOME: %w", err)
				}
				return path, nil
			}
			if raw != "" {
				if resolveHome == nil {
					return "", errors.New("resolve user home directory for blank XDG_CONFIG_HOME: resolver is unavailable")
				}
				home, err := resolveHome()
				if err != nil {
					return "", fmt.Errorf("resolve user home directory for blank XDG_CONFIG_HOME: %w", err)
				}
				home, err = cleanAbsolutePath(home)
				if err != nil {
					return "", fmt.Errorf("user home directory: %w", err)
				}
				return filepath.Join(home, ".config"), nil
			}
		}
	}
	path, err := resolve()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	path, err = cleanAbsolutePath(path)
	if err != nil {
		return "", fmt.Errorf("user config directory: %w", err)
	}
	return path, nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
