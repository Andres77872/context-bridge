// Package update implements the self-update flow for the context-bridge
// binary. It mirrors the hosted installer's semantics: download the requested
// GitHub release, verify its checksum, publish the verified artifact
// atomically next to the current executable, and re-run integration
// installation from the new binary. If integration installation fails, the
// previous binary is restored.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"context-bridge/internal/opencode"
)

const (
	defaultRepo         = "Andres77872/context-bridge"
	binaryName          = "context-bridge"
	maxChecksumsBytes   = 1 << 20   // 1 MiB
	maxArchiveBytes     = 256 << 20 // 256 MiB
	maxBinaryBytes      = 512 << 20 // 512 MiB
	maxAPIResponseBytes = 1 << 20   // 1 MiB
)

var (
	tagPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	checksumPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

// Options configures a self-update run. Zero values select production
// defaults; the function fields exist so tests can avoid network, process,
// and manifest access.
type Options struct {
	// TargetVersion is a release tag such as "v0.3.0" (the leading "v" is
	// optional). Empty or "latest" resolves the newest published release.
	TargetVersion string
	// CheckOnly reports whether an update is available without installing.
	CheckOnly bool
	// CurrentVersion is the running binary's version string.
	CurrentVersion string

	Stdout io.Writer
	Stderr io.Writer

	// Repo is the GitHub "owner/name" to download releases from.
	Repo string
	// APIBaseURL overrides https://api.github.com (tests).
	APIBaseURL string
	// DownloadBaseURL overrides https://github.com (tests).
	DownloadBaseURL string
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
	// ExecutablePath resolves the binary to replace (tests).
	ExecutablePath func() (string, error)
	// OSName and ArchName override runtime.GOOS / runtime.GOARCH (tests).
	OSName   string
	ArchName string
	// OwnedBinary reports whether path is claimed by the integration
	// ownership manifest (tests).
	OwnedBinary func(path string) (bool, string, error)
	// RunIntegration runs `<binary> integration install` from the newly
	// published binary (tests).
	RunIntegration func(binaryPath string, owned bool, stdout, stderr io.Writer) error
}

func (o *Options) fillDefaults() error {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.Repo) == "" {
		o.Repo = defaultRepo
	}
	owner, name, ok := strings.Cut(o.Repo, "/")
	if !ok || !tagPattern.MatchString(owner) || !tagPattern.MatchString(name) {
		return fmt.Errorf("release repository must use owner/name form: %q", o.Repo)
	}
	if o.APIBaseURL == "" {
		o.APIBaseURL = "https://api.github.com"
	}
	if o.DownloadBaseURL == "" {
		o.DownloadBaseURL = "https://github.com"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.ExecutablePath == nil {
		o.ExecutablePath = defaultExecutablePath
	}
	if o.OSName == "" {
		o.OSName = runtime.GOOS
	}
	if o.ArchName == "" {
		o.ArchName = runtime.GOARCH
	}
	if o.OwnedBinary == nil {
		o.OwnedBinary = func(path string) (bool, string, error) {
			return opencode.OwnedBinary(path)
		}
	}
	if o.RunIntegration == nil {
		o.RunIntegration = runIntegrationInstall
	}
	return nil
}

// Run executes the self-update flow described in the package comment.
func Run(opts Options) error {
	if err := opts.fillDefaults(); err != nil {
		return err
	}
	if opts.OSName != "linux" && opts.OSName != "darwin" {
		return fmt.Errorf("self-update supports published linux and darwin releases; current OS is %s", opts.OSName)
	}
	if opts.ArchName != "amd64" && opts.ArchName != "arm64" {
		return fmt.Errorf("self-update supports published amd64 and arm64 releases; current architecture is %s", opts.ArchName)
	}

	binaryPath, err := opts.ExecutablePath()
	if err != nil {
		return err
	}
	if err := validateReplaceTarget(binaryPath); err != nil {
		return err
	}

	tag, err := resolveTargetTag(opts)
	if err != nil {
		return err
	}

	current := normalizeVersion(opts.CurrentVersion)
	target := normalizeVersion(tag)
	if opts.CheckOnly {
		if current == target {
			_, err := fmt.Fprintf(opts.Stdout, "context-bridge %s is up to date (latest release: %s)\n", opts.CurrentVersion, tag)
			return err
		}
		_, err := fmt.Fprintf(opts.Stdout, "update available: %s -> %s\nRun `context-bridge update` to install it.\n", opts.CurrentVersion, tag)
		return err
	}
	if current == target {
		_, err := fmt.Fprintf(opts.Stdout, "context-bridge %s is already up to date\n", opts.CurrentVersion)
		return err
	}

	fmt.Fprintf(opts.Stderr, "Target version:  %s\n", tag)
	fmt.Fprintf(opts.Stderr, "Install path:    %s\n", binaryPath)

	workdir, err := os.MkdirTemp("", "context-bridge-update-")
	if err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	defer os.RemoveAll(workdir)

	archivePath, err := downloadAndVerifyArchive(opts, tag, target, workdir)
	if err != nil {
		return err
	}
	newBinary, err := extractBinary(archivePath, workdir)
	if err != nil {
		return err
	}

	owned, _, err := opts.OwnedBinary(binaryPath)
	if err != nil {
		return fmt.Errorf("inspect binary ownership: %w", err)
	}

	if err := publishAndIntegrate(opts, newBinary, binaryPath, owned); err != nil {
		return err
	}
	_, err = fmt.Fprintf(opts.Stdout, "Updated context-bridge %s -> %s\nVerify: context-bridge version\n", opts.CurrentVersion, tag)
	return err
}

func defaultExecutablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	return filepath.Clean(executable), nil
}

