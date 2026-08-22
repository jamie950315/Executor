//go:build windows

package relayclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func acquireEnrollmentFileLock(ctx context.Context, configPath string) (*os.File, func(), bool, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, nil, false, errors.New("open dashboard enrollment lock")
	}
	lockPath, err := windows.UTF16PtrFromString(enrollmentLockPath(configPath))
	if err != nil {
		return nil, nil, false, errors.New("open dashboard enrollment lock")
	}
	waited := false
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, waited, err
		}
		handle, err := windows.CreateFile(
			lockPath,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_ALWAYS,
			windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = windows.CloseHandle(handle)
				return nil, nil, waited, err
			}
			var info windows.ByHandleFileInformation
			if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
				info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
				info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				_ = windows.CloseHandle(handle)
				return nil, nil, waited, errors.New("invalid dashboard enrollment lock")
			}
			file := os.NewFile(uintptr(handle), enrollmentLockPath(configPath))
			if file == nil {
				_ = windows.CloseHandle(handle)
				return nil, nil, waited, errors.New("open dashboard enrollment lock")
			}
			return file, func() { _ = file.Close() }, waited, nil
		}
		if err != windows.ERROR_SHARING_VIOLATION && err != windows.ERROR_LOCK_VIOLATION {
			return nil, nil, waited, errors.New("open dashboard enrollment lock")
		}
		waited = true
		select {
		case <-ctx.Done():
			return nil, nil, waited, ctx.Err()
		case <-retry.C:
		}
	}
}
