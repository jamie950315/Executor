//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"syscall"
)

func preserveFileOwnership(file *os.File, existing os.FileInfo) error {
	stat, ok := existing.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("read existing config ownership")
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}
