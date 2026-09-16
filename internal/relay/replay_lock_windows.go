//go:build windows

package relay

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"time"
)

func lockReplayFile(file *os.File) (func(), error) {
	deadline := time.Now().Add(2 * time.Second)
	overlapped := new(windows.Overlapped)
	for {
		err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped) }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) || time.Now().After(deadline) {
			return nil, ErrReplayStorage
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Windows records are opened write-through and flushed individually. Directory
// fsync is not supported by os.File on Windows; native power-loss behavior is
// not asserted by the process-restart tests.
func syncReplayDirectory(_ *os.Root) error { return nil }

// Windows uses the ACL of the protected Executor state parent. POSIX mode
// bits do not represent that ACL; the caller must provision this directory
// under its existing protected state location.
func privateReplayDirectory(_ os.FileInfo) bool { return true }
