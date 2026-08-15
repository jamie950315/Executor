//go:build darwin || linux

package terminal

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

type scriptLauncher struct{}

func newPTYLauncher() ptyLauncher {
	return scriptLauncher{}
}

func (scriptLauncher) Start(spec SessionSpec) (terminalProcess, error) {
	cmd := buildPTYCommand(spec.Command)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), flattenEnv(spec.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execTerminalProcess{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

type execTerminalProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (p *execTerminalProcess) PID() int                       { return p.cmd.Process.Pid }
func (p *execTerminalProcess) Read(data []byte) (int, error)  { return p.stdout.Read(data) }
func (p *execTerminalProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }
func (p *execTerminalProcess) Wait() error                    { return p.cmd.Wait() }
func (p *execTerminalProcess) CloseInput() error              { return p.stdin.Close() }
func (p *execTerminalProcess) Resize(int, int) error          { return ErrResizeUnsupported }

func (p *execTerminalProcess) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	return nil
}

func (p *execTerminalProcess) Signal(signal Signal) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	var sig syscall.Signal
	switch signal {
	case SignalInterrupt:
		sig = syscall.SIGINT
	case SignalTerminate:
		sig = syscall.SIGTERM
	case SignalKill:
		sig = syscall.SIGKILL
	default:
		return nil
	}
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func buildPTYCommand(command []string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		args := append([]string{"-q", "/dev/null"}, command...)
		return exec.Command("script", args...)
	}
	return exec.Command("script", "-qfec", quoteCommand(command), "/dev/null")
}

func quoteCommand(command []string) string {
	parts := make([]string, 0, len(command))
	for _, arg := range command {
		if arg == "" {
			parts = append(parts, "''")
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(arg, "'", `'"'"'`)+"'")
	}
	return strings.Join(parts, " ")
}
