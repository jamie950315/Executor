package relay

import (
	"os"
	"runtime"
)

// WithHubStateLock serializes owner delegation changes across processes. The
// callback must not recursively acquire this lock. State must be owner-protected.
func WithHubStateLock(directory string, action func() error) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ErrReplayStorage
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return ErrReplayStorage
	}
	if info, err := root.Lstat(".hub-delegations.lock"); err == nil && !info.Mode().IsRegular() {
		return ErrReplayStorage
	} else if err != nil && !os.IsNotExist(err) {
		return ErrReplayStorage
	}
	file, err := openHubLockFile(root, ".hub-delegations.lock")
	if err != nil {
		return ErrReplayStorage
	}
	defer file.Close()
	release, err := lockReplayFile(file)
	if err != nil {
		return err
	}
	defer release()
	return action()
}
