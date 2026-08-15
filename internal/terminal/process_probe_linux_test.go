//go:build linux

package terminal

import (
	"errors"
	"os"
	"syscall"
)

func processStopped(pid int) (bool, error) {
	process, err := readLinuxProcess(pid)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		probeErr := syscall.Kill(pid, 0)
		if errors.Is(probeErr, syscall.ESRCH) {
			return true, nil
		}
		if probeErr != nil {
			return false, probeErr
		}
		return false, err
	}
	return process.state == 'Z' || process.state == 'X', nil
}
