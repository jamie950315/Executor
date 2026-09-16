package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/jamie950315/executor/internal/daemon"
	"github.com/jamie950315/executor/internal/hub"
)

type hubAliases []string

func (a *hubAliases) String() string         { return "explicit OAuth resource aliases" }
func (a *hubAliases) Set(value string) error { *a = append(*a, value); return nil }

func runHubCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "hub" {
		return false, 0
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "Usage: executor hub init|registration|run --state-dir <dedicated-directory>")
		return true, 2
	}
	action := args[1]
	if action != "init" && action != "registration" && action != "run" {
		fmt.Fprintln(stderr, "Unknown Hub action")
		return true, 2
	}
	flags := flag.NewFlagSet("hub "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state-dir", "", "dedicated Hub state directory")
	var id, domain, dashboard, listen *string
	var aliases hubAliases
	if action == "init" {
		id = flags.String("hub-id", "pi5-hub", "public Hub identifier")
		domain = flags.String("domain", "", "canonical public OAuth/MCP hostname")
		dashboard = flags.String("dashboard-url", "", "authenticated Dashboard origin")
		listen = flags.String("listen", "127.0.0.1:28787", "loopback Hub listener")
		flags.Var(&aliases, "resource-alias", "explicit equivalent OAuth transport URL (repeatable)")
	}
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *state == "" || !filepath.IsAbs(*state) {
		return true, 2
	}
	if action == "init" {
		result, err := hub.Initialize(hub.InitOptions{StateDir: *state, HubID: *id, Domain: *domain, DashboardURL: *dashboard, ListenAddress: *listen, ResourceAliases: aliases})
		if result.RecoveryKey != "" {
			fmt.Fprintf(stdout, "Sensitive — save immediately. Recovery key (shown once): %s\n", result.RecoveryKey)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return true, 1
		}
		if result.Existing {
			fmt.Fprintln(stdout, "Existing Hub state reused; recovery key unchanged.")
		}
		fmt.Fprintf(stdout, "Hub configuration: %s\nPublic registration JSON:\n", result.ConfigPath)
		if json.NewEncoder(stdout).Encode(result.Registration) != nil {
			return true, 1
		}
		return true, 0
	}
	registration, err := hub.ReadRegistration(*state)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if action == "registration" {
		if json.NewEncoder(stdout).Encode(registration) != nil {
			return true, 1
		}
		return true, 0
	}
	if err := daemon.RunAgent(ctx, filepath.Join(*state, "config.json")); err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	return true, 0
}
