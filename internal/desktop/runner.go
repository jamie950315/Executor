package desktop

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type defaultCommandRunner struct{}

func (defaultCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func commandKey(name string, args ...string) string {
	return name + "|" + strings.Join(args, "|")
}

func systemEnv() map[string]string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}
