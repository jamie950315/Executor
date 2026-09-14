//go:build darwin || linux

package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Honor the selected child environment when finding an executable. exec.Command
// otherwise resolves a bare name against the helper's PATH before cmd.Env is set.
func newUnixCommand(spec SessionSpec) (*exec.Cmd, error) {
	name := spec.Command[0]
	program := name
	if path, custom := spec.Env["PATH"]; custom && !strings.ContainsRune(name, '/') {
		cwd := spec.Dir
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return nil, err
			}
		}
		dirs := filepath.SplitList(path)
		if len(dirs) == 0 {
			dirs = []string{""}
		}
		program = ""
		for _, dir := range dirs {
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
			candidate, err := filepath.Abs(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && unix.Access(candidate, unix.X_OK) == nil {
				program = candidate
				break
			}
		}
		if program == "" {
			return nil, fmt.Errorf("EXECUTABLE_NOT_FOUND: %q in configured PATH: %w", name, exec.ErrNotFound)
		}
	}
	cmd := exec.Command(program, spec.Command[1:]...)
	cmd.Args[0] = name
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), flattenEnv(spec.Env)...)
	return cmd, nil
}
