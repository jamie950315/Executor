package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestCreatePersistsOnlyCredentialHashesAndReturnsRecoveryOnce(t *testing.T) {
	dir := t.TempDir()
	created, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.RecoveryKey) < 32 || len(created.URLSecret) < 32 || len(created.BrokerIPCKey) < 32 || len(created.DesktopIPCKey) < 32 || len(created.DashboardKey) < 32 {
		t.Fatalf("generated secrets are too short: %#v", created)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.RecoveryKey != "" || loaded.URLSecret != "" {
		t.Fatal("one-time credentials were persisted in plaintext")
	}
	if !loaded.VerifyRecoveryKey(created.RecoveryKey) || !loaded.VerifyURLSecret(created.URLSecret) {
		t.Fatal("persisted hashes do not verify one-time credentials")
	}
	if loaded.BrokerIPCKey != created.BrokerIPCKey || loaded.DesktopIPCKey != created.DesktopIPCKey || loaded.OAuthKey != created.OAuthKey || loaded.DashboardKey != created.DashboardKey {
		t.Fatal("service keys differ from created keys")
	}
	if created.BrokerIPCKey == created.DesktopIPCKey {
		t.Fatal("broker and desktop IPC reused a credential")
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
	if before.RecoveryKey == after.RecoveryKey || before.URLSecret == after.URLSecret || before.BrokerIPCKey == after.BrokerIPCKey || before.DesktopIPCKey == after.DesktopIPCKey || before.DashboardKey == after.DashboardKey {
		t.Fatal("Rotate reused a credential")
	}
}

func TestConcurrentRotationsSerializeWithoutLosingGenerations(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir); err != nil {
		t.Fatal(err)
	}

	const rotations = 16
	results := make([]Values, rotations)
	errs := make([]error, rotations)
	var wait sync.WaitGroup
	for index := range rotations {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errs[index] = Rotate(dir)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Rotate %d: %v", index, err)
		}
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != rotations+1 {
		t.Fatalf("generation = %d, want %d", loaded.Generation, rotations+1)
	}
	matchingRecoveryKeys := 0
	for _, result := range results {
		if loaded.VerifyRecoveryKey(result.RecoveryKey) {
			matchingRecoveryKeys++
		}
	}
	if matchingRecoveryKeys != 1 {
		t.Fatalf("final store verified %d returned recovery keys, want 1", matchingRecoveryKeys)
	}
}
