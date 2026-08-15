//go:build darwin || linux

package secrets

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRotatePreservesExistingUnixOwner(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir); err != nil {
		t.Fatal(err)
	}

	targetUID := os.Geteuid()
	targetGID := os.Getegid()
	if os.Geteuid() == 0 {
		targetUID = 12345
		targetGID = 23456
	} else {
		groups, err := os.Getgroups()
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range groups {
			if group != targetGID {
				targetGID = group
				break
			}
		}
	}

	path := filepath.Join(dir, secretsFile)
	if err := os.Chown(path, targetUID, targetGID); err != nil {
		t.Fatalf("assign test owner: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeOwner := before.Sys().(*syscall.Stat_t)

	if _, err := Rotate(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterOwner := after.Sys().(*syscall.Stat_t)
	if beforeOwner.Uid != afterOwner.Uid || beforeOwner.Gid != afterOwner.Gid {
		t.Fatalf("secret owner changed from %d:%d to %d:%d", beforeOwner.Uid, beforeOwner.Gid, afterOwner.Uid, afterOwner.Gid)
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %o, want 600", after.Mode().Perm())
	}
}

type ownerOverrideInfo struct {
	os.FileInfo
	owner *syscall.Stat_t
}

func (info ownerOverrideInfo) Sys() any { return info.owner }

func TestFileOwnerIDsPreserveUIDAndGID(t *testing.T) {
	base, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, err := fileOwnerIDs(ownerOverrideInfo{
		FileInfo: base,
		owner:    &syscall.Stat_t{Uid: 12345, Gid: 23456},
	})
	if err != nil {
		t.Fatal(err)
	}
	if uid != 12345 || gid != 23456 {
		t.Fatalf("owner IDs = %d:%d, want 12345:23456", uid, gid)
	}
}

func TestRotateDoesNotFollowPredictableTempSymlink(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, secretsFile+".tmp")); err != nil {
		t.Fatal(err)
	}

	if _, err := Rotate(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "untouched\n" {
		t.Fatalf("predictable temp symlink target was modified: %q", data)
	}
}
