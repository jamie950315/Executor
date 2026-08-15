package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreatePersistsPrivateSecretsAndReturnsRecoveryOnce(t *testing.T) {
	dir := t.TempDir()
	created, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.RecoveryKey) < 32 || len(created.URLSecret) < 32 || len(created.IPCKey) < 32 {
		t.Fatalf("generated secrets are too short: %#v", created)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RecoveryKey != created.RecoveryKey || loaded.URLSecret != created.URLSecret || loaded.IPCKey != created.IPCKey {
		t.Fatal("loaded secrets differ from created secrets")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, secretsFile))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("secret mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestRotateChangesEveryCredential(t *testing.T) {
	dir := t.TempDir()
	before, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Rotate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.RecoveryKey == after.RecoveryKey || before.URLSecret == after.URLSecret || before.IPCKey == after.IPCKey {
		t.Fatal("Rotate reused a credential")
	}
}
