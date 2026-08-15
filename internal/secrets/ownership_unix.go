//go:build darwin || linux

package secrets

import (
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwnership(file *os.File, existing os.FileInfo) error {
	uid, gid, err := fileOwnerIDs(existing)
	if err != nil {
		return err
	}
	return file.Chown(uid, gid)
}

func fileOwnerIDs(existing os.FileInfo) (int, int, error) {
	stat, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("read existing secret ownership")
	}
	return int(stat.Uid), int(stat.Gid), nil
}
