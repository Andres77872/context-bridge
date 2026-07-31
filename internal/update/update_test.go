package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type releaseFixture struct {
	t             *testing.T
	tag           string
	binaryContent []byte
	server        *httptest.Server
	apiHits       int
	downloadHits  int
	badChecksum   bool
}

func newReleaseFixture(t *testing.T, tag string, binaryContent []byte) *releaseFixture {
	t.Helper()
	fx := &releaseFixture{t: t, tag: tag, binaryContent: binaryContent}
	mux := http.NewServeMux()
	version := strings.TrimPrefix(tag, "v")
	archiveName := fmt.Sprintf("context-bridge_%s_linux_amd64.tar.gz", version)
	checksumsName := fmt.Sprintf("context-bridge_%s_checksums.txt", version)

	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fx.apiHits++
		fmt.Fprintf(w, `{"tag_name": %q}`, fx.tag)
	})
	mux.HandleFunc("/owner/repo/releases/download/"+tag+"/"+archiveName, func(w http.ResponseWriter, _ *http.Request) {
		fx.downloadHits++
		_, _ = w.Write(fx.archive())
	})
	mux.HandleFunc("/owner/repo/releases/download/"+tag+"/"+checksumsName, func(w http.ResponseWriter, _ *http.Request) {
		sum := hashBytes(fx.archive())
		if fx.badChecksum {
			sum = strings.Repeat("0", 64)
		}
		fmt.Fprintf(w, "%s  %s\n%s  context-bridge_%s_darwin_arm64.tar.gz\n", sum, archiveName, strings.Repeat("1", 64), version)
	})
	fx.server = httptest.NewServer(mux)
	t.Cleanup(fx.server.Close)
	return fx
}

func (fx *releaseFixture) archive() []byte {
	fx.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "README.md", body: []byte("docs")},
		{name: "context-bridge", body: fx.binaryContent},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}); err != nil {
			fx.t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			fx.t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		fx.t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		fx.t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func (fx *releaseFixture) options(binaryPath string, integration *[]string) Options {
	fx.t.Helper()
	return Options{
		CurrentVersion:  "v0.1.0",
		Repo:            "owner/repo",
		APIBaseURL:      fx.server.URL,
		DownloadBaseURL: fx.server.URL,
		HTTPClient:      fx.server.Client(),
		Stdout:          &bytes.Buffer{},
		Stderr:          &bytes.Buffer{},
		OSName:          "linux",
		ArchName:        "amd64",
		ExecutablePath:  func() (string, error) { return binaryPath, nil },
		OwnedBinary:     func(string) (bool, string, error) { return false, "", nil },
		RunIntegration: func(path string, owned bool, _, _ io.Writer) error {
			*integration = append(*integration, fmt.Sprintf("%s owned=%v", path, owned))
			return nil
		},
	}
}

func installedTestBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "context-bridge")
	if err := os.WriteFile(binaryPath, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("seed installed binary: %v", err)
	}
	return binaryPath
}

func TestRunReplacesBinaryAndRunsIntegration(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("new binary payload"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	stdout := &bytes.Buffer{}
	opts.Stdout = stdout

	if err := Run(opts); err != nil {
		t.Fatalf("run update: %v", err)
	}
	replaced, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read replaced binary: %v", err)
	}
	if string(replaced) != "new binary payload" {
		t.Fatalf("binary was not replaced: %q", replaced)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat replaced binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected replaced binary mode 0755, got %04o", info.Mode().Perm())
	}
	if len(integration) != 1 || integration[0] != binaryPath+" owned=false" {
		t.Fatalf("unexpected integration calls: %v", integration)
	}
	if !strings.Contains(stdout.String(), "Updated context-bridge v0.1.0 -> v0.2.0") {
		t.Fatalf("expected update confirmation, got %q", stdout.String())
	}
	entries, err := os.ReadDir(filepath.Dir(binaryPath))
	if err != nil {
		t.Fatalf("read install dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".context-bridge.") {
			t.Fatalf("temporary artifact left behind: %s", entry.Name())
		}
	}
}

