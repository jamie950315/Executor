//go:build darwin || linux

package secrets

import (
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwnership(path string, existing os.FileInfo) error {
	stat, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read existing secret ownership")
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
