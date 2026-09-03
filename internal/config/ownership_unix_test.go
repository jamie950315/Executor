//go:build darwin || linux

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestPrivilegedSaveAndMigrationPreserveConfigOwnerAndMode(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to reproduce a privileged config replacement")
	}
	uid, err := strconv.Atoi(os.Getenv("EXECUTOR_TEST_OWNER_UID"))
	if err != nil || uid == 0 {
		t.Skip("EXECUTOR_TEST_OWNER_UID must name a non-root test owner")
	}
	gid, err := strconv.Atoi(os.Getenv("EXECUTOR_TEST_OWNER_GID"))
	if err != nil {
		t.Fatalf("parse EXECUTOR_TEST_OWNER_GID: %v", err)
	}

	for _, migration := range []bool{false, true} {
		name := "save"
		if migration {
			name = "migration"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if migration {
				legacy := map[string]any{
					"version": 1, "state_dir": dir, "agent_address": "127.0.0.1:8787",
					"dashboard_address": "127.0.0.1:8788", "broker_endpoint": filepath.Join(dir, "broker.sock"),
					"desktop_endpoint": filepath.Join(dir, "desktop.sock"), "audit_retention_hours": 168,
				}
				encoded, err := json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, encoded, 0o640); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := Save(path, Default(dir)); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chown(path, uid, gid); err != nil {
				t.Fatal(err)
			}
			if migration {
				if _, err := Load(path); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Domain = "updated.example.test"
				if err := Save(path, cfg); err != nil {
					t.Fatal(err)
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("config has no Unix ownership metadata")
			}
			if int(stat.Uid) != uid || int(stat.Gid) != gid || info.Mode().Perm() != 0o600 {
				t.Fatalf("config owner/mode = %d:%d %o, want %d:%d 600", stat.Uid, stat.Gid, info.Mode().Perm(), uid, gid)
			}
		})
	}
}
