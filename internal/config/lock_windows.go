//go:build windows

package config

import (
	"errors"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func acquireConfigLock(path string) (func(), error) {
	lockPath, err := windows.UTF16PtrFromString(filepath.Join(filepath.Dir(path), ".config.lock"))
	if err != nil {
		return nil, errors.New("open config lock")
	}
	var handle windows.Handle
	for {
		handle, err = windows.CreateFile(
			lockPath,
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
			return nil, errors.New("open config lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("invalid config lock")
	}
	return func() { _ = windows.CloseHandle(handle) }, nil
}
