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

	primaryGroup := os.Getegid()
	alternateGroup := -1
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group != primaryGroup {
			alternateGroup = group
			break
		}
	}
	if alternateGroup == -1 {
		t.Skip("current user has no alternate group for ownership preservation test")
	}

	path := filepath.Join(dir, secretsFile)
	if err := os.Chown(path, -1, alternateGroup); err != nil {
		t.Skipf("cannot assign an alternate owned group: %v", err)
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
}
