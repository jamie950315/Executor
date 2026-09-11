package terminal

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	DefaultOutputLimit = 64 << 10
	MaxOutputLimit     = 1 << 20
)

type SessionMode string

const (
	ModeInteractive SessionMode = "interactive"
	ModeCommand     SessionMode = "command"
)

// OutputLimit normalizes an omitted internal/RPC limit to the default page size.
// Public MCP arguments distinguish omission from zero and reject explicit zero.
func OutputLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultOutputLimit, nil
	}
	if limit < 1 || limit > MaxOutputLimit {
		return 0, fmt.Errorf("terminal output limit must be between 1 and %d bytes", MaxOutputLimit)
	}
	return limit, nil
}

func ValidateSessionSpec(spec SessionSpec) error {
	if !usesTTY(spec) && len(spec.Command) == 0 {
		return errors.New("tty=false requires an explicit argv command")
	}
	if !usesTTY(spec) && (spec.Columns != 0 || spec.Rows != 0) {
		return errors.New("terminal dimensions require tty=true")
	}
	if len(spec.Command) > 0 && spec.Command[0] == "" {
		return errors.New("terminal argv requires a non-empty executable")
	}
	for _, arg := range spec.Command {
		if strings.ContainsRune(arg, 0) {
			return errors.New("terminal argv contains a NUL byte")
		}
	}
	if strings.ContainsRune(spec.Dir, 0) {
		return errors.New("terminal cwd contains a NUL byte")
	}
	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("terminal environment requires non-empty names without '=' or NUL and values without NUL")
		}
	}
	if spec.Columns < 0 || spec.Rows < 0 || spec.Columns > MaxDimension || spec.Rows > MaxDimension {
		return fmt.Errorf("terminal dimensions must be between 1 and %d when provided", MaxDimension)
	}
	return nil
}

func usesTTY(spec SessionSpec) bool { return spec.TTY == nil || *spec.TTY }

// Run this in the chosen helper, whose OS identity determines path access.
// Keep structural validation in the dispatcher independent of its privileges.
func validateWorkingDirectory(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		code := "CWD_UNAVAILABLE"
		if errors.Is(err, os.ErrNotExist) {
			code = "CWD_NOT_FOUND"
		}
		if errors.Is(err, os.ErrPermission) {
			code = "CWD_ACCESS_DENIED"
		}
		return fmt.Errorf("%s: %q: %w", code, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("CWD_NOT_DIRECTORY: %q", path)
	}
	return nil
}

func BackendCapabilities() map[string]any {
	pipe := runtime.GOOS != "windows"
	return map[string]any{
		"platform": runtime.GOOS, "terminalResize": true, "nonInteractive": pipe,
		"separateStdoutStderr": pipe, "closeStdin": pipe, "ttyDefault": true,
		"gracefulTerminate": pipe, "pipeInterrupt": pipe,
	}
}

// lifecycle requires s.mu to be held. An interactive shell exposes its own
// lifetime; individual shell command execution and exit status remain unknown.
func (s *sessionState) lifecycle() (SessionMode, *bool, *int) {
	if s.meta.Mode != ModeCommand {
		return ModeInteractive, nil, nil
	}
	running := s.running
	var exitCode *int
	if !running {
		exitCode = confirmedExitCode(s.waitErr)
	}
	return ModeCommand, &running, exitCode
}

func confirmedExitCode(err error) *int {
	code := 0
	if err != nil {
		var exited interface{ ExitCode() int }
		if !errors.As(err, &exited) || exited.ExitCode() < 0 {
			return nil
		}
		code = exited.ExitCode()
	}
	return &code
}

// processExitError preserves an observed exit code on backends such as ConPTY
// whose wait operation reports the code separately from API failures.
type processExitError struct{ code int }

func (e processExitError) Error() string {
	return fmt.Sprintf("terminal process exited with code %d", e.code)
}
func (e processExitError) ExitCode() int { return e.code }
