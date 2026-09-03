package packaging

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		filepath.Join(root, "scripts", "deploy-from-source.sh"),
		filepath.Join(root, "scripts", "deploy-from-source.ps1"),
		filepath.Join(root, "scripts", "deploy-dashboard-from-source.sh"),
		filepath.Join(root, "scripts", "deploy-dashboard-from-source.ps1"),
		filepath.Join(root, "scripts", "enroll-dashboard.sh"),
		filepath.Join(root, "scripts", "enroll-dashboard.ps1"),
		filepath.Join(root, "scripts", "bootstrap.sh"),
		filepath.Join(root, "scripts", "bootstrap.ps1"),
		filepath.Join(root, "scripts", "rollback.sh"),
		filepath.Join(root, "scripts", "uninstall.sh"),
		filepath.Join(root, "scripts", "build-release-artifacts.sh"),
		filepath.Join(root, "packaging", "macos", "Executor Desktop.app", "Contents", "Info.plist"),
		filepath.Join(root, ".github", "workflows", "go.yml"),
		filepath.Join(root, ".github", "workflows", "release.yml"),
		filepath.Join(root, "packaging", "cmd", "executor", "main.go"),
		filepath.Join(root, "cmd", "executor-kill", "main.go"),
		filepath.Join(root, "docs", "DEPLOYMENT.md"),
		filepath.Join(root, "docs", "TROUBLESHOOTING.md"),
	}
	for _, path := range checks {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
}

func TestUnixDeploymentEntrypointsAreExecutable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable mode bits are not represented by Windows checkouts")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deploy-from-source.sh", "deploy-dashboard-from-source.sh", "enroll-dashboard.sh", "bootstrap.sh", "rollback.sh", "uninstall.sh"} {
		info, err := os.Stat(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("scripts/%s is not executable: %s", name, info.Mode().Perm())
		}
	}
}

func TestDashboardSourceDeploymentLocalValidationDoesNotMutateCloudflare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PowerShell entrypoint is validated on Windows runners")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not installed")
	}
	versionOutput, err := exec.Command(node, "--version").Output()
	if err != nil || !dashboardNodeVersionSupported(strings.TrimSpace(string(versionOutput))) {
		t.Skipf("Dashboard requires Node 20.19+, 22.13+, or 24+; current Node is %q", strings.TrimSpace(string(versionOutput)))
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	stateHome := t.TempDir()
	tokenPath := filepath.Join(t.TempDir(), "cloudflare.token")
	const tokenValue = "test-only-dashboard-api-token"
	if err := os.WriteFile(tokenPath, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(root, "scripts", "deploy-dashboard-from-source.sh"),
		"validate",
		"--hostname", "dashboard.example.test",
		"--account-id", "0123456789abcdef0123456789abcdef",
		"--api-token-file", tokenPath,
		"--allowed-email", "owner@example.test",
	)
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "XDG_STATE_HOME="+stateHome)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Dashboard local source validation failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), tokenValue) {
		t.Fatalf("Dashboard validation exposed token material: %s", output)
	}
	if !strings.Contains(string(output), "no Cloudflare changes were made") {
		t.Fatalf("Dashboard validation did not identify its non-mutating result: %s", output)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "executor", "dashboard-deployment.json")); !os.IsNotExist(err) {
		t.Fatalf("local validation created deployment state: %v", err)
	}
}

func dashboardNodeVersionSupported(version string) bool {
	version = strings.TrimPrefix(version, "v")
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major == 20 && minor >= 19 || major == 22 && minor >= 13 || major >= 24
}

func TestDeployFromSourcePreparesNativeBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix source deployment entrypoint is tested on macOS and Linux")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "deploy-from-source.sh"), "--prepare-only", bundleDir)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "EXECUTOR_MACOS_SIGN_IDENTITY=-")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare native source bundle: %v\n%s", err, output)
	}
	for _, name := range []string{
		"executor", "executor-kill", "scripts/bootstrap.sh", "scripts/enroll-dashboard.sh", "scripts/deploy-dashboard-from-source.sh",
		"dashboard/package.json", "dashboard/package-lock.json", "dashboard/wrangler.deploy.template.jsonc",
		"dashboard/scripts/deploy.mjs", "dashboard/migrations/0001_control_plane.sql", "dashboard/src/index.ts", "docs/DEPLOYMENT.md",
	} {
		if _, err := os.Stat(filepath.Join(bundleDir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("prepared bundle missing %s: %v", name, err)
		}
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(filepath.Join(bundleDir, "Executor Desktop.app", "Contents", "MacOS", "executor-desktop")); err != nil {
			t.Fatalf("prepared macOS bundle missing Desktop app: %v", err)
		}
	}
}

func TestDarwinReleaseIncludesSignedDesktopApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign verification requires macOS")
	}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "build-release-artifacts.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"OUT_DIR="+outDir,
		"EXECUTOR_BUILD_TARGETS=darwin/"+runtime.GOARCH,
		"EXECUTOR_MACOS_SIGN_IDENTITY=-",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Darwin release: %v\n%s", err, output)
	}

	archive := filepath.Join(outDir, "executor_darwin_"+runtime.GOARCH+".tar.gz")
	entries := readTarEntries(t, archive)
	assertArchiveEntries(t, entries,
		"Executor Desktop.app/Contents/Info.plist",
		"Executor Desktop.app/Contents/MacOS/executor-desktop",
		"Executor Desktop.app/Contents/_CodeSignature/CodeResources",
	)

	extractDir := t.TempDir()
	extract := exec.Command("tar", "-xzf", archive, "-C", extractDir)
	if output, err := extract.CombinedOutput(); err != nil {
		t.Fatalf("extract Darwin release: %v\n%s", err, output)
	}
	verify := exec.Command("codesign", "--verify", "--deep", "--strict", filepath.Join(extractDir, "Executor Desktop.app"))
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify desktop app signature: %v\n%s", err, output)
	}
}

func TestDarwinReleaseRunsOnMacOSForNativeDesktopEvents(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.ReplaceAll(string(mustReadFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))), "\r\n", "\n")
	if !strings.Contains(workflow, "goos: darwin\n            goarch: amd64\n            runner: macos-latest") ||
		!strings.Contains(workflow, "goos: darwin\n            goarch: arm64\n            runner: macos-latest") ||
		!strings.Contains(workflow, "runs-on: ${{ matrix.runner }}") {
		t.Fatalf("Darwin releases must be built on macOS with CGO desktop events:\n%s", workflow)
	}
}

func TestReleaseWorkflowValidatesDashboardPackageBeforeArchives(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(mustReadFile(t, filepath.Join(root, ".github", "workflows", "release.yml")))
	dashboardJob := strings.Split(workflow, "\n  build:")[0]
	for _, required := range []string{
		"actions/setup-node@v4",
		"actions/setup-go@v5",
		"go-version-file: go.mod",
		"cache-dependency-path: dashboard/package-lock.json",
		"npm ci",
		"npm test",
		"npm run check",
		"npm run build",
		"dashboard/test/deploy/windows-deployment.tests.ps1",
	} {
		if !strings.Contains(dashboardJob, required) {
			t.Fatalf("release workflow does not validate Dashboard requirement %q:\n%s", required, workflow)
		}
	}
	goWorkflow := string(mustReadFile(t, filepath.Join(root, ".github", "workflows", "go.yml")))
	goDashboardJob := strings.Split(goWorkflow, "\n  test:")[0]
	for _, required := range []string{"actions/setup-go@v5", "go-version-file: go.mod", "dashboard/test/deploy/windows-deployment.tests.ps1"} {
		if !strings.Contains(goDashboardJob, required) {
			t.Fatalf("Go workflow Dashboard job does not validate Windows source packaging requirement %q:\n%s", required, goWorkflow)
		}
	}
}

func TestDashboardWorkerSecretNamesAreNotHiddenByRepositoryIgnoreRules(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "check-ignore", "--no-index", "dashboard/.executor-secrets-regression.json")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("repository ignore rules conceal a Worker secret file: %s", output)
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("git ignore scan failed unexpectedly: %v\n%s", err, output)
	}
}