func validateReplaceTarget(binaryPath string) error {
	if !filepath.IsAbs(binaryPath) {
		return fmt.Errorf("executable path must be absolute: %q", binaryPath)
	}
	info, err := os.Lstat(binaryPath)
	if err != nil {
		return fmt.Errorf("inspect current binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink at %s", binaryPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular file at %s", binaryPath)
	}
	return nil
}

func resolveTargetTag(opts Options) (string, error) {
	requested := strings.TrimSpace(opts.TargetVersion)
	if requested != "" && requested != "latest" {
		if !strings.HasPrefix(requested, "v") {
			requested = "v" + requested
		}
		if !tagPattern.MatchString(requested) {
			return "", fmt.Errorf("release tag contains unsupported characters: %q", requested)
		}
		return requested, nil
	}

	url := fmt.Sprintf("%s/repos/%s/releases/latest", opts.APIBaseURL, opts.Repo)
	body, err := httpGet(opts.HTTPClient, url, maxAPIResponseBytes)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("decode latest release metadata: %w", err)
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" || !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("could not determine latest release tag from %s", url)
	}
	return tag, nil
}

func normalizeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

func httpGet(client *http.Client, url string, limit int64) ([]byte, error) {
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", url, limit)
	}
	return body, nil
}

func downloadAndVerifyArchive(opts Options, tag, version, workdir string) (string, error) {
	archiveName := fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, version, opts.OSName, opts.ArchName)
	checksumsName := fmt.Sprintf("%s_%s_checksums.txt", binaryName, version)
	baseURL := fmt.Sprintf("%s/%s/releases/download/%s", opts.DownloadBaseURL, opts.Repo, tag)

	archive, err := httpGet(opts.HTTPClient, baseURL+"/"+archiveName, maxArchiveBytes)
	if err != nil {
		return "", fmt.Errorf("download release archive: %w", err)
	}
	checksums, err := httpGet(opts.HTTPClient, baseURL+"/"+checksumsName, maxChecksumsBytes)
	if err != nil {
		return "", fmt.Errorf("download release checksums: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return "", err
	}
	if actual := hashBytes(archive); actual != expected {
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", archiveName, expected, actual)
	}
	fmt.Fprintln(opts.Stderr, "Checksum verified")

	archivePath := filepath.Join(workdir, archiveName)
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		return "", fmt.Errorf("stage release archive: %w", err)
	}
	return archivePath, nil
}

func checksumFor(checksums []byte, archiveName string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(checksums)))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		if name != archiveName {
			continue
		}
		if !checksumPattern.MatchString(fields[0]) {
			return "", fmt.Errorf("malformed checksum entry for %s", archiveName)
		}
		return strings.ToLower(fields[0]), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("parse checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum entry for %s", archiveName)
}

func extractBinary(archivePath, workdir string) (string, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open release archive: %w", err)
	}
	defer archive.Close()

	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("decompress release archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("could not find %s in release archive", binaryName)
		}
		if err != nil {
			return "", fmt.Errorf("read release archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		binaryPath := filepath.Join(workdir, binaryName)
		file, err := os.OpenFile(binaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			return "", fmt.Errorf("stage extracted binary: %w", err)
		}
		written, err := io.Copy(file, io.LimitReader(tarReader, maxBinaryBytes+1))
		closeErr := file.Close()
		if err != nil {
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if closeErr != nil {
			return "", fmt.Errorf("finalize extracted binary: %w", closeErr)
		}
		if written == 0 {
			return "", errors.New("extracted binary is empty")
		}
		if written > maxBinaryBytes {
			return "", fmt.Errorf("extracted binary exceeds %d bytes", maxBinaryBytes)
		}
		return binaryPath, nil
	}
}

func publishAndIntegrate(opts Options, newBinary, binaryPath string, owned bool) error {
	installDir := filepath.Dir(binaryPath)
	candidate, err := os.CreateTemp(installDir, "."+binaryName+".tmp.")
	if err != nil {
		return fmt.Errorf("stage install candidate: %w", err)
	}
	candidatePath := candidate.Name()
	removeCandidate := true
	defer func() {
		if removeCandidate {
			_ = os.Remove(candidatePath)
		}
	}()
	source, err := os.Open(newBinary)
	if err != nil {
		_ = candidate.Close()
		return fmt.Errorf("open extracted binary: %w", err)
	}
	_, copyErr := io.Copy(candidate, source)
	_ = source.Close()
	if closeErr := candidate.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("write install candidate: %w", copyErr)
	}
	if err := os.Chmod(candidatePath, 0o755); err != nil {
		return fmt.Errorf("mark install candidate executable: %w", err)
	}

	// Re-validate immediately before publishing: the target must still be a
	// regular file so the atomic rename cannot follow a racing symlink swap.
	if err := validateReplaceTarget(binaryPath); err != nil {
		return err
	}
	previous, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("back up current binary: %w", err)
	}
	previousInfo, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("inspect current binary: %w", err)
	}

	if err := os.Rename(candidatePath, binaryPath); err != nil {
		return fmt.Errorf("publish updated binary: %w", err)
	}
	removeCandidate = false

	restore := func() {
		if writeErr := os.WriteFile(binaryPath, previous, previousInfo.Mode().Perm()); writeErr != nil {
			fmt.Fprintf(opts.Stderr, "CRITICAL: failed to restore previous binary at %s: %v\n", binaryPath, writeErr)
		}
	}
	if err := opts.RunIntegration(binaryPath, owned, opts.Stderr, opts.Stderr); err != nil {
		restore()
		return fmt.Errorf("OpenCode integration install failed; the previous binary was restored: %w", err)
	}
	return nil
}

func runIntegrationInstall(binaryPath string, owned bool, stdout, stderr io.Writer) error {
	args := []string{"integration", "install"}
	if owned {
		args = append(args, "--owned-binary")
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
