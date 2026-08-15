//go:build windows

package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func acquireStoreLock(dir string) (func(), error) {
	if err := ensureStoreDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".secrets.lock")
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var handle windows.Handle
	for {
		handle, err = windows.CreateFile(
			pathPtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_ALWAYS,
			windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err == nil {
			break
		}
		if err != windows.ERROR_SHARING_VIOLATION && err != windows.ERROR_LOCK_VIOLATION {
			return nil, fmt.Errorf("open secret store lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("stat secret store lock: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("secret store lock is not a regular file")
	}
	return func() {
		_ = windows.CloseHandle(handle)
	}, nil
}

func ensureStoreDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
