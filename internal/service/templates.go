package service

import (
	"embed"
	"errors"
	"path"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates/*
var templateFS embed.FS

type Target string

const (
	TargetMacOS   Target = "macos"
	TargetLinux   Target = "linux"
	TargetWindows Target = "windows"
	TargetWSL     Target = "wsl"
)

type InstallConfig struct {
	BinaryPath            string
	DesktopBinaryPath     string
	ConfigPath            string
	DataDir               string
	LogPath               string
	CloudflaredBinaryPath string
	CloudflaredTokenPath  string
	CloudflaredLogPath    string
	DesktopUser           string
	AgentUser             string
	AgentGroup            string
	BrokerUser            string
	BrokerGroup           string
	WindowsAgentService   string
}

type Bundle struct {
	Files map[string]string
}

func RenderBundle(target Target, cfg InstallConfig) (Bundle, error) {
	if cfg.BinaryPath == "" || cfg.ConfigPath == "" {
		return Bundle{}, errors.New("binary path and config path are required")
	}
	if cfg.CloudflaredBinaryPath == "" || cfg.CloudflaredTokenPath == "" {
		return Bundle{}, errors.New("cloudflared binary path and token path are required")
	}
	if cfg.AgentUser == "" || cfg.AgentGroup == "" {
		return Bundle{}, errors.New("agent user and group are required")
	}
	if cfg.BrokerUser == "" || cfg.BrokerGroup == "" {
		return Bundle{}, errors.New("broker user and group are required")
	}
	if cfg.WindowsAgentService == "" {
		return Bundle{}, errors.New("windows agent service identity is required")
	}
	if cfg.DesktopBinaryPath == "" {
		cfg.DesktopBinaryPath = cfg.BinaryPath
	}
	if cfg.DesktopUser == "" {
		cfg.DesktopUser = cfg.AgentUser
	}
	files := map[string]string{}
	for output, source := range targetTemplates(target) {
		rendered, err := renderTemplate(source, cfg)
		if err != nil {
			return Bundle{}, err
		}
		files[output] = rendered
	}
	if len(files) == 0 {
		return Bundle{}, errors.New("unsupported target")
	}
	return Bundle{Files: files}, nil
}

func targetTemplates(target Target) map[string]string {
	switch target {
	case TargetMacOS:
		return map[string]string{
			"LaunchDaemons/com.executor.agent.plist":       "templates/macos-agent.plist.tmpl",
			"LaunchDaemons/com.executor.broker.plist":      "templates/macos-broker.plist.tmpl",
			"LaunchDaemons/com.executor.dashboard.plist":   "templates/macos-dashboard.plist.tmpl",
			"LaunchAgents/com.executor.desktop.plist":      "templates/macos-desktop.plist.tmpl",
			"LaunchDaemons/com.executor.cloudflared.plist": "templates/macos-cloudflared.plist.tmpl",
		}
	case TargetLinux:
		return map[string]string{
			"systemd/executor-agent.service":        "templates/linux-agent.service.tmpl",
			"systemd/executor-broker.service":       "templates/linux-broker.service.tmpl",
			"systemd/executor-dashboard.service":    "templates/linux-dashboard.service.tmpl",
			"systemd-user/executor-desktop.service": "templates/linux-desktop.service.tmpl",
			"systemd/executor-cloudflared.service":  "templates/linux-cloudflared.service.tmpl",
		}
	case TargetWindows:
		return map[string]string{
			"windows/install-services.ps1":         "templates/windows-install-services.ps1.tmpl",
			"windows/register-desktop-startup.ps1": "templates/windows-register-desktop.ps1.tmpl",
			"windows/configure-cloudflared.ps1":    "templates/windows-configure-cloudflared.ps1.tmpl",
		}
	case TargetWSL:
		return map[string]string{
			"systemd/executor-agent.service":        "templates/linux-agent.service.tmpl",
			"systemd/executor-broker.service":       "templates/linux-broker.service.tmpl",
			"systemd/executor-dashboard.service":    "templates/linux-dashboard.service.tmpl",
			"systemd-user/executor-desktop.service": "templates/linux-desktop.service.tmpl",
			"systemd/executor-cloudflared.service":  "templates/linux-cloudflared.service.tmpl",
			"wsl/README.txt":                        "templates/wsl-readme.txt.tmpl",
		}
	default:
		return nil
	}
}

func renderTemplate(name string, cfg InstallConfig) (string, error) {
	body, err := templateFS.ReadFile(name)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(path.Base(name)).Funcs(template.FuncMap{
		"systemdPath":             systemdPath,
		"systemdWorkingDirectory": systemdWorkingDirectory,
		"systemdArg": func(value string) string {
			return systemdPath(strings.ReplaceAll(value, "$", "$$"))
		},
	}).Parse(string(body))
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, cfg); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Unit-file paths expand percent specifiers even inside quotes. ExecStart
// arguments additionally expand dollars, which systemdArg escapes separately.
func systemdPath(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "%", "%%"))
}

// WorkingDirectory is a whole path, not a quoted ExecStart word. Unit-file
// parsing strips boundary whitespace and treats trailing backslashes as line
// continuations, so reject those names rather than silently changing them.
func systemdWorkingDirectory(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") || strings.TrimSpace(value) != value || strings.HasSuffix(value, "\\") {
		return "", errors.New("working directory cannot be represented literally in a systemd unit")
	}
	return strings.ReplaceAll(value, "%", "%%"), nil
}
