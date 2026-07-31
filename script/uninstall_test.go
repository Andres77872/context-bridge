package script

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestHostedUninstallScriptUsesTemporaryReleaseBinary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("hosted uninstall script test only supports unix-like systems")
	}

	workdir := t.TempDir()
	fixturesDir := filepath.Join(workdir, "fixtures")
	binDir := filepath.Join(workdir, "bin")
	logsDir := filepath.Join(workdir, "logs")
	for _, dir := range []string{fixturesDir, binDir, logsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	archiveName, checksumsName := releaseAssetNames(t)
	invokedArgs := filepath.Join(logsDir, "invoked-args.txt")
	excludePath := filepath.Join(logsDir, "exclude-path.txt")
	pathBinaryMarker := filepath.Join(logsDir, "path-binary-used.txt")

	archivePath := filepath.Join(fixturesDir, archiveName)
	if err := writeHostedArchive(archivePath, invokedArgs, excludePath); err != nil {
		t.Fatalf("write hosted archive: %v", err)
	}
	if err := writeChecksumsFile(archivePath, filepath.Join(fixturesDir, checksumsName)); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	if err := writeCurlStub(filepath.Join(binDir, "curl"), fixturesDir); err != nil {
		t.Fatalf("write curl stub: %v", err)
	}
	if err := writePathBinaryStub(filepath.Join(binDir, "context-bridge"), pathBinaryMarker); err != nil {
		t.Fatalf("write path binary stub: %v", err)
	}

	cmd := exec.Command("bash", "uninstall.sh", "--mode=preserve-data", "--yes")
	cmd.Dir = filepath.Join(repoRoot(t), "script")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VERSION=1.2.3",
		"REPO=acme/context-bridge",
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("run hosted uninstall script: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	argsData, err := os.ReadFile(invokedArgs)
	if err != nil {
		t.Fatalf("read invoked args: %v", err)
	}
	if got := strings.TrimSpace(string(argsData)); got != "uninstall --mode=preserve-data --yes" {
		t.Fatalf("expected hosted script to invoke temporary binary with uninstall args, got %q", got)
	}

	excludeData, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude path: %v", err)
	}
	resolvedExclude := strings.TrimSpace(string(excludeData))
	if resolvedExclude == "" {
		t.Fatalf("expected hosted script to set CONTEXT_BRIDGE_UNINSTALL_EXCLUDE_PATH")
	}
	if !strings.Contains(resolvedExclude, "tmp") || !strings.HasSuffix(resolvedExclude, "context-bridge") {
		t.Fatalf("expected exclude path to point at extracted temp binary, got %q", resolvedExclude)
	}

	if _, err := os.Stat(pathBinaryMarker); err == nil {
		t.Fatalf("expected hosted uninstall to avoid PATH context-bridge binary")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat PATH binary marker: %v", err)
	}
	if !strings.Contains(stderr.String(), "Running hosted uninstall with temporary context-bridge v1.2.3") {
		t.Fatalf("expected hosted uninstall log message, stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Checksum verified") {
		t.Fatalf("expected hosted uninstall checksum verification log, stderr=%q", stderr.String())
	}
}

func TestHostedUninstallDryRunDoesNotRequireTTY(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("hosted uninstall script test only supports unix-like systems")
	}

	workdir := t.TempDir()
	fixturesDir := filepath.Join(workdir, "fixtures")
	binDir := filepath.Join(workdir, "bin")
	logsDir := filepath.Join(workdir, "logs")
	for _, dir := range []string{fixturesDir, binDir, logsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	archiveName, checksumsName := releaseAssetNames(t)
	invokedArgs := filepath.Join(logsDir, "invoked-args.txt")
	excludePath := filepath.Join(logsDir, "exclude-path.txt")
	archivePath := filepath.Join(fixturesDir, archiveName)
	if err := writeHostedArchive(archivePath, invokedArgs, excludePath); err != nil {
		t.Fatalf("write hosted archive: %v", err)
	}
	if err := writeChecksumsFile(archivePath, filepath.Join(fixturesDir, checksumsName)); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	if err := writeCurlStub(filepath.Join(binDir, "curl"), fixturesDir); err != nil {
		t.Fatalf("write curl stub: %v", err)
	}

	cmd := exec.Command("bash", "uninstall.sh", "--dry-run")
	cmd.Dir = filepath.Join(repoRoot(t), "script")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VERSION=1.2.3",
		"REPO=acme/context-bridge",
	)
	cmd.Stdin = strings.NewReader("")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("run hosted dry-run without a TTY: %v\nstderr:\n%s", err, stderr.String())
	}
	argsData, err := os.ReadFile(invokedArgs)
	if err != nil {
		t.Fatalf("read invoked args: %v", err)
	}
	if got := strings.TrimSpace(string(argsData)); got != "uninstall --dry-run" {
		t.Fatalf("expected hosted dry-run to reach the temporary binary, got %q", got)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

func releaseAssetNames(t *testing.T) (string, string) {
	t.Helper()

	var osName string
	switch runtime.GOOS {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "darwin"
	default:
		t.Fatalf("unsupported GOOS %q", runtime.GOOS)
	}

	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	default:
		t.Fatalf("unsupported GOARCH %q", runtime.GOARCH)
	}

	return fmt.Sprintf("context-bridge_%s_%s_%s.tar.gz", "1.2.3", osName, arch), "context-bridge_1.2.3_checksums.txt"
}

func writeHostedArchive(archivePath, invokedArgsPath, excludePathPath string) error {
	file, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	binary := fmt.Sprintf("#!/usr/bin/env bash\nset -eu\nprintf '%%s\\n' \"$*\" > %q\nprintf '%%s\\n' \"${CONTEXT_BRIDGE_UNINSTALL_EXCLUDE_PATH:-}\" > %q\n", invokedArgsPath, excludePathPath)
	header := &tar.Header{
		Name: "context-bridge",
		Mode: 0755,
		Size: int64(len(binary)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(binary)); err != nil {
		return err
	}
	return nil
}

func writeChecksumsFile(archivePath, checksumsPath string) error {
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	content := fmt.Sprintf("%x  %s\n", sum, filepath.Base(archivePath))
	return os.WriteFile(checksumsPath, []byte(content), 0755)
}

func writeCurlStub(path, fixturesDir string) error {
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -eu
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    -H)
      shift 2
      ;;
    -f|-s|-S|-L|-fsSL|-q)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
src=%q/$(basename "$url")
if [ -n "$out" ]; then
  cp "$src" "$out"
else
  cat "$src"
fi
`, fixturesDir)
	return os.WriteFile(path, []byte(script), 0755)
}

func writePathBinaryStub(path, marker string) error {
	script := fmt.Sprintf("#!/usr/bin/env bash\nset -eu\nprintf 'used\\n' > %q\nexit 99\n", marker)
	return os.WriteFile(path, []byte(script), 0755)
}
