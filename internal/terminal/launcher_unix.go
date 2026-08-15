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

type ptyLauncher interface {
	Start(spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error)
	Kill(cmd *exec.Cmd) error
	Signal(cmd *exec.Cmd, signal Signal) error
}

type scriptLauncher struct{}

func newPTYLauncher() ptyLauncher {
	return scriptLauncher{}
}

func (scriptLauncher) Start(spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	cmd := buildPTYCommand(spec.Command)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), flattenEnv(spec.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, stdin, stdout, nil
}

func (scriptLauncher) Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return nil
}

func (scriptLauncher) Signal(cmd *exec.Cmd, signal Signal) error {
	if cmd == nil || cmd.Process == nil {
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
	return syscall.Kill(-cmd.Process.Pid, sig)
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
