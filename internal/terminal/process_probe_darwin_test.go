//go:build darwin

package terminal

import (
	"errors"
	"syscall"
)

func processStopped(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
