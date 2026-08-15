//go:build darwin || linux

package secrets

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquireStoreLock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open secret store lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), dir)
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock secret store: %w", err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
