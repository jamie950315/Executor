//go:build linux

package terminal

import (
	"errors"
	"syscall"
	"testing"
)

func TestSignalLinuxProcessTreatsMissingProcessAsExited(t *testing.T) {
	err := signalLinuxProcess(linuxProcess{pid: -1, startTime: "missing"}, syscall.SIGKILL)
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("signal missing process error = %v, want ESRCH", err)
	}
}
