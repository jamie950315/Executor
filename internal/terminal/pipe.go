//go:build darwin || linux

package terminal

import (
	"io"
	"os"
	"os/exec"
	"sync/atomic"
)

type pipeProcessControl interface {
	Kill() error
	Signal(Signal) error
	Reap()
}

// The parent owns all three pipes. exec.Cmd.Wait never closes the read ends
// while capture goroutines are draining final stdout and stderr.
func startPipeProcess(spec SessionSpec) (terminalProcess, error) {
	cmd, err := newUnixCommand(spec)
	if err != nil {
		return nil, err
	}
	configurePipeCommand(cmd)
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, err
	}
	started := false
	defer func() {
		_ = inR.Close()
		_ = outW.Close()
		_ = errW.Close()
		if !started {
			_ = inW.Close()
			_ = outR.Close()
			_ = errR.Close()
		}
	}()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	control, err := attachPipeControl(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	started = true
	return &pipeProcess{cmd: cmd, stdin: inW, stdout: outR, stderr: errR, control: control}, nil
}

type pipeProcess struct {
	cmd                   *exec.Cmd
	stdin, stdout, stderr *os.File
	control               pipeProcessControl
	exited                atomic.Bool
}

func (p *pipeProcess) PID() int { return p.cmd.Process.Pid }
func (p *pipeProcess) Read(data []byte) (int, error) {
	n, err := p.stdout.Read(data)
	if err != nil {
		_ = p.stdout.Close()
	}
	return n, err
}
func (p *pipeProcess) StderrReader() io.Reader { return closingPipeReader{p.stderr} }

type closingPipeReader struct{ file *os.File }

func (r closingPipeReader) Read(data []byte) (int, error) {
	n, err := r.file.Read(data)
	if err != nil {
		_ = r.file.Close()
	}
	return n, err
}
func (p *pipeProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }
func (p *pipeProcess) CloseInput() error              { return p.stdin.Close() }
func (p *pipeProcess) Wait() error {
	err := p.cmd.Wait()
	p.exited.Store(true)
	p.control.Reap()
	return err
}
func (p *pipeProcess) Kill() error { return p.control.Kill() }
func (p *pipeProcess) Signal(sig Signal) error {
	if p.exited.Load() {
		return os.ErrProcessDone
	}
	return p.control.Signal(sig)
}
func (p *pipeProcess) Resize(int, int) error { return ErrResizeUnsupported }
