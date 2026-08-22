//go:build darwin || linux

package relayclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func acquireEnrollmentFileLock(ctx context.Context, configPath string) (*os.File, func(), bool, error) {
	return acquireEnrollmentFileLockWithOwnership(ctx, configPath, unix.Fchown)
}

func acquireEnrollmentFileLockWithOwnership(
	ctx context.Context,
	configPath string,
	setOwner func(fd, uid, gid int) error,
) (*os.File, func(), bool, error) {
	configFD, err := unix.Open(configPath, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, false, errors.New("invalid dashboard enrollment config")
	}
	var configStat unix.Stat_t
	configErr := unix.Fstat(configFD, &configStat)
	_ = unix.Close(configFD)
	if configErr != nil || configStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, nil, false, errors.New("invalid dashboard enrollment config")
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, nil, false, errors.New("open dashboard enrollment lock")
	}
	path := enrollmentLockPath(configPath)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, false, errors.New("open dashboard enrollment lock")
	}
	file := os.NewFile(uintptr(fd), path)
	closeFile := func() { _ = file.Close() }
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		closeFile()
		return nil, nil, false, errors.New("invalid dashboard enrollment lock")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		closeFile()
		return nil, nil, false, errors.New("secure dashboard enrollment lock")
	}
	if setOwner == nil || setOwner(fd, int(configStat.Uid), int(configStat.Gid)) != nil {
		closeFile()
		return nil, nil, false, errors.New("secure dashboard enrollment lock")
	}
	waited := false
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			closeFile()
			return nil, nil, waited, err
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = unix.Flock(fd, unix.LOCK_UN)
				closeFile()
				return nil, nil, waited, err
			}
			return file, func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				closeFile()
			}, waited, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeFile()
			return nil, nil, waited, errors.New("lock dashboard enrollment")
		}
		waited = true
		select {
		case <-ctx.Done():
			closeFile()
			return nil, nil, waited, ctx.Err()
		case <-retry.C:
		}
	}
}
