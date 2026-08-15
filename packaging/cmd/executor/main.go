package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jamie950315/executor/internal/cli"
	"github.com/jamie950315/executor/internal/service"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "render-service-bundle" {
		os.Exit(runRenderServiceBundle(os.Args[2:]))
	}
	if handled, code := runRuntimeCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, defaultRuntimeCommands()); handled {
		os.Exit(code)
	}
	os.Exit(cli.Run(ctx, os.Args[1:], newBackend(defaultStateDir()), os.Stdout, os.Stderr))
}

func runRenderServiceBundle(args []string) int {
	set := flag.NewFlagSet("render-service-bundle", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	target := set.String("target", "", "target platform: macos, linux, windows, wsl")
	outputDir := set.String("output", "", "directory to write rendered files")
	binaryPath := set.String("binary-path", "", "executor binary path")
	configPath := set.String("config-path", "", "executor config path")
	dataDir := set.String("data-dir", "", "executor state data directory")
	logPath := set.String("log-path", "", "executor log path")
	cloudflaredBinary := set.String("cloudflared-binary-path", "cloudflared", "cloudflared binary path")
	cloudflaredToken := set.String("cloudflared-token-path", "", "cloudflared token file path")
	cloudflaredLog := set.String("cloudflared-log-path", "", "cloudflared log path")
	agentUser := set.String("agent-user", "", "dedicated low-privilege agent user")
	agentGroup := set.String("agent-group", "", "dedicated low-privilege agent group")
	brokerUser := set.String("broker-user", "", "broker service user")
	brokerGroup := set.String("broker-group", "", "broker service group")
	windowsAgentService := set.String("windows-agent-service", "", "dedicated Windows agent service identity")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *target == "" || *outputDir == "" {
		fmt.Fprintln(os.Stderr, "render-service-bundle requires --target and --output")
		return 2
	}

	bundle, err := service.RenderBundle(service.Target(*target), service.InstallConfig{
		BinaryPath:            *binaryPath,
		ConfigPath:            *configPath,
		DataDir:               *dataDir,
		LogPath:               *logPath,
		CloudflaredBinaryPath: *cloudflaredBinary,
		CloudflaredTokenPath:  *cloudflaredToken,
		CloudflaredLogPath:    *cloudflaredLog,
		AgentUser:             *agentUser,
		AgentGroup:            *agentGroup,
		BrokerUser:            *brokerUser,
		BrokerGroup:           *brokerGroup,
		WindowsAgentService:   *windowsAgentService,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "render-service-bundle error: %v\n", err)
		return 1
	}
	for rel, body := range bundle.Files {
		path := filepath.Join(*outputDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "render-service-bundle mkdir error: %v\n", err)
			return 1
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "render-service-bundle write error: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(os.Stdout, "Rendered service bundle at %s\n", *outputDir)
	return 0
}
