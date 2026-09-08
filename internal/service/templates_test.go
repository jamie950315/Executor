package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestMacOSServicePathsRoundTripThroughXML(t *testing.T) {
	want := `/tmp/Executor & <test> "path"`
	cfg := InstallConfig{
		BinaryPath: want, DesktopBinaryPath: want, ConfigPath: want,
		LogPath: want, CloudflaredBinaryPath: want, CloudflaredLogPath: want, CloudflaredTokenPath: want,
	}
	for _, source := range targetTemplates(TargetMacOS) {
		t.Run(source, func(t *testing.T) {
			body, err := renderTemplate(source, cfg)
			if err != nil {
				t.Fatal(err)
			}
			var plist struct {
				Dict struct {
					Array struct {
						Strings []string `xml:"string"`
					} `xml:"array"`
				} `xml:"dict"`
			}
			if err := xml.Unmarshal([]byte(body), &plist); err != nil {
				t.Fatalf("invalid service XML: %v", err)
			}
			if len(plist.Dict.Array.Strings) == 0 || plist.Dict.Array.Strings[0] != want {
				t.Fatalf("service executable path did not round trip: %#v", plist.Dict.Array.Strings)
			}
		})
	}
}

func TestLinuxServicePathsAreLiteralArguments(t *testing.T) {
	cfg := InstallConfig{BinaryPath: `/opt/Executor test/bin%name`, ConfigPath: `/tmp/$config "quoted".json`, DataDir: `/tmp/Executor %data`}
	for _, target := range []Target{TargetLinux, TargetWSL} {
		for name, source := range targetTemplates(target) {
			if !strings.Contains(name, "executor-agent") {
				continue
			}
			body, err := renderTemplate(source, cfg)
			if err != nil {
				t.Fatal(err)
			}
			assertContainsAll(t, body,
				`ExecStart="/opt/Executor test/bin%%name" agent --config "/tmp/$$config \"quoted\".json"`,
				`WorkingDirectory=/tmp/Executor %%data`,
			)
		}
	}
}

func TestLinuxWorkingDirectoryRejectsUnrepresentableUnitValues(t *testing.T) {
	for _, directory := range []string{"/tmp/path\nInjected=value", "/tmp/path\r", "/tmp/path\x00", "/tmp/trailing ", "/tmp/trailing\\"} {
		if _, err := renderTemplate("templates/linux-agent.service.tmpl", InstallConfig{DataDir: directory}); err == nil {
			t.Errorf("working directory %q must fail before rendering a misleading unit", directory)
		}
	}
}

