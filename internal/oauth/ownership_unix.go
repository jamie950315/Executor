//go:build darwin || linux

package oauth

import (
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwnership(file *os.File, existing os.FileInfo) error {
	stat, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read existing OAuth state ownership")
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}
