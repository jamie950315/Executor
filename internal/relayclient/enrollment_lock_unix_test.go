//go:build darwin || linux

package relayclient

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnrollmentLockIsPrivateAndPreservesConfigOwner(t *testing.T) {
	configPath, _, _ := relayFixture(t)
	tokenPath := filepath.Join(t.TempDir(), "enrollment.token")
	if err := os.WriteFile(tokenPath, []byte("test-only-private-lock-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if err := unix.Stat(configPath, &before); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: enrollmentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	if err := Enroll(context.Background(), EnrollOptions{
		ConfigPath: configPath, DashboardURL: "https://private-lock.example.test",
		TokenFile: tokenPath, HTTPClient: client, ExecutorVersion: "test-version",
	}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	lockInfo, err := os.Stat(enrollmentLockPath(configPath))
	if err != nil {
		t.Fatalf("stat enrollment lock: %v", err)
	}
	if !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("enrollment lock mode = %v, want regular 0600", lockInfo.Mode())
	}
	var after unix.Stat_t
	if err := unix.Stat(configPath, &after); err != nil {
		t.Fatal(err)
	}
	if after.Uid != before.Uid || after.Gid != before.Gid {
		t.Fatalf("config owner changed from %d:%d to %d:%d", before.Uid, before.Gid, after.Uid, after.Gid)
	}
}
