package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapLinuxInstallsAndRollsBackManagedUnits(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	tmpBundle := filepath.Join(tmp, "tmp-bundle")
	executorStub := filepath.Join(binDir, "executor")
	systemctlStub := filepath.Join(binDir, "systemctl")
	runuserStub := filepath.Join(binDir, "runuser")
	idStub := filepath.Join(binDir, "id")
	useraddStub := filepath.Join(binDir, "useradd")
	userdelStub := filepath.Join(binDir, "userdel")
	installStub := filepath.Join(binDir, "install")
	mktempStub := filepath.Join(binDir, "mktemp")

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf 'executor %s\\n' \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\"\n  printf '{\"version\":1,\"state_dir\":\"%s\",\"domain\":\"%s\",\"agent_address\":\"127.0.0.1:8787\",\"dashboard_address\":\"127.0.0.1:8788\",\"broker_endpoint\":\"/tmp/broker.sock\",\"desktop_endpoint\":\"/tmp/desktop.sock\",\"audit_retention_hours\":168}\\n' \"$EXECUTOR_STATE_DIR\" \"$EXECUTOR_DOMAIN\" > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  shift\n  while [[ $# -gt 0 ]]; do\n    case \"$1\" in\n      --output) output=\"$2\"; shift 2 ;;\n      *) shift ;;\n    esac\n  done\n  mkdir -p \"$output/systemd\" \"$output/systemd-user\"\n  printf '[Service]\\nUser=executor-agent\\nGroup=executor-agent\\nExecStart=/usr/local/bin/executor agent --config %s\\n' \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd/executor-agent.service\"\n  printf '[Service]\\nUser=root\\nGroup=root\\nExecStart=/usr/local/bin/executor broker --config %s\\n' \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd/executor-broker.service\"\n  printf '[Service]\\nExecStart=/usr/local/bin/cloudflared tunnel run --token-file %s\\n' \"$CLOUDFLARED_TOKEN_PATH\" > \"$output/systemd/cloudflared.service\"\n  printf '[Service]\\nExecStart=/usr/local/bin/executor desktop --config %s\\n' \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd-user/executor-desktop.service\"\n  exit 0\nfi\nexit 1\n")
	writeStub(t, systemctlStub, "#!/usr/bin/env bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, runuserStub, "#!/usr/bin/env bash\nprintf 'runuser %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"executor-agent\" ]]; then exit 1; fi\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, useraddStub, "#!/usr/bin/env bash\nprintf 'useradd %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, userdelStub, "#!/usr/bin/env bash\nprintf 'userdel %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, installStub, "#!/usr/bin/env bash\nexec /usr/bin/install \"$@\"\n")
	writeStub(t, mktempStub, "#!/usr/bin/env bash\nmkdir -p \""+tmpBundle+"\"\nprintf '%s\\n' \""+tmpBundle+"\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "install-root"),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_BIN="+executorStub,
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"CLOUDFLARED_TOKEN_PATH="+filepath.Join(tmp, "state", "cloudflared", "executor.token"),
		"SYSTEMCTL_BIN="+systemctlStub,
		"RUNUSER_BIN="+runuserStub,
		"ID_BIN="+idStub,
		"USERADD_BIN="+useraddStub,
		"USERDEL_BIN="+userdelStub,
		"INSTALL_BIN="+installStub,
		"MKTEMP_BIN="+mktempStub,
		"SUDO_USER=jamie",
	)

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "executor-agent.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "executor-broker.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "cloudflared.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd-user", "executor-desktop.service"))

	commandLog := readFile(t, logPath)
	for _, want := range []string{
		"executor setup --domain executor.example.com",
		"executor render-service-bundle",
		"useradd --system --user-group executor-agent",
		"systemctl daemon-reload",
		"systemctl enable executor-agent.service executor-broker.service cloudflared.service",
		"systemctl restart executor-agent.service executor-broker.service cloudflared.service",
		"runuser -u jamie -- env XDG_RUNTIME_DIR=/run/user/501",
		"systemctl --user enable executor-desktop.service",
		"systemctl --user restart executor-desktop.service",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("command log missing %q:\n%s", want, commandLog)
		}
	}
	if _, err := os.Stat(tmpBundle); !os.IsNotExist(err) {
		t.Fatalf("temporary bundle should be removed, err=%v", err)
	}

	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)
	commandLog = readFile(t, logPath)
	for _, want := range []string{
		"systemctl stop executor-agent.service executor-broker.service cloudflared.service",
		"systemctl disable executor-agent.service executor-broker.service cloudflared.service",
		"runuser -u jamie -- env XDG_RUNTIME_DIR=/run/user/501",
		"systemctl --user stop executor-desktop.service",
		"systemctl --user disable executor-desktop.service",
		"userdel executor-agent",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("rollback log missing %q:\n%s", want, commandLog)
		}
	}

	runScript(t, filepath.Join(root, "scripts", "uninstall.sh"), env)
	commandLog = readFile(t, logPath)
	if !strings.Contains(commandLog, "systemctl daemon-reload") {
		t.Fatalf("uninstall should reload systemd:\n%s", commandLog)
	}
}

