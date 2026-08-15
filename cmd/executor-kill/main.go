// executor-kill is an independent emergency Kill Switch for Executor.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jamie950315/executor/internal/control"
)

type killRunner interface {
	Kill(context.Context) (control.Result, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, func(configPath string) (killRunner, error) {
		return control.Load(configPath)
	}))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, load func(string) (killRunner, error)) int {
	flags := flag.NewFlagSet("executor-kill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "path to Executor configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Usage: executor-kill --config <path>")
		return 2
	}
	runner, err := load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "executor-kill: %v\n", err)
		return 1
	}
	result, err := runner.Kill(ctx)
	if result.RecoveryKey != "" && result.URLSecret != "" {
		fmt.Fprintf(stdout, "New recovery key (shown once): %s\nNew URL secret (shown once): %s\n", result.RecoveryKey, result.URLSecret)
	}
	if err != nil {
		fmt.Fprintf(stderr, "executor-kill: %v\n", err)
		return 1
	}
	return 0
}