func TestRunPreservesManifestOwnership(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("new binary payload"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	opts.OwnedBinary = func(path string) (bool, string, error) {
		if path != binaryPath {
			t.Fatalf("ownership checked for unexpected path %q", path)
		}
		return true, "sha", nil
	}

	if err := Run(opts); err != nil {
		t.Fatalf("run update: %v", err)
	}
	if len(integration) != 1 || integration[0] != binaryPath+" owned=true" {
		t.Fatalf("expected owned integration install, got %v", integration)
	}
}

func TestRunAlreadyUpToDateSkipsDownload(t *testing.T) {
	fx := newReleaseFixture(t, "v0.1.0", []byte("unused"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	stdout := &bytes.Buffer{}
	opts.Stdout = stdout

	if err := Run(opts); err != nil {
		t.Fatalf("run update: %v", err)
	}
	if fx.downloadHits != 0 {
		t.Fatalf("expected no downloads for an up-to-date binary, got %d", fx.downloadHits)
	}
	if !strings.Contains(stdout.String(), "already up to date") {
		t.Fatalf("expected up-to-date message, got %q", stdout.String())
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "old binary" {
		t.Fatalf("binary changed during no-op update: %q", content)
	}
	if len(integration) != 0 {
		t.Fatalf("integration must not run for a no-op update: %v", integration)
	}
}

func TestRunCheckOnlyReportsWithoutInstalling(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("unused"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	opts.CheckOnly = true
	stdout := &bytes.Buffer{}
	opts.Stdout = stdout

	if err := Run(opts); err != nil {
		t.Fatalf("run check: %v", err)
	}
	if !strings.Contains(stdout.String(), "update available: v0.1.0 -> v0.2.0") {
		t.Fatalf("expected availability report, got %q", stdout.String())
	}
	if fx.downloadHits != 0 || len(integration) != 0 {
		t.Fatalf("check-only run must not download or integrate (downloads=%d integrations=%v)", fx.downloadHits, integration)
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "old binary" {
		t.Fatalf("binary changed during check-only run: %q", content)
	}
}

func TestRunRejectsChecksumMismatch(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("new binary payload"))
	fx.badChecksum = true
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)

	err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "old binary" {
		t.Fatalf("binary changed after failed verification: %q", content)
	}
	if len(integration) != 0 {
		t.Fatalf("integration must not run after failed verification: %v", integration)
	}
}

func TestRunRestoresPreviousBinaryWhenIntegrationFails(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("new binary payload"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	opts.RunIntegration = func(string, bool, io.Writer, io.Writer) error {
		return fmt.Errorf("plugin conflict")
	}

	err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "previous binary was restored") {
		t.Fatalf("expected integration failure with restore, got %v", err)
	}
	content, readErr := os.ReadFile(binaryPath)
	if readErr != nil {
		t.Fatalf("read restored binary: %v", readErr)
	}
	if string(content) != "old binary" {
		t.Fatalf("previous binary was not restored: %q", content)
	}
}

func TestRunRefusesSymlinkExecutable(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("unused"))
	dir := t.TempDir()
	target := filepath.Join(dir, "real-binary")
	if err := os.WriteFile(target, []byte("real"), 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(dir, "context-bridge")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var integration []string
	opts := fx.options(link, &integration)

	err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func TestRunNormalizesExplicitVersion(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("new binary payload"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	opts.TargetVersion = "0.2.0"

	if err := Run(opts); err != nil {
		t.Fatalf("run pinned update: %v", err)
	}
	if fx.apiHits != 0 {
		t.Fatalf("pinned version must not query the latest-release API, got %d hits", fx.apiHits)
	}
	content, _ := os.ReadFile(binaryPath)
	if string(content) != "new binary payload" {
		t.Fatalf("pinned update did not install: %q", content)
	}
}

func TestRunRejectsUnsupportedPlatform(t *testing.T) {
	fx := newReleaseFixture(t, "v0.2.0", []byte("unused"))
	binaryPath := installedTestBinary(t)
	var integration []string
	opts := fx.options(binaryPath, &integration)
	opts.OSName = "windows"

	err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "linux and darwin") {
		t.Fatalf("expected unsupported platform error, got %v", err)
	}
}

func TestChecksumForMatchesEntryVariants(t *testing.T) {
	sum := strings.Repeat("a", 64)
	checksums := []byte(sum + "  ./context-bridge_0.2.0_linux_amd64.tar.gz\n")
	got, err := checksumFor(checksums, "context-bridge_0.2.0_linux_amd64.tar.gz")
	if err != nil || got != sum {
		t.Fatalf("expected ./-prefixed entry to match, got %q, %v", got, err)
	}
	if _, err := checksumFor([]byte("nonsense\n"), "missing.tar.gz"); err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Fatalf("expected missing entry error, got %v", err)
	}
	if _, err := checksumFor([]byte("zz  context-bridge_0.2.0_linux_amd64.tar.gz\n"), "context-bridge_0.2.0_linux_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "malformed checksum") {
		t.Fatalf("expected malformed checksum error, got %v", err)
	}
}
