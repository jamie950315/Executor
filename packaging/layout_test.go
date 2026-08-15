package packaging

import (
	"os"
	"path/filepath"
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
		filepath.Join(root, "docs", "DEPLOYMENT.md"),
	}
	for _, path := range checks {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
}
