package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

func TestCreatePersistsP256RelayIdentityAndRotatePreservesIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	created, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	var private map[string]string
	if err := json.Unmarshal(created.RelayPrivateJWK, &private); err != nil {
		t.Fatalf("decode relay identity: %v", err)
	}
	if len(private) != 5 || private["kty"] != "EC" || private["crv"] != "P-256" || len(private["x"]) != 43 || len(private["y"]) != 43 || len(private["d"]) != 43 {
		t.Fatalf("relay private JWK = %#v", private)
	}
	rotated, err := Rotate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeRelayJWKForTest(t, rotated.RelayPrivateJWK), private) {
		t.Fatal("Rotate changed the relay device identity")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeRelayJWKForTest(t, loaded.RelayPrivateJWK), private) {
		t.Fatal("persisted relay identity changed after Rotate")
	}
}

func TestLoadBackfillsMissingRelayIdentityAndDurablySavesIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	created, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, secretsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "relay_private_jwk")
	data, err = json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := Load(dir)
	if err != nil {
		t.Fatalf("Load legacy secrets: %v", err)
	}
	firstIdentity := decodeRelayJWKForTest(t, first.RelayPrivateJWK)
	if len(first.RelayPrivateJWK) == 0 || reflect.DeepEqual(firstIdentity, decodeRelayJWKForTest(t, created.RelayPrivateJWK)) {
		t.Fatal("legacy secret store did not receive a fresh relay identity")
	}
	second, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeRelayJWKForTest(t, second.RelayPrivateJWK), firstIdentity) {
		t.Fatal("backfilled relay identity was not durably saved")
	}
}

func decodeRelayJWKForTest(t *testing.T, data json.RawMessage) map[string]string {
	t.Helper()
	var value map[string]string
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

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
