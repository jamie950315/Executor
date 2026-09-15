//go:build darwin || linux

package relay

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func lockReplayFile(file *os.File) (func(), error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
		}
		if (!errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN)) || time.Now().After(deadline) {
			return nil, ErrReplayStorage
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func syncReplayDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func privateReplayDirectory(info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }
