package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/jamie950315/executor/internal/daemon"
)

type runtimeCommands struct {
	agent     func(context.Context, string) error
	broker    func(context.Context, string) error
	desktop   func(context.Context, string) error
	dashboard func(context.Context, string) error
	stdio     func(context.Context, string, io.Reader, io.Writer) error
}

func defaultRuntimeCommands() runtimeCommands {
	return runtimeCommands{
		agent:     daemon.RunAgent,
		broker:    daemon.RunBroker,
		desktop:   daemon.RunDesktop,
		dashboard: daemon.RunDashboard,
		stdio:     daemon.RunStdio,
	}
}

func runRuntimeCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, commands runtimeCommands) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	name := args[0]
	if name != "agent" && name != "broker" && name != "desktop" && name != "dashboard" && name != "stdio" {
		return false, 0
	}
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	configPath := set.String("config", filepath.Join(defaultStateDir(), "config.json"), "Executor config path")
	if err := set.Parse(args[1:]); err != nil {
		return true, 2
	}
	if set.NArg() != 0 {
		fmt.Fprintf(stderr, "Executor %s: unexpected arguments: %v\n", name, set.Args())
		return true, 2
	}
	var err error
	switch name {
	case "agent":
		err = commands.agent(ctx, *configPath)
	case "broker":
		err = commands.broker(ctx, *configPath)
	case "desktop":
		err = commands.desktop(ctx, *configPath)
	case "dashboard":
		err = commands.dashboard(ctx, *configPath)
	case "stdio":
		err = commands.stdio(ctx, *configPath, stdin, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Executor %s error: %v\n", name, err)
		return true, 1
	}
	return true, 0
}
