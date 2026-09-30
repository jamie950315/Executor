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
	return newUnixPipeControl(process.Pid)
}

type unixPipeControl struct {
	killTree     func() error
	rootReplaced func() (bool, error)
	killGroup    func() error
	pid          int
}

func (p unixPipeControl) Reap() {}
func (p unixPipeControl) Kill() error {
	if replaced, err := p.rootReplaced(); err != nil {
		return err
	} else if replaced {
		return os.ErrProcessDone
	}
	if err := p.killTree(); err != nil {
		return err
	}
	// The command can exit while background processes still own its pipes.
	// Its private process group survives that exit; descendant lookup does not.
	if replaced, err := p.rootReplaced(); err != nil {
		return err
	} else if replaced {
		return os.ErrProcessDone
	}
	if err := p.killGroup(); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
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
