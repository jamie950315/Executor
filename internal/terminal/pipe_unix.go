//go:build darwin || linux

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configurePipeCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func attachPipeControl(process *os.Process) (pipeProcessControl, error) {
	return unixPipeControl{process.Pid}, nil
}

type unixPipeControl struct{ pid int }

func (p unixPipeControl) Reap()       {}
func (p unixPipeControl) Kill() error { return killProcessTree(p.pid) }
func (p unixPipeControl) Signal(sig Signal) error {
	switch sig {
	case SignalInterrupt:
		return syscall.Kill(-p.pid, syscall.SIGINT)
	case SignalTerminate:
		return syscall.Kill(-p.pid, syscall.SIGTERM)
	case SignalKill:
		return p.Kill()
	default:
		return errors.New("unsupported terminal signal")
	}
}