func TestBootstrapMacOSLoadsLaunchdUnits(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	executorStub := filepath.Join(binDir, "executor")
	launchctlStub := filepath.Join(binDir, "launchctl")
	idStub := filepath.Join(binDir, "id")
	statStub := filepath.Join(binDir, "stat")
	dsclStub := filepath.Join(binDir, "dscl")
	sysadminctlStub := filepath.Join(binDir, "sysadminctl")
	installStub := filepath.Join(binDir, "install")

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf 'executor %s\\n' \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\"\n  printf '{\"version\":1}\\n' > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  shift\n  while [[ $# -gt 0 ]]; do\n    case \"$1\" in\n      --output) output=\"$2\"; shift 2 ;;\n      *) shift ;;\n    esac\n  done\n  mkdir -p \"$output/LaunchDaemons\" \"$output/LaunchAgents\"\n  printf '<plist><dict><key>Label</key><string>com.executor.agent</string></dict></plist>' > \"$output/LaunchDaemons/com.executor.agent.plist\"\n  printf '<plist><dict><key>Label</key><string>com.executor.broker</string></dict></plist>' > \"$output/LaunchDaemons/com.executor.broker.plist\"\n  printf '<plist><dict><key>Label</key><string>com.cloudflare.cloudflared</string></dict></plist>' > \"$output/LaunchDaemons/com.cloudflare.cloudflared.plist\"\n  printf '<plist><dict><key>Label</key><string>com.executor.desktop</string></dict></plist>' > \"$output/LaunchAgents/com.executor.desktop.plist\"\n  exit 0\nfi\nexit 1\n")
	writeStub(t, launchctlStub, "#!/usr/bin/env bash\nprintf 'launchctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, statStub, "#!/usr/bin/env bash\nprintf '777\\n'\n")
	writeStub(t, dsclStub, "#!/usr/bin/env bash\nexit 1\n")
	writeStub(t, sysadminctlStub, "#!/usr/bin/env bash\nprintf 'sysadminctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, installStub, "#!/usr/bin/env bash\nexec /usr/bin/install \"$@\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "Library"),
		"EXECUTOR_TARGET=macos",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_BIN="+executorStub,
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"LAUNCHCTL_BIN="+launchctlStub,
		"ID_BIN="+idStub,
		"STAT_BIN="+statStub,
		"DSCL_BIN="+dsclStub,
		"SYSADMINCTL_BIN="+sysadminctlStub,
		"INSTALL_BIN="+installStub,
		"SUDO_UID=501",
	)

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.agent.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.broker.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.cloudflare.cloudflared.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchAgents", "com.executor.desktop.plist"))

	commandLog := readFile(t, logPath)
	for _, want := range []string{
		"sysadminctl -addUser executor-agent",
		"launchctl bootstrap system",
		"launchctl enable system/com.executor.agent",
		"launchctl kickstart -k system/com.executor.agent",
		"launchctl bootstrap gui/501",
		"launchctl enable gui/501/com.executor.desktop",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("command log missing %q:\n%s", want, commandLog)
		}
	}
}

func TestRollbackPreservesExistingDedicatedIdentity(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	executorStub := filepath.Join(binDir, "executor")
	systemctlStub := filepath.Join(binDir, "systemctl")
	runuserStub := filepath.Join(binDir, "runuser")
	idStub := filepath.Join(binDir, "id")
	useraddStub := filepath.Join(binDir, "useradd")
	userdelStub := filepath.Join(binDir, "userdel")

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf 'executor %s\\n' \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\"; printf '{\"version\":1}\\n' > \"$EXECUTOR_CONFIG_PATH\"; exit 0; fi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then shift; while [[ $# -gt 0 ]]; do case \"$1\" in --output) output=\"$2\"; shift 2 ;; *) shift ;; esac; done; mkdir -p \"$output/systemd\" \"$output/systemd-user\"; printf 'x' > \"$output/systemd/executor-agent.service\"; printf 'x' > \"$output/systemd/executor-broker.service\"; printf 'x' > \"$output/systemd/cloudflared.service\"; printf 'x' > \"$output/systemd-user/executor-desktop.service\"; exit 0; fi\nexit 1\n")
	writeStub(t, systemctlStub, "#!/usr/bin/env bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, runuserStub, "#!/usr/bin/env bash\nprintf 'runuser %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"executor-agent\" ]]; then printf '900\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, useraddStub, "#!/usr/bin/env bash\nprintf 'useradd %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, userdelStub, "#!/usr/bin/env bash\nprintf 'userdel %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "install-root"),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_BIN="+executorStub,
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"SYSTEMCTL_BIN="+systemctlStub,
		"RUNUSER_BIN="+runuserStub,
		"ID_BIN="+idStub,
		"USERADD_BIN="+useraddStub,
		"USERDEL_BIN="+userdelStub,
		"SUDO_USER=jamie",
	)

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)
	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)

	commandLog := readFile(t, logPath)
	if strings.Contains(commandLog, "useradd --system --user-group executor-agent") {
		t.Fatalf("existing identity should not be re-created:\n%s", commandLog)
	}
	if strings.Contains(commandLog, "userdel executor-agent") {
		t.Fatalf("existing identity should not be deleted on rollback:\n%s", commandLog)
	}
}

func runScript(t *testing.T, script string, env []string) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Env = env
	cmd.Dir = filepath.Dir(filepath.Dir(script))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, string(output))
	}
}

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
