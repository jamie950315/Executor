package control

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
)

type commandSpec struct {
	name string
	args []string
}

type uidLookup func(string) (string, error)

type systemServiceManager struct {
	platform string
	env      func(string) string
	uid      uidLookup
	run      func(context.Context, commandSpec) error
}

func (m systemServiceManager) Stop(ctx context.Context, service Service) error {
	return m.execute(ctx, "stop", service)
}

func (m systemServiceManager) Start(ctx context.Context, service Service) error {
	return m.execute(ctx, "start", service)
}

func (m systemServiceManager) execute(ctx context.Context, action string, service Service) error {
	commands, err := serviceCommands(m.platform, action, service, m.env, m.uid)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err := m.run(ctx, command); err != nil {
			return err
		}
	}
	return nil
}

func runCommand(ctx context.Context, command commandSpec) error {
	return exec.CommandContext(ctx, command.name, command.args...).Run()
}

func serviceCommands(platform, action string, service Service, env func(string) string, lookup uidLookup) ([]commandSpec, error) {
	if action != "start" && action != "stop" {
		return nil, fmt.Errorf("unknown service action %q", action)
	}
	if env == nil {
		env = func(string) string { return "" }
	}
	switch platform {
	case "linux":
		return linuxCommands(action, service, env, lookup)
	case "windows":
		return windowsCommands(action, service)
	case "darwin":
		return darwinCommands(action, service, env, lookup)
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
}

func linuxCommands(action string, service Service, env func(string) string, lookup uidLookup) ([]commandSpec, error) {
	if service == Desktop {
		owner := env("EXECUTOR_DESKTOP_USER")
		if owner == "" {
			return nil, errors.New("EXECUTOR_DESKTOP_USER is required to manage the active-user desktop service")
		}
		uid, err := resolvedUID(env("EXECUTOR_DESKTOP_UID"), owner, lookup)
		if err != nil {
			return nil, fmt.Errorf("resolve desktop user UID: %w", err)
		}
		return []commandSpec{{name: "runuser", args: []string{"-u", owner, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + uid, "systemctl", "--user", action, "executor-desktop.service"}}}, nil
	}
	unit := map[Service]string{Agent: "executor-agent.service", Broker: "executor-broker.service", Cloudflared: "executor-cloudflared.service"}[service]
	if unit == "" {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	return []commandSpec{{name: "systemctl", args: []string{action, unit}}}, nil
}

func windowsCommands(action string, service Service) ([]commandSpec, error) {
	if service == Desktop {
		taskAction := map[string]string{"stop": "/End", "start": "/Run"}[action]
		return []commandSpec{{name: "schtasks.exe", args: []string{taskAction, "/TN", "ExecutorDesktop"}}}, nil
	}
	name := map[Service]string{Agent: "ExecutorAgent", Broker: "ExecutorBroker", Cloudflared: "ExecutorCloudflared"}[service]
	if name == "" {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	return []commandSpec{{name: "sc.exe", args: []string{action, name}}}, nil
}

func darwinCommands(action string, service Service, env func(string) string, lookup uidLookup) ([]commandSpec, error) {
	label := map[Service]string{Agent: "com.executor.agent", Broker: "com.executor.broker", Desktop: "com.executor.desktop", Cloudflared: "com.executor.cloudflared"}[service]
	if label == "" {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	domain := "system"
	plistDir := "/Library/LaunchDaemons"
	if service == Desktop {
		uid, err := resolvedUID(firstNonEmpty(env("EXECUTOR_GUI_UID"), env("SUDO_UID")), "", lookup)
		if err != nil {
			return nil, fmt.Errorf("resolve GUI owner UID: %w", err)
		}
		domain = "gui/" + uid
		plistDir = "/Library/LaunchAgents"
	}
	target := domain + "/" + label
	if action == "stop" {
		return []commandSpec{{name: "launchctl", args: []string{"bootout", target}}}, nil
	}
	plist := plistDir + "/" + label + ".plist"
	return []commandSpec{
		{name: "launchctl", args: []string{"bootstrap", domain, plist}},
		{name: "launchctl", args: []string{"enable", target}},
		{name: "launchctl", args: []string{"kickstart", "-k", target}},
	}, nil
}

func resolvedUID(configured, owner string, lookup uidLookup) (string, error) {
	uid := configured
	if uid == "" {
		if lookup == nil {
			return "", errors.New("no UID lookup is available")
		}
		var err error
		uid, err = lookup(owner)
		if err != nil {
			return "", err
		}
	}
	value, err := strconv.ParseUint(uid, 10, 32)
	if err != nil || value == 0 {
		return "", errors.New("UID must be a non-zero integer")
	}
	return uid, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
