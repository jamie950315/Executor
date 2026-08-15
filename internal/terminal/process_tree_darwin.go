//go:build darwin

package terminal

import (
	"errors"
	"syscall"
)

func killProcessTree(rootPID int) error {
	err := syscall.Kill(-rootPID, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