func TestRenderBundleEncodesPlatformServiceSemantics(t *testing.T) {
	t.Parallel()

	cfg := InstallConfig{
		BinaryPath:            "/usr/local/bin/executor",
		DesktopBinaryPath:     "/Library/Application Support/Executor/Executor Desktop.app/Contents/MacOS/executor-desktop",
		ConfigPath:            "/etc/executor/config.json",
		DataDir:               "/var/lib/executor",
		LogPath:               "/var/log/executor.log",
		CloudflaredBinaryPath: "/usr/local/bin/cloudflared",
		CloudflaredTokenPath:  "/etc/cloudflared/executor.token",
		CloudflaredLogPath:    "/var/log/cloudflared.log",
		AgentUser:             "jamie",
		AgentGroup:            "staff",
		BrokerUser:            "root",
		BrokerGroup:           "root",
		WindowsAgentService:   "ExecutorAgentSvc",
	}

	mac, err := RenderBundle(TargetMacOS, cfg)
	if err != nil {
		t.Fatalf("RenderBundle macOS: %v", err)
	}
	assertContainsAll(t, mac.Files["LaunchDaemons/com.executor.agent.plist"],
		"<string>com.executor.agent</string>",
		"<key>UserName</key>",
		"<string>jamie</string>",
		"<key>GroupName</key>",
		"<string>staff</string>",
	)
	assertContainsAll(t, mac.Files["LaunchDaemons/com.executor.broker.plist"],
		"<string>com.executor.broker</string>",
		"<key>UserName</key>",
		"<string>root</string>",
	)
	assertContainsAll(t, mac.Files["LaunchDaemons/com.executor.dashboard.plist"],
		"<string>com.executor.dashboard</string>",
		"<key>UserName</key>",
		"<string>root</string>",
		"<string>dashboard</string>",
		"<string>/etc/executor/config.json</string>",
	)
	assertContainsAll(t, mac.Files["LaunchDaemons/com.executor.cloudflared.plist"],
		"<string>com.executor.cloudflared</string>",
		"--token-file",
		"/etc/cloudflared/executor.token",
	)
	assertContainsAll(t, mac.Files["LaunchAgents/com.executor.desktop.plist"],
		"/Library/Application Support/Executor/Executor Desktop.app/Contents/MacOS/executor-desktop",
		"<string>desktop</string>",
	)
	if _, exists := mac.Files["LaunchDaemons/com.cloudflare.cloudflared.plist"]; exists {
		t.Fatal("macOS bundle would replace the host cloudflared LaunchDaemon")
	}

	linux, err := RenderBundle(TargetLinux, cfg)
	if err != nil {
		t.Fatalf("RenderBundle linux: %v", err)
	}
	assertContainsAll(t, linux.Files["systemd/executor-agent.service"],
		`ExecStart="/usr/local/bin/executor" agent --config "/etc/executor/config.json"`,
		"User=jamie",
		"Group=staff",
	)
	assertContainsAll(t, linux.Files["systemd/executor-broker.service"],
		`ExecStart="/usr/local/bin/executor" broker --config "/etc/executor/config.json"`,
		"User=root",
		"Group=root",
	)
	assertContainsAll(t, linux.Files["systemd/executor-dashboard.service"],
		`ExecStart="/usr/local/bin/executor" dashboard --config "/etc/executor/config.json"`,
		"User=root",
		"Group=root",
	)
	assertContainsAll(t, linux.Files["systemd/executor-cloudflared.service"],
		`run --token-file "/etc/cloudflared/executor.token"`,
	)
	if _, exists := linux.Files["systemd/cloudflared.service"]; exists {
		t.Fatal("Linux bundle would replace the host cloudflared service")
	}

	wsl, err := RenderBundle(TargetWSL, cfg)
	if err != nil {
		t.Fatalf("RenderBundle WSL: %v", err)
	}
	for _, name := range []string{
		"systemd/executor-agent.service",
		"systemd/executor-broker.service",
		"systemd/executor-dashboard.service",
		"systemd-user/executor-desktop.service",
		"systemd/executor-cloudflared.service",
		"wsl/README.txt",
	} {
		if wsl.Files[name] == "" {
			t.Fatalf("WSL bundle missing %q", name)
		}
	}

	windows, err := RenderBundle(TargetWindows, cfg)
	if err != nil {
		t.Fatalf("RenderBundle windows: %v", err)
	}
	assertContainsAll(t, windows.Files["windows/install-services.ps1"],
		"ExecutorAgentSvc",
		"New-Service -Name \"ExecutorAgent\"",
		"New-Service -Name \"ExecutorDashboard\"",
		"`\"$Binary`\" dashboard --config",
		"sc.exe config ExecutorDashboard obj= LocalSystem",
		"`\"$Binary`\" agent --config",
		"sc.exe config ExecutorAgent obj= $AgentIdentity",
	)
	if strings.Contains(windows.Files["windows/install-services.ps1"], "`\"$Binary`\" executor agent") {
		t.Fatalf("windows agent service still includes extra executor argv:\n%s", windows.Files["windows/install-services.ps1"])
	}
	if strings.Contains(windows.Files["windows/install-services.ps1"], "agent-password.txt") {
		t.Fatalf("windows agent service should not persist a service password:\n%s", windows.Files["windows/install-services.ps1"])
	}
	if strings.Contains(windows.Files["windows/install-services.ps1"], "AsPlainText") {
		t.Fatalf("windows agent service should not use plaintext password conversion:\n%s", windows.Files["windows/install-services.ps1"])
	}
	assertContainsAll(t, windows.Files["windows/configure-cloudflared.ps1"],
		"New-Service -Name \"ExecutorCloudflared\"",
		"--token-file",
	)
	if strings.Contains(windows.Files["windows/configure-cloudflared.ps1"], "Get-Service -Name \"cloudflared\"") {
		t.Fatal("Windows bundle would inspect or replace the host cloudflared service")
	}
	assertContainsAll(t, windows.Files["windows/register-desktop-startup.ps1"],
		"$TaskName = \"ExecutorDesktop\"",
		"$DesktopUser = \"jamie\"",
		"New-ScheduledTaskAction",
		"New-ScheduledTaskTrigger -AtLogOn -User $DesktopUser",
		"New-ScheduledTaskPrincipal -UserId $DesktopUser",
		"-LogonType Interactive",
		"-ExecutionTimeLimit ([TimeSpan]::Zero)",
		"Register-ScheduledTask -TaskName $TaskName",
		"Start-ScheduledTask -TaskName $TaskName",
	)
	if strings.Contains(windows.Files["windows/register-desktop-startup.ps1"], "executor-desktop.cmd") {
		t.Fatalf("windows desktop script should not use Startup cmd anymore:\n%s", windows.Files["windows/register-desktop-startup.ps1"])
	}
	if strings.Contains(windows.Files["windows/register-desktop-startup.ps1"], "InteractiveToken") {
		t.Fatalf("windows desktop script uses unsupported PowerShell 5.1 logon type:\n%s", windows.Files["windows/register-desktop-startup.ps1"])
	}
	if strings.Contains(windows.Files["windows/register-desktop-startup.ps1"], "WindowsIdentity") {
		t.Fatalf("windows desktop script should use the resolved desktop user from bootstrap instead of the current process identity:\n%s", windows.Files["windows/register-desktop-startup.ps1"])
	}
}

func assertContainsAll(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered body missing %q:\n%s", want, body)
		}
	}
}
