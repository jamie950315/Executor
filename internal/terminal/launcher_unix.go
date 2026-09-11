//go:build darwin || linux

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type nativeLauncher struct{}

func newPTYLauncher() ptyLauncher { return nativeLauncher{} }

func (nativeLauncher) Start(spec SessionSpec) (terminalProcess, error) {
	if !usesTTY(spec) {
		return startPipeProcess(spec)
	}
	cmd, err := newUnixCommand(spec)
	if err != nil {
		return nil, err
	}
	columns, rows := spec.Columns, spec.Rows
	if columns == 0 {
		columns = defaultTerminalColumns
	}
	if rows == 0 {
		rows = defaultTerminalRows
	}
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &nativePTYProcess{cmd: cmd, master: master}, nil
}

type nativePTYProcess struct {
	cmd     *exec.Cmd
	master  *os.File
	control sync.Mutex
	closed  bool
	exited  atomic.Bool
}

func (p *nativePTYProcess) PID() int { return p.cmd.Process.Pid }
func (p *nativePTYProcess) Read(data []byte) (int, error) {
	n, err := p.master.Read(data)
	if err != nil {
		p.control.Lock()
		if !p.closed {
			p.closed = true
			_ = p.master.Close()
		}
		p.control.Unlock()
	}
	return n, err
}
func (p *nativePTYProcess) Write(data []byte) (int, error) { return p.master.Write(data) }
func (p *nativePTYProcess) Wait() error {
	err := p.cmd.Wait()
	p.exited.Store(true)
	return err
}
func (p *nativePTYProcess) CloseInput() error {
	// Keep the master readable until the slave closes and final output drains.
	if p.exited.Load() {
		return nil
	}
	_, err := p.master.Write([]byte{4})
	return err
}
func (p *nativePTYProcess) Resize(columns, rows int) error {
	p.control.Lock()
	defer p.control.Unlock()
	if p.closed || p.exited.Load() {
		return os.ErrProcessDone
	}
	return pty.Setsize(p.master, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
}
func (p *nativePTYProcess) Kill() error { return killProcessTree(p.PID()) }
func (p *nativePTYProcess) Signal(signal Signal) error {
	if signal == SignalKill {
		return p.Kill()
	}
	var sig syscall.Signal
	switch signal {
	case SignalInterrupt:
		sig = syscall.SIGINT
	case SignalTerminate:
		sig = syscall.SIGTERM
	default:
		return errors.New("unsupported terminal signal")
	}
	p.control.Lock()
	defer p.control.Unlock()
	if p.closed || p.exited.Load() {
		return os.ErrProcessDone
	}
	// Address the foreground job on this PTY, preserving its shell and handler.
	group, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return err
	}
	if group <= 1 {
		return errors.New("foreground terminal process group is unavailable")
	}
	return syscall.Kill(-group, sig)
}
