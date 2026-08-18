//go:build darwin || linux

package oauth

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestSaveStatePreservesExistingOwnerWhenRunAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to reproduce a privileged credential rotation")
	}
	uid, err := strconv.Atoi(os.Getenv("EXECUTOR_TEST_OWNER_UID"))
	if err != nil || uid == 0 {
		t.Skip("EXECUTOR_TEST_OWNER_UID must name a non-root test owner")
	}
	gid, err := strconv.Atoi(os.Getenv("EXECUTOR_TEST_OWNER_GID"))
	if err != nil {
		t.Fatalf("parse EXECUTOR_TEST_OWNER_GID: %v", err)
	}

	core := newTestCore(t)
	path := filepath.Join(t.TempDir(), "oauth-state.json")
	if err := core.SaveState(path); err != nil {
		t.Fatalf("initial SaveState: %v", err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		t.Fatalf("assign existing state owner: %v", err)
	}

	core.RevokeAll()
	if err := core.SaveState(path); err != nil {
		t.Fatalf("privileged SaveState: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("state file has no Unix ownership metadata")
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("state owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
	}
}