func TestBuildReleaseArtifactsIncludeExecutorAndKillBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release archive script is covered by Linux and macOS jobs")
	}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	forbiddenArtifacts := []string{
		filepath.Join(root, "scripts", ".executor-packaging-test", "runtime.token"),
		filepath.Join(root, "docs", ".executor-packaging-test", "browser-state.json"),
		filepath.Join(root, "dashboard", "scripts", ".executor-packaging-test", "deployment-state.json"),
		filepath.Join(root, "dashboard", ".executor-secrets-packaging.json"),
	}
	for _, path := range forbiddenArtifacts {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test-only runtime artifact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(root, "scripts", ".executor-packaging-test"))
		_ = os.RemoveAll(filepath.Join(root, "docs", ".executor-packaging-test"))
		_ = os.RemoveAll(filepath.Join(root, "dashboard", "scripts", ".executor-packaging-test"))
		_ = os.Remove(filepath.Join(root, "dashboard", ".executor-secrets-packaging.json"))
	})
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
	assertArchiveEntries(t, linuxEntries,
		"executor", "executor-kill", "scripts/bootstrap.sh", "scripts/deploy-from-source.sh", "scripts/deploy-dashboard-from-source.sh", "scripts/enroll-dashboard.sh",
		"dashboard/package.json", "dashboard/package-lock.json", "dashboard/wrangler.deploy.template.jsonc", "dashboard/scripts/deploy.mjs",
		"dashboard/migrations/0001_control_plane.sql", "dashboard/migrations/0002_device_tombstones.sql", "dashboard/src/index.ts",
		"docs/DEPLOYMENT.md", "docs/TROUBLESHOOTING.md", "THIRD_PARTY_NOTICES.md",
	)

	windowsEntries := readZipEntries(t, filepath.Join(outDir, "executor_windows_amd64.zip"))
	assertArchiveEntries(t, windowsEntries,
		"executor.exe", "executor-kill.exe", "scripts/bootstrap.ps1", "scripts/deploy-from-source.ps1", "scripts/deploy-dashboard-from-source.ps1", "scripts/enroll-dashboard.ps1",
		"dashboard/package.json", "dashboard/package-lock.json", "dashboard/wrangler.deploy.template.jsonc", "dashboard/scripts/deploy.mjs",
		"dashboard/migrations/0001_control_plane.sql", "dashboard/migrations/0002_device_tombstones.sql", "dashboard/src/index.ts",
		"docs/DEPLOYMENT.md", "docs/TROUBLESHOOTING.md", "THIRD_PARTY_NOTICES.md",
	)
	assertArchiveExcludes(t, linuxEntries, "dashboard/node_modules/", "dashboard/dist/", "dashboard/.wrangler/", ".playwright-cli/")
	assertArchiveExcludes(t, windowsEntries, "dashboard/node_modules/", "dashboard/dist/", "dashboard/.wrangler/", ".playwright-cli/")
	for _, entries := range [][]string{linuxEntries, windowsEntries} {
		for _, forbidden := range []string{"runtime.token", "browser-state.json", "deployment-state.json", ".executor-secrets-packaging.json"} {
			for _, entry := range entries {
				if strings.HasSuffix(entry, "/"+forbidden) || entry == forbidden {
					t.Fatalf("archive contains untracked runtime artifact %q", entry)
				}
			}
		}
	}

	sums := string(mustReadFile(t, filepath.Join(outDir, "SHA256SUMS.txt")))
	if strings.Contains(sums, outDir) {
		t.Fatalf("SHA256SUMS.txt contains builder-specific output path %q:\n%s", outDir, sums)
	}
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

func assertArchiveExcludes(t *testing.T, entries []string, forbiddenPrefixes ...string) {
	t.Helper()
	for _, entry := range entries {
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(entry, prefix) || strings.Contains(entry, "/"+prefix) {
				t.Fatalf("archive contains forbidden generated entry %q", entry)
			}
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

func TestUnifiedDashboardDocsCoverCloneSafeOwnerWorkflow(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"README.md": {
			"scripts/deploy-dashboard-from-source.sh",
			"Cloudflare Access",
			"30-day",
			"emergency rescue",
			"web ChatGPT cannot",
		},
		"docs/DEPLOYMENT.md": {
			"--hostname",
			"--api-token-file",
			"--allowed-email",
			"Workers Scripts",
			"D1",
			"Workers Routes",
			"Access: Apps and Policies",
			"dashboard-enrollment-token-temporary",
			"rotate-enrollment",
			"disable-enrollment",
		},
		"docs/TROUBLESHOOTING.md": {
			"ACCESS_AUD",
			"ACCESS_TEAM_DOMAIN",
			"custom_domain: true",
			"D1 migration",
			"WebSocket",
			"localhost rescue",
			"Executor-owned",
		},
	}
	for relativePath, required := range checks {
		body := string(mustReadFile(t, filepath.Join(root, filepath.FromSlash(relativePath))))
		for _, phrase := range required {
			if !strings.Contains(body, phrase) {
				t.Fatalf("%s does not explain %q", relativePath, phrase)
			}
		}
	}
}
