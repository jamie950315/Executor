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
	var lockStat unix.Stat_t
	if err := unix.Lstat(enrollmentLockPath(configPath), &lockStat); err != nil {
		t.Fatalf("stat enrollment lock: %v", err)
	}
	if lockStat.Mode&unix.S_IFMT != unix.S_IFREG || lockStat.Mode&0o777 != 0o600 {
		t.Fatalf("enrollment lock mode = %o, want regular 0600", lockStat.Mode)
	}
	if lockStat.Uid != before.Uid || lockStat.Gid != before.Gid {
		t.Fatalf("enrollment lock owner = %d:%d, want config owner %d:%d", lockStat.Uid, lockStat.Gid, before.Uid, before.Gid)
	}
	var after unix.Stat_t
	if err := unix.Stat(configPath, &after); err != nil {
		t.Fatal(err)
	}
	if after.Uid != before.Uid || after.Gid != before.Gid {
		t.Fatalf("config owner changed from %d:%d to %d:%d", before.Uid, before.Gid, after.Uid, after.Gid)
	}
}

func TestEnrollmentLockAppliesExistingConfigOwnerThroughOpenDescriptor(t *testing.T) {
	configPath, _, _ := relayFixture(t)
	lockPath := enrollmentLockPath(configPath)
	if err := os.WriteFile(lockPath, []byte("legacy lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var configStat unix.Stat_t
	if err := unix.Stat(configPath, &configStat); err != nil {
		t.Fatal(err)
	}

	var ownershipCalls, appliedUID, appliedGID int
	lock, release, _, err := acquireEnrollmentFileLockWithOwnership(
		context.Background(),
		configPath,
		func(fd, uid, gid int) error {
			ownershipCalls++
			var descriptorStat unix.Stat_t
			if err := unix.Fstat(fd, &descriptorStat); err != nil {
				t.Fatalf("stat enrollment lock descriptor: %v", err)
			}
			var pathStat unix.Stat_t
			if err := unix.Lstat(lockPath, &pathStat); err != nil {
				t.Fatalf("stat enrollment lock path: %v", err)
			}
			if descriptorStat.Mode&unix.S_IFMT != unix.S_IFREG || descriptorStat.Dev != pathStat.Dev || descriptorStat.Ino != pathStat.Ino {
				t.Fatal("ownership was not applied through the open regular lock descriptor")
			}
			appliedUID, appliedGID = uid, gid
			return nil
		},
	)
	if err != nil {
		t.Fatalf("acquire enrollment lock: %v", err)
	}
	defer release()
	if lock == nil {
		t.Fatal("acquire enrollment lock returned no file")
	}
	if ownershipCalls != 1 {
		t.Fatalf("enrollment lock ownership applications = %d, want 1", ownershipCalls)
	}
	if appliedUID != int(configStat.Uid) || appliedGID != int(configStat.Gid) {
		t.Fatalf("applied enrollment lock owner = %d:%d, want config owner %d:%d", appliedUID, appliedGID, configStat.Uid, configStat.Gid)
	}
}

func TestEnrollmentLockRejectsInvalidConfigFiles(t *testing.T) {
	configPath, _, _ := relayFixture(t)
	root := t.TempDir()
	missingPath := filepath.Join(root, "missing", "config.json")
	directoryPath := filepath.Join(root, "directory-config")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "symlink-config.json")
	if err := os.Symlink(configPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing":   missingPath,
		"directory": directoryPath,
		"symlink":   symlinkPath,
	} {
		t.Run(name, func(t *testing.T) {
			file, release, _, err := acquireEnrollmentFileLock(context.Background(), path)
			if err == nil {
				if release != nil {
					release()
				} else if file != nil {
					_ = file.Close()
				}
				t.Fatal("invalid config file acquired an enrollment lock")
			}
			if err.Error() != "invalid dashboard enrollment config" {
				t.Fatalf("invalid config error = %q, want fixed error", err)
			}
		})
	}
}

func TestEnrollmentLockRejectsSymlinkLock(t *testing.T) {
	configPath, _, _ := relayFixture(t)
	target := filepath.Join(t.TempDir(), "lock-target")
	if err := os.WriteFile(target, []byte("not a lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, enrollmentLockPath(configPath)); err != nil {
		t.Fatal(err)
	}
	file, release, _, err := acquireEnrollmentFileLock(context.Background(), configPath)
	if err == nil {
		if release != nil {
			release()
		} else if file != nil {
			_ = file.Close()
		}
		t.Fatal("symlink enrollment lock was accepted")
	}
	if err.Error() != "open dashboard enrollment lock" {
		t.Fatalf("symlink enrollment lock error = %q, want fixed error", err)
	}
}
