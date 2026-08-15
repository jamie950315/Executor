package filesystem

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalService_UnrestrictedOperations(t *testing.T) {
	root := t.TempDir()
	alphaDir := filepath.Join(root, "alpha")
	nestedDir := filepath.Join(root, "nested")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatalf("seed alpha/one.txt: %v", err)
	}
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	svc := NewLocalService()

	if err := svc.WriteFile(filepath.Join(nestedDir, "two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := svc.AppendFile(filepath.Join(nestedDir, "two.txt"), []byte("-more"), 0o600); err != nil {
		t.Fatalf("append file: %v", err)
	}
	if err := svc.Mkdir(filepath.Join(root, "created", "leaf"), 0o755); err != nil {
		t.Fatalf("mkdir path: %v", err)
	}

	data, err := svc.ReadFile(filepath.Join(alphaDir, "one.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "one" {
		t.Fatalf("unexpected read contents: %q", string(data))
	}

	entries, err := svc.List(root)
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 root entries, got %d", len(entries))
	}

	matches, err := svc.Glob(filepath.Join(root, "*", "*.txt"))
	if err != nil {
		t.Fatalf("glob txt files: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 glob matches, got %d", len(matches))
	}

	info, err := svc.Stat(filepath.Join(nestedDir, "two.txt"))
	if err != nil {
		t.Fatalf("stat nested/two.txt: %v", err)
	}
	if info.Size != 8 || runtime.GOOS != "windows" && info.Mode.Perm() != 0o600 {
		t.Fatalf("unexpected stat: %#v", info)
	}

	appended, err := svc.ReadFile(filepath.Join(nestedDir, "two.txt"))
	if err != nil {
		t.Fatalf("read appended file: %v", err)
	}
	if string(appended) != "two-more" {
		t.Fatalf("unexpected appended contents: %q", string(appended))
	}

	dst := filepath.Join(root, "moved.txt")
	if err := svc.Move(filepath.Join(nestedDir, "two.txt"), dst); err != nil {
		t.Fatalf("move file: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("expected moved file at destination: %v", err)
	}

	if err := svc.Delete(dst); err != nil {
		t.Fatalf("delete moved file: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("expected deleted file to be absent, got err=%v", err)
	}
	if stat, err := os.Stat(filepath.Join(root, "created", "leaf")); err != nil || !stat.IsDir() {
		t.Fatalf("expected created directory to exist, stat=%v err=%v", stat, err)
	}
}
