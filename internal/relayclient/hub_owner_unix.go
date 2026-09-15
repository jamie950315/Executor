//go:build darwin || linux

package relayclient

import (
	"os"
	"path/filepath"
	"syscall"
)

func preserveHubApprovalOwner(file *os.File, existing os.FileInfo) error {
	info, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrRequestUnauthorized
	}
	return file.Chown(int(info.Uid), int(info.Gid))
}

func finishHubApprovalRename(directory, name string) error {
	if err := os.Rename(filepath.Join(directory, name), filepath.Join(directory, "hub-delegations.json")); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
