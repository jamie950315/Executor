package packaging

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagingScaffoldExists(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	checks := []string{
		filepath.Join(root, "scripts", "bootstrap.sh"),
		filepath.Join(root, "scripts", "bootstrap.ps1"),
		filepath.Join(root, "scripts", "rollback.sh"),
		filepath.Join(root, "scripts", "uninstall.sh"),
		filepath.Join(root, "scripts", "build-release-artifacts.sh"),
		filepath.Join(root, ".github", "workflows", "go.yml"),
		filepath.Join(root, ".github", "workflows", "release.yml"),
		filepath.Join(root, "packaging", "cmd", "executor", "main.go"),
		filepath.Join(root, "cmd", "executor-kill", "main.go"),
		filepath.Join(root, "docs", "DEPLOYMENT.md"),
	}
	for _, path := range checks {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
}

func TestDarwinReleaseRunsOnMacOSForNativeDesktopEvents(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(mustReadFile(t, filepath.Join(root, ".github", "workflows", "release.yml")))
	if !strings.Contains(workflow, "goos: darwin\n            goarch: amd64\n            runner: macos-latest") ||
		!strings.Contains(workflow, "goos: darwin\n            goarch: arm64\n            runner: macos-latest") ||
		!strings.Contains(workflow, "runs-on: ${{ matrix.runner }}") {
		t.Fatalf("Darwin releases must be built on macOS with CGO desktop events:\n%s", workflow)
	}
}

func TestBuildReleaseArtifactsIncludeExecutorAndKillBinaries(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "build-release-artifacts.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"OUT_DIR="+outDir,
		"EXECUTOR_BUILD_TARGETS=linux/amd64 windows/amd64",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build-release-artifacts.sh failed: %v\n%s", err, string(output))
	}

	linuxEntries := readTarEntries(t, filepath.Join(outDir, "executor_linux_amd64.tar.gz"))
	assertArchiveEntries(t, linuxEntries, "executor", "executor-kill", "scripts/bootstrap.sh", "docs/DEPLOYMENT.md", "THIRD_PARTY_NOTICES.md")

	windowsEntries := readZipEntries(t, filepath.Join(outDir, "executor_windows_amd64.zip"))
	assertArchiveEntries(t, windowsEntries, "executor.exe", "executor-kill.exe", "scripts/bootstrap.ps1", "docs/DEPLOYMENT.md", "THIRD_PARTY_NOTICES.md")

	sums := string(mustReadFile(t, filepath.Join(outDir, "SHA256SUMS.txt")))
	for _, want := range []string{
		"executor_linux_amd64.tar.gz",
		"executor_windows_amd64.zip",
	} {
		if !strings.Contains(sums, want) {
			t.Fatalf("SHA256SUMS.txt missing %q:\n%s", want, sums)
		}
	}
}

func readTarEntries(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open tar: %v", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var entries []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("tar.Next: %v", err)
		}
		entries = append(entries, strings.TrimPrefix(hdr.Name, "./"))
	}
	return entries
}

func readZipEntries(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader: %v", err)
	}
	defer zr.Close()
	entries := make([]string, 0, len(zr.File))
	for _, file := range zr.File {
		entries = append(entries, strings.TrimPrefix(file.Name, "./"))
	}
	return entries
}

func assertArchiveEntries(t *testing.T, entries []string, wants ...string) {
	t.Helper()
	body := strings.Join(entries, "\n")
	for _, want := range wants {
		found := false
		for _, entry := range entries {
			if entry == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("archive missing %q:\n%s", want, body)
		}
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func TestDeploymentDocsDescribeDashboardServiceBundles(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "docs", "DEPLOYMENT.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"LaunchDaemon for `executor dashboard`",
		"Linux systemd unit for `dashboard`",
		"Windows PowerShell scripts for Executor service install",
		"`ExecutorDashboard`",
		"stop managed services",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("DEPLOYMENT.md missing %q:\n%s", want, text)
		}
	}
}
