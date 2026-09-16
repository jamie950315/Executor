package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/jamie950315/executor/internal/hub"
)

func runRelayDeviceCommand(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "relay-device" {
		return false, 0
	}
	if len(args) < 2 || args[1] != "init" {
		fmt.Fprintln(stderr, "Usage: executor relay-device init --state-dir <dedicated-directory> [--listen 127.0.0.1:29788]")
		return true, 2
	}
	flags := flag.NewFlagSet("relay-device init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	state := flags.String("state-dir", "", "dedicated relay-only device state")
	listen := flags.String("listen", "127.0.0.1:29788", "local rescue listener")
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return true, 2
	}
	result, err := hub.InitializeRelayDevice(*state, *listen)
	if result.RecoveryKey != "" {
		fmt.Fprintf(stdout, "Sensitive — save immediately. Recovery key (shown once): %s\n", result.RecoveryKey)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return true, 1
	}
	if result.Existing {
		fmt.Fprintln(stdout, "Existing relay-only state reused; credentials unchanged.")
	}
	fmt.Fprintf(stdout, "Relay-only device: %s\nConfiguration: %s\nNo public MCP Agent, Cloudflare tunnel, or OpenAI tunnel client was installed.\n", result.DeviceID, result.ConfigPath)
	return true, 0
}
