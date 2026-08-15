//go:build windows

package terminal

import (
	"io"
	"os"
	"os/exec"
	"syscall"
)

type ptyLauncher interface {
	Start(spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error)
	Kill(cmd *exec.Cmd) error
}

type windowsLauncher struct{}

func newPTYLauncher() ptyLauncher {
	return windowsLauncher{}
}

func (windowsLauncher) Start(spec SessionSpec) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	cmd := buildWindowsCommand(spec)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), flattenEnv(spec.Env)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}

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

func (windowsLauncher) Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/PID", itoa(cmd.Process.Pid), "/T", "/F").Run()
}

func buildWindowsCommand(spec SessionSpec) *exec.Cmd {
	if len(spec.Command) == 0 {
		return exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NoExit", "-Command", "-")
	}
	return exec.Command(spec.Command[0], spec.Command[1:]...)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	buf := [20]byte{}
	index := len(buf)
	for value > 0 {
		index--
		buf[index] = byte('0' + value%10)
		value /= 10
	}
	return sign + string(buf[index:])
}
