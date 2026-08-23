//go:build !windows

package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardEnrollmentHelperUsesOnlyDesignatedTemporaryCopies(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tmp := t.TempDir()
	executorPath := filepath.Join(tmp, "executor")
	commandLog := filepath.Join(tmp, "commands.log")
	passedTokenPath := filepath.Join(tmp, "passed-token-path")
	writeStub(t, executorPath, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$COMMAND_LOG"
if [[ "$1" == "dashboard" && "$2" == "enroll" ]]; then
  if [[ "${FAIL_ENROLL:-}" == "1" ]]; then exit 9; fi
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "--token-file" ]]; then printf '%s\n' "$2" > "$PASSED_TOKEN_PATH"; rm -f -- "$2"; break; fi
    shift
  done
  printf 'enrolled\n'
  exit 0
fi
if [[ "$1" == "dashboard" && "$2" == "status" ]]; then printf '{"enrolled":true,"relay":"configured"}\n'; exit 0; fi
if [[ "$1" == "status" ]]; then printf '{"state":"armed","agent":"online"}\n'; exit 0; fi
exit 2
`)
	environment := append(os.Environ(), "COMMAND_LOG="+commandLog, "PASSED_TOKEN_PATH="+passedTokenPath)
	sourceToken := filepath.Join(tmp, "source-enrollment.token")
	const tokenValue = "test-only-enrollment-bearer"
	if err := os.WriteFile(sourceToken, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(root, "scripts", "enroll-dashboard.sh"),
		"--executor", executorPath,
		"--url", "https://dashboard.example.test",
		"--token-file", sourceToken,
	)
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("enroll with protected copy: %v\n%s", err, output)
	}
	if strings.Contains(string(output), tokenValue) {
		t.Fatalf("enrollment helper exposed bearer: %s", output)
	}
	assertFileExists(t, sourceToken)
	passed := strings.TrimSpace(readFile(t, passedTokenPath))
	if passed == sourceToken {
		t.Fatal("helper passed the persistent source token to the consuming enrollment command")
	}
	if _, err := os.Stat(passed); !os.IsNotExist(err) {
		t.Fatalf("helper-owned enrollment copy remains: %v", err)
	}
	log := readFile(t, commandLog)
	for _, want := range []string{"dashboard enroll --url https://dashboard.example.test", "dashboard status --json", "status --json"} {
		if !strings.Contains(log, want) {
			t.Fatalf("enrollment verification log missing %q:\n%s", want, log)
		}
	}

	temporaryToken := filepath.Join(tmp, "temporary-enrollment.token")
	if err := os.WriteFile(temporaryToken, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("bash", filepath.Join(root, "scripts", "enroll-dashboard.sh"),
		"--executor", executorPath,
		"--url", "https://dashboard.example.test",
		"--token-file", temporaryToken,
		"--temporary-token",
	)
	command.Env = environment
	if output, err = command.CombinedOutput(); err != nil {
		t.Fatalf("enroll designated temporary token: %v\n%s", err, output)
	}
	if _, err := os.Stat(temporaryToken); !os.IsNotExist(err) {
		t.Fatalf("designated temporary token remains after success: %v", err)
	}

	failedToken := filepath.Join(tmp, "failed-temporary-enrollment.token")
	if err := os.WriteFile(failedToken, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("bash", filepath.Join(root, "scripts", "enroll-dashboard.sh"),
		"--executor", executorPath,
		"--url", "https://dashboard.example.test",
		"--token-file", failedToken,
		"--temporary-token",
	)
	command.Env = append(environment, "FAIL_ENROLL=1")
	if output, err = command.CombinedOutput(); err == nil {
		t.Fatalf("failed enrollment unexpectedly succeeded: %s", output)
	}
	assertFileExists(t, failedToken)
	if strings.Contains(readFile(t, commandLog), "rollback") {
		t.Fatal("failed optional enrollment rolled back a healthy local install")
	}
}

func TestBootstrapLinuxInstallsAndRollsBackManagedUnits(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	tmpBundle := filepath.Join(tmp, "tmp-bundle")
	executorStub := filepath.Join(bundleRoot, "executor")
	executorKillStub := filepath.Join(bundleRoot, "executor-kill")
	stableExecutorPath := filepath.Join(tmp, "stable-bin", "executor")
	stableKillPath := filepath.Join(tmp, "stable-bin", "executor-kill")
	systemctlStub := filepath.Join(binDir, "systemctl")
	runuserStub := filepath.Join(binDir, "runuser")
	idStub := filepath.Join(binDir, "id")
	useraddStub := filepath.Join(binDir, "useradd")
	userdelStub := filepath.Join(binDir, "userdel")
	chownStub := filepath.Join(binDir, "chown")
	chmodStub := filepath.Join(binDir, "chmod")
	cpStub := filepath.Join(binDir, "cp")
	installStub := filepath.Join(binDir, "install")
	mktempStub := filepath.Join(binDir, "mktemp")

	oldExecutorBody := "#!/usr/bin/env bash\necho old-executor\n"
	if err := os.MkdirAll(filepath.Dir(stableExecutorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, stableExecutorPath, oldExecutorBody)

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\" \"$(dirname \"$CLOUDFLARED_TOKEN_PATH\")\"\n  printf '{\"bootstrap_secret\":\"secret\"}\\n' > \"${EXECUTOR_STATE_DIR}/secrets.json\"\n  printf 'cf-token\\n' > \"$CLOUDFLARED_TOKEN_PATH\"\n  if [[ \" $* \" == *\" --cloudflare-token-file \"* ]]; then\n    cloudflare=',\"cloudflare\":{\"account_id\":\"acct-1\",\"zone_id\":\"zone-1\",\"tunnel_id\":\"tunnel-1\",\"tunnel_name\":\"executor\",\"dns_record_id\":\"dns-1\",\"token_file_path\":\"'\"$CLOUDFLARED_TOKEN_PATH\"'\",\"hostname\":\"'\"$EXECUTOR_DOMAIN\"'\"}'\n  else\n    cloudflare=''\n  fi\n  printf '{\"version\":1,\"state_dir\":\"%s\",\"domain\":\"%s\",\"agent_address\":\"127.0.0.1:8787\",\"dashboard_address\":\"127.0.0.1:8788\",\"broker_endpoint\":\"/tmp/broker.sock\",\"desktop_endpoint\":\"/tmp/desktop.sock\",\"audit_retention_hours\":168%s}\\n' \"$EXECUTOR_STATE_DIR\" \"$EXECUTOR_DOMAIN\" \"$cloudflare\" > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  shift\n  output=''\n  binary=''\n  while [[ $# -gt 0 ]]; do\n    case \"$1\" in\n      --output) output=\"$2\"; shift 2 ;;\n      --binary-path) binary=\"$2\"; shift 2 ;;\n      *) shift ;;\n    esac\n  done\n  mkdir -p \"$output/systemd\" \"$output/systemd-user\"\n  printf '[Service]\\nExecStart=%s agent --config %s\\n' \"$binary\" \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd/executor-agent.service\"\n  printf '[Service]\\nUser=root\\nGroup=root\\nExecStart=%s broker --config %s\\n' \"$binary\" \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd/executor-broker.service\"\n  printf '[Service]\\nUser=root\\nGroup=root\\nExecStart=%s dashboard --config %s\\n' \"$binary\" \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd/executor-dashboard.service\"\n  printf '[Service]\\nExecStart=/usr/local/bin/cloudflared tunnel run --token-file %s\\n' \"$CLOUDFLARED_TOKEN_PATH\" > \"$output/systemd/cloudflared.service\"\n  printf '[Service]\\nExecStart=%s desktop --config %s\\n' \"$binary\" \"$EXECUTOR_CONFIG_PATH\" > \"$output/systemd-user/executor-desktop.service\"\n  exit 0\nfi\nexit 1\n")
	writeStub(t, executorKillStub, "#!/usr/bin/env bash\necho kill\n")
	replaceInFile(t, executorStub, "systemd/cloudflared.service", "systemd/executor-cloudflared.service")
	writeStub(t, systemctlStub, "#!/usr/bin/env bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, runuserStub, "#!/usr/bin/env bash\nprintf 'runuser %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-gn\" && \"$2\" == \"jamie\" ]]; then printf 'jamie\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, useraddStub, "#!/usr/bin/env bash\nprintf 'useradd %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, userdelStub, "#!/usr/bin/env bash\nprintf 'userdel %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, chownStub, "#!/usr/bin/env bash\nprintf 'chown %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, chmodStub, "#!/usr/bin/env bash\nprintf 'chmod %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, cpStub, "#!/usr/bin/env bash\nif [[ -e \"$2\" && \"$2\" == \"$EXECUTOR_INSTALL_BINARY_PATH\" ]]; then printf 'cp: Text file busy\\n' >&2; exit 26; fi\nexec /bin/cp \"$@\"\n")
	writeStub(t, installStub, "#!/usr/bin/env bash\nexec /usr/bin/install \"$@\"\n")
	writeStub(t, mktempStub, "#!/usr/bin/env bash\nmkdir -p \""+tmpBundle+"\"\nprintf '%s\\n' \""+tmpBundle+"\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "install-root"),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+stableKillPath,
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"CLOUDFLARED_TOKEN_PATH="+filepath.Join(tmp, "state", "cloudflared", "executor.token"),
		"SYSTEMCTL_BIN="+systemctlStub,
		"RUNUSER_BIN="+runuserStub,
		"ID_BIN="+idStub,
		"USERADD_BIN="+useraddStub,
		"USERDEL_BIN="+userdelStub,
		"CHOWN_BIN="+chownStub,
		"CHMOD_BIN="+chmodStub,
		"INSTALL_BIN="+installStub,
		"MKTEMP_BIN="+mktempStub,
		"EXECUTOR_PERMISSION_RETRY_ATTEMPTS=3",
		"EXECUTOR_PERMISSION_RETRY_DELAY=0",
		"SUDO_USER=jamie",
		"CLOUDFLARE_API_TOKEN_FILE="+filepath.Join(tmp, "api-token.txt"),
	)
	if err := os.WriteFile(filepath.Join(tmp, "api-token.txt"), []byte("api-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyService := filepath.Join(tmp, "install-root", "systemd", "system", "cloudflared.service")
	if err := os.MkdirAll(filepath.Dir(legacyService), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyService, []byte("legacy Executor tunnel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	originalHostService := "original host tunnel\n"
	legacyBackup := filepath.Join(stateDir, "service-backups") + legacyService
	if err := os.MkdirAll(filepath.Dir(legacyBackup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyBackup, []byte(originalHostService), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyManifest := "systemd:cloudflared.service|" + legacyService + "|restore\n"
	if err := os.WriteFile(filepath.Join(stateDir, "service-manifest.txt"), []byte(legacyManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)
	if got := readFile(t, legacyService); got != originalHostService {
		t.Fatalf("legacy host cloudflared service was not restored:\n%s", got)
	}
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "system", "executor-agent.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "system", "executor-broker.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "system", "executor-dashboard.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "system", "executor-cloudflared.service"))
	assertFileExists(t, filepath.Join(tmp, "install-root", "systemd", "user", "executor-desktop.service"))
	assertFileExists(t, stableExecutorPath)
	assertFileExists(t, stableKillPath)
	if got := readFile(t, filepath.Join(tmp, "install-root", "systemd", "system", "executor-agent.service")); !strings.Contains(got, stableExecutorPath) {
		t.Fatalf("executor-agent.service should point to stable binary %q:\n%s", stableExecutorPath, got)
	}
	if strings.Contains(readFile(t, logPath), bundleRoot) {
		t.Fatalf("bootstrap should not execute bundled binary path directly after install:\n%s", readFile(t, logPath))
	}

	commandLog := readFile(t, logPath)
	if got := strings.Count(commandLog, stableExecutorPath+" permissions request-all"); got != 3 {
		t.Fatalf("permission initialization attempts = %d, want 3:\n%s", got, commandLog)
	}
	for _, want := range []string{
		stableExecutorPath + " setup --domain executor.example.com --cloudflare-token-file " + filepath.Join(tmp, "api-token.txt"),
		stableExecutorPath + " render-service-bundle",
		stableExecutorPath + " permissions request-all",
		"--binary-path " + stableExecutorPath,
		"--agent-user jamie --agent-group jamie",
		"chown -R jamie:jamie " + filepath.Join(tmp, "state"),
		"chmod -R u+rwX,go-rwx " + filepath.Join(tmp, "state"),
		"systemctl daemon-reload",
		"systemctl stop cloudflared.service",
		"systemctl disable cloudflared.service",
		"systemctl enable cloudflared.service",
		"systemctl start cloudflared.service",
		"systemctl enable executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service",
		"systemctl restart executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service",
		"runuser -u jamie -- env XDG_RUNTIME_DIR=/run/user/501",
		"systemctl --user enable executor-desktop.service",
		"systemctl --user restart executor-desktop.service",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("command log missing %q:\n%s", want, commandLog)
		}
	}
	newTunnelStart := strings.Index(commandLog, "systemctl restart executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service")
	legacyTunnelStop := strings.Index(commandLog, "systemctl stop cloudflared.service")
	if newTunnelStart < 0 || legacyTunnelStop < 0 || newTunnelStart >= legacyTunnelStop {
		t.Fatal("legacy cloudflared migration must run after the isolated tunnel starts")
	}
	if strings.Contains(commandLog, "useradd --system --user-group executor-agent") {
		t.Fatalf("bootstrap should not create a dedicated executor-agent identity:\n%s", commandLog)
	}
	if _, err := os.Stat(tmpBundle); !os.IsNotExist(err) {
		t.Fatalf("temporary bundle should be removed, err=%v", err)
	}

	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)
	if got := readFile(t, stableExecutorPath); got != oldExecutorBody {
		t.Fatalf("rollback should restore previous executor binary:\n%s", got)
	}
	if _, err := os.Stat(stableKillPath); !os.IsNotExist(err) {
		t.Fatalf("rollback should remove newly-installed executor-kill, err=%v", err)
	}
	commandLog = readFile(t, logPath)
	for _, want := range []string{
		"systemctl stop executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service",
		"systemctl disable executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service",
		"runuser -u jamie -- env XDG_RUNTIME_DIR=/run/user/501",
		"systemctl --user stop executor-desktop.service",
		"systemctl --user disable executor-desktop.service",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("rollback log missing %q:\n%s", want, commandLog)
		}
	}
	if strings.Contains(commandLog, "userdel executor-agent") {
		t.Fatalf("rollback should not delete a dedicated executor-agent identity:\n%s", commandLog)
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
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	executorStub := filepath.Join(bundleRoot, "executor")
	executorKillStub := filepath.Join(bundleRoot, "executor-kill")
	bundledDesktopBinary := filepath.Join(bundleRoot, "Executor Desktop.app", "Contents", "MacOS", "executor-desktop")
	bundledDesktopInfo := filepath.Join(bundleRoot, "Executor Desktop.app", "Contents", "Info.plist")
	stableExecutorPath := filepath.Join(tmp, "stable-bin", "executor")
	stableKillPath := filepath.Join(tmp, "stable-bin", "executor-kill")
	stableDesktopAppPath := filepath.Join(tmp, "Library", "Application Support", "Executor", "Executor Desktop.app")
	launchctlStub := filepath.Join(binDir, "launchctl")
	idStub := filepath.Join(binDir, "id")
	statStub := filepath.Join(binDir, "stat")
	dsclStub := filepath.Join(binDir, "dscl")
	sysadminctlStub := filepath.Join(binDir, "sysadminctl")
	chownStub := filepath.Join(binDir, "chown")
	chmodStub := filepath.Join(binDir, "chmod")
	installStub := filepath.Join(binDir, "install")

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\" \"$(dirname \"$CLOUDFLARED_TOKEN_PATH\")\"\n  printf '{\"bootstrap_secret\":\"secret\"}\\n' > \"${EXECUTOR_STATE_DIR}/secrets.json\"\n  printf 'cf-token\\n' > \"$CLOUDFLARED_TOKEN_PATH\"\n  if [[ \" $* \" == *\" --cloudflare-token-file \"* ]]; then\n    cloudflare=',\"cloudflare\":{\"account_id\":\"acct-1\",\"zone_id\":\"zone-1\",\"tunnel_id\":\"tunnel-1\",\"tunnel_name\":\"executor\",\"dns_record_id\":\"dns-1\",\"token_file_path\":\"'\"$CLOUDFLARED_TOKEN_PATH\"'\",\"hostname\":\"'\"$EXECUTOR_DOMAIN\"'\"}'\n  else\n    cloudflare=''\n  fi\n  printf '{\"version\":1,\"state_dir\":\"%s\",\"domain\":\"%s\",\"agent_address\":\"127.0.0.1:8787\",\"dashboard_address\":\"127.0.0.1:8788\",\"broker_endpoint\":\"/tmp/broker.sock\",\"desktop_endpoint\":\"/tmp/desktop.sock\",\"audit_retention_hours\":168%s}\\n' \"$EXECUTOR_STATE_DIR\" \"$EXECUTOR_DOMAIN\" \"$cloudflare\" > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  shift\n  output=''\n  binary=''\n  while [[ $# -gt 0 ]]; do\n    case \"$1\" in\n      --output) output=\"$2\"; shift 2 ;;\n      --binary-path) binary=\"$2\"; shift 2 ;;\n      *) shift ;;\n    esac\n  done\n  mkdir -p \"$output/LaunchDaemons\" \"$output/LaunchAgents\"\n  printf '<plist><dict><key>Label</key><string>com.executor.agent</string><key>ProgramArguments</key><array><string>%s</string></array></dict></plist>' \"$binary\" > \"$output/LaunchDaemons/com.executor.agent.plist\"\n  printf '<plist><dict><key>Label</key><string>com.executor.broker</string><key>ProgramArguments</key><array><string>%s</string></array></dict></plist>' \"$binary\" > \"$output/LaunchDaemons/com.executor.broker.plist\"\n  printf '<plist><dict><key>Label</key><string>com.executor.dashboard</string><key>ProgramArguments</key><array><string>%s</string></array></dict></plist>' \"$binary\" > \"$output/LaunchDaemons/com.executor.dashboard.plist\"\n  printf '<plist><dict><key>Label</key><string>com.cloudflare.cloudflared</string></dict></plist>' > \"$output/LaunchDaemons/com.cloudflare.cloudflared.plist\"\n  printf '<plist><dict><key>Label</key><string>com.executor.desktop</string><key>ProgramArguments</key><array><string>%s</string></array></dict></plist>' \"$binary\" > \"$output/LaunchAgents/com.executor.desktop.plist\"\n  exit 0\nfi\nexit 1\n")
	writeStub(t, executorKillStub, "#!/usr/bin/env bash\necho kill\n")
	if err := os.MkdirAll(filepath.Dir(bundledDesktopBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, bundledDesktopBinary, "#!/usr/bin/env bash\necho desktop\n")
	if err := os.WriteFile(bundledDesktopInfo, []byte("<plist><dict/></plist>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaceInFile(t, executorStub, "com.cloudflare.cloudflared", "com.executor.cloudflared")
	writeStub(t, launchctlStub, "#!/usr/bin/env bash\nprintf 'launchctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"print\" ]]; then marker=\"${COMMAND_LOG}.print.${2//\\//_}\"; if [[ ! -e \"$marker\" ]]; then : > \"$marker\"; exit 0; fi; exit 1; fi\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-gn\" && \"$2\" == \"jamie\" ]]; then printf 'staff\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, statStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-c\" ]]; then exit 1; fi\nif [[ \"$1\" == \"-f\" && \"$2\" == \"%u\" ]]; then printf '777\\n'; exit 0; fi\nif [[ \"$1\" == \"-f\" && \"$2\" == \"%Su\" ]]; then printf 'console-user\\n'; exit 0; fi\nif [[ \"$1\" == \"-f\" && \"$2\" == \"%Lp\" ]]; then printf '600\\n'; exit 0; fi\nprintf '777\\n'\n")
	writeStub(t, dsclStub, "#!/usr/bin/env bash\nexit 1\n")
	writeStub(t, sysadminctlStub, "#!/usr/bin/env bash\nprintf 'sysadminctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, chownStub, "#!/usr/bin/env bash\nprintf 'chown %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, chmodStub, "#!/usr/bin/env bash\nprintf 'chmod %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, installStub, "#!/usr/bin/env bash\nexec /usr/bin/install \"$@\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "Library"),
		"EXECUTOR_TARGET=macos",
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+stableKillPath,
		"EXECUTOR_MACOS_DESKTOP_APP_PATH="+stableDesktopAppPath,
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"CLOUDFLARED_TOKEN_PATH="+filepath.Join(tmp, "state", "cloudflared", "executor.token"),
		"LAUNCHCTL_BIN="+launchctlStub,
		"ID_BIN="+idStub,
		"STAT_BIN="+statStub,
		"DSCL_BIN="+dsclStub,
		"SYSADMINCTL_BIN="+sysadminctlStub,
		"CHOWN_BIN="+chownStub,
		"CHMOD_BIN="+chmodStub,
		"INSTALL_BIN="+installStub,
		"EXECUTOR_PERMISSION_RETRY_ATTEMPTS=3",
		"EXECUTOR_PERMISSION_RETRY_DELAY=0",
		"SUDO_USER=jamie",
		"SUDO_UID=501",
		"CLOUDFLARE_API_TOKEN_FILE="+filepath.Join(tmp, "api-token.txt"),
	)
	if err := os.WriteFile(filepath.Join(tmp, "api-token.txt"), []byte("api-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalDesktopInfo := "<plist><dict><key>OldInstall</key><true/></dict></plist>\n"
	if err := os.MkdirAll(filepath.Join(stableDesktopAppPath, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stableDesktopAppPath, "Contents", "Info.plist"), []byte(originalDesktopInfo), 0o644); err != nil {
		t.Fatal(err)
	}

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.agent.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.broker.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.dashboard.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.cloudflared.plist"))
	assertFileExists(t, filepath.Join(tmp, "Library", "LaunchAgents", "com.executor.desktop.plist"))
	assertFileExists(t, stableExecutorPath)
	assertFileExists(t, stableKillPath)
	assertFileExists(t, filepath.Join(stableDesktopAppPath, "Contents", "Info.plist"))
	assertFileExists(t, filepath.Join(stableDesktopAppPath, "Contents", "MacOS", "executor-desktop"))
	if got := readFile(t, filepath.Join(tmp, "Library", "LaunchDaemons", "com.executor.agent.plist")); !strings.Contains(got, stableExecutorPath) {
		t.Fatalf("com.executor.agent.plist should point to stable binary %q:\n%s", stableExecutorPath, got)
	}

	commandLog := readFile(t, logPath)
	if got := strings.Count(commandLog, stableExecutorPath+" permissions request-all"); got != 3 {
		t.Fatalf("permission initialization attempts = %d, want 3:\n%s", got, commandLog)
	}
	for _, want := range []string{
		stableExecutorPath + " setup --domain executor.example.com --cloudflare-token-file " + filepath.Join(tmp, "api-token.txt"),
		stableExecutorPath + " render-service-bundle",
		stableExecutorPath + " permissions request-all",
		"--binary-path " + stableExecutorPath,
		"--desktop-binary-path " + filepath.Join(stableDesktopAppPath, "Contents", "MacOS", "executor-desktop"),
		"--agent-user jamie --agent-group staff",
		"--broker-user root --broker-group wheel",
		"chown -R jamie:staff " + filepath.Join(tmp, "state"),
		"chmod -R u+rwX,go-rwx " + filepath.Join(tmp, "state"),
		"launchctl bootstrap system",
		"launchctl bootout system/com.executor.agent",
		"launchctl print system/com.executor.agent",
		"launchctl enable system/com.executor.dashboard",
		"launchctl kickstart -k system/com.executor.dashboard",
		"launchctl enable system/com.executor.agent",
		"launchctl kickstart -k system/com.executor.agent",
		"launchctl bootstrap gui/501",
		"launchctl bootout gui/501/com.executor.desktop",
		"launchctl enable gui/501/com.executor.desktop",
	} {
		if !strings.Contains(commandLog, want) {
			t.Fatalf("command log missing %q:\n%s", want, commandLog)
		}
	}
	if strings.Contains(commandLog, bundleRoot) {
		t.Fatalf("bootstrap should not execute bundled binary path directly after install:\n%s", commandLog)
	}
	if strings.Contains(commandLog, "sysadminctl -addUser executor-agent") {
		t.Fatalf("bootstrap should not create a dedicated executor-agent identity:\n%s", commandLog)
	}
	if got := readFile(t, filepath.Join(stableDesktopAppPath, "Contents", "Info.plist")); got == originalDesktopInfo {
		t.Fatal("bootstrap did not replace the previous desktop app")
	}

	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)
	if got := readFile(t, filepath.Join(stableDesktopAppPath, "Contents", "Info.plist")); got != originalDesktopInfo {
		t.Fatalf("rollback did not restore the previous desktop app:\n%s", got)
	}
}

func TestRollbackIgnoresLegacyDedicatedIdentityManifestEntries(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	systemctlStub := filepath.Join(binDir, "systemctl")
	runuserStub := filepath.Join(binDir, "runuser")
	idStub := filepath.Join(binDir, "id")
	userdelStub := filepath.Join(binDir, "userdel")

	writeStub(t, systemctlStub, "#!/usr/bin/env bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, runuserStub, "#!/usr/bin/env bash\nprintf 'runuser %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, userdelStub, "#!/usr/bin/env bash\nprintf 'userdel %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")

	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(stateDir, "service-manifest.txt")
	if err := os.WriteFile(manifestPath, []byte("identity:user|executor-agent|delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+stateDir,
		"EXECUTOR_INSTALL_ROOT="+filepath.Join(tmp, "install-root"),
		"EXECUTOR_TARGET=linux",
		"SYSTEMCTL_BIN="+systemctlStub,
		"RUNUSER_BIN="+runuserStub,
		"ID_BIN="+idStub,
		"USERDEL_BIN="+userdelStub,
		"SUDO_USER=jamie",
	)
	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)

	commandLog := readFile(t, logPath)
	if strings.Contains(commandLog, "userdel executor-agent") {
		t.Fatalf("legacy identity manifest entries should be ignored on rollback:\n%s", commandLog)
	}
}

func TestBootstrapRequiresCloudflareTokenFileOrCompletedMetadata(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(tmp, "commands.log")
	executorStub := filepath.Join(bundleRoot, "executor")
	executorKillStub := filepath.Join(bundleRoot, "executor-kill")
	stableExecutorPath := filepath.Join(tmp, "stable-bin", "executor")
	stableKillPath := filepath.Join(tmp, "stable-bin", "executor-kill")
	idStub := filepath.Join(binDir, "id")
	useraddStub := filepath.Join(binDir, "useradd")
	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\"\n  printf '{\"version\":1,\"state_dir\":\"%s\",\"domain\":\"%s\",\"agent_address\":\"127.0.0.1:8787\",\"dashboard_address\":\"127.0.0.1:8788\",\"broker_endpoint\":\"/tmp/broker.sock\",\"desktop_endpoint\":\"/tmp/desktop.sock\",\"audit_retention_hours\":168}\\n' \"$EXECUTOR_STATE_DIR\" \"$EXECUTOR_DOMAIN\" > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  printf 'render should not run\\n' >&2\n  exit 99\nfi\nexit 1\n")
	writeStub(t, executorKillStub, "#!/usr/bin/env bash\necho kill\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-gn\" && \"$2\" == \"jamie\" ]]; then printf 'jamie\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, useraddStub, "#!/usr/bin/env bash\nexit 0\n")
	stateDir := filepath.Join(tmp, "state")
	installRoot := filepath.Join(tmp, "install-root")
	legacyService := filepath.Join(installRoot, "systemd", "system", "cloudflared.service")
	if err := os.MkdirAll(filepath.Dir(legacyService), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyBody := "working legacy Executor tunnel\n"
	if err := os.WriteFile(legacyService, []byte(legacyBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyManifest := "systemd:cloudflared.service|" + legacyService + "|remove\n"
	if err := os.WriteFile(filepath.Join(stateDir, "service-manifest.txt"), []byte(legacyManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+logPath,
		"EXECUTOR_STATE_DIR="+stateDir,
		"EXECUTOR_INSTALL_ROOT="+installRoot,
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+stableKillPath,
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_CONFIG_PATH="+filepath.Join(tmp, "state", "config.json"),
		"ID_BIN="+idStub,
		"USERADD_BIN="+useraddStub,
		"SUDO_USER=jamie",
	)

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "bootstrap.sh"))
	cmd.Env = append(env, "CLOUDFLARED_BIN=/usr/bin/true")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bootstrap unexpectedly succeeded:\n%s", string(output))
	}
	if !strings.Contains(string(output), "Cloudflare") {
		t.Fatalf("bootstrap error should mention Cloudflare readiness:\n%s", string(output))
	}
	if got := readFile(t, legacyService); got != legacyBody {
		t.Fatalf("failed bootstrap changed working legacy tunnel:\n%s", got)
	}
	if got := readFile(t, filepath.Join(stateDir, "service-manifest.txt")); got != legacyManifest {
		t.Fatalf("Cloudflare preflight failure changed existing manifest:\n%s", got)
	}
	if _, err := os.Stat(stableExecutorPath); !os.IsNotExist(err) {
		t.Fatalf("Cloudflare preflight failure installed executor before validation, err=%v", err)
	}
	if _, err := os.Stat(stableKillPath); !os.IsNotExist(err) {
		t.Fatalf("Cloudflare preflight failure installed executor-kill before validation, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("Cloudflare preflight failure consumed the one-time recovery key, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("Cloudflare preflight failure created config before validation, err=%v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), " setup ") {
		t.Fatalf("Cloudflare preflight failure ran setup before validation:\n%s", data)
	}
}

func TestBootstrapRejectsInsecureCloudflareTokenBeforeInstalling(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tmp := t.TempDir()
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	executorStub := filepath.Join(bundleRoot, "executor")
	executorKillStub := filepath.Join(bundleRoot, "executor-kill")
	writeStub(t, executorStub, "#!/usr/bin/env bash\nprintf 'setup unexpectedly ran\\n' >&2\nexit 99\n")
	writeStub(t, executorKillStub, "#!/usr/bin/env bash\nexit 0\n")
	tokenPath := filepath.Join(tmp, "cloudflare.token")
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stableExecutorPath := filepath.Join(tmp, "installed", "executor")
	stableKillPath := filepath.Join(tmp, "installed", "executor-kill")
	env := append(os.Environ(),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+stableKillPath,
		"CLOUDFLARE_API_TOKEN_FILE="+tokenPath,
		"CLOUDFLARED_BIN=/usr/bin/true",
	)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "bootstrap.sh"))
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bootstrap accepted insecure Cloudflare token permissions:\n%s", output)
	}
	if !strings.Contains(string(output), "0600") {
		t.Fatalf("bootstrap did not explain required token permissions:\n%s", output)
	}
	for _, path := range []string{stableExecutorPath, stableKillPath, filepath.Join(tmp, "state", "secrets.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("insecure token preflight changed %s, err=%v", path, err)
		}
	}
}

func TestBootstrapRejectsBlankCloudflareTokenBeforeInstalling(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tmp := t.TempDir()
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(bundleRoot, "executor"), "#!/usr/bin/env bash\nprintf 'setup unexpectedly ran\\n' >&2\nexit 99\n")
	writeStub(t, filepath.Join(bundleRoot, "executor-kill"), "#!/usr/bin/env bash\nexit 0\n")
	tokenPath := filepath.Join(tmp, "cloudflare.token")
	if err := os.WriteFile(tokenPath, []byte(" \t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableExecutorPath := filepath.Join(tmp, "installed", "executor")
	env := append(os.Environ(),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+filepath.Join(tmp, "installed", "executor-kill"),
		"CLOUDFLARE_API_TOKEN_FILE="+tokenPath,
		"CLOUDFLARED_BIN=/usr/bin/true",
	)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "bootstrap.sh"))
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bootstrap accepted a blank Cloudflare token:\n%s", output)
	}
	if !strings.Contains(string(output), "empty") {
		t.Fatalf("bootstrap did not explain blank token rejection:\n%s", output)
	}
	if _, err := os.Stat(stableExecutorPath); !os.IsNotExist(err) {
		t.Fatalf("blank token preflight installed executor, err=%v", err)
	}
}

func TestBootstrapValidatesCompleteBundleBeforeInstalling(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tmp := t.TempDir()
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(bundleRoot, "executor"), "#!/usr/bin/env bash\nexit 99\n")
	tokenPath := filepath.Join(tmp, "cloudflare.token")
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableExecutorPath := filepath.Join(tmp, "installed", "executor")
	env := append(os.Environ(),
		"EXECUTOR_TARGET=linux",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+filepath.Join(tmp, "installed", "executor-kill"),
		"CLOUDFLARE_API_TOKEN_FILE="+tokenPath,
		"CLOUDFLARED_BIN=/usr/bin/true",
	)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "bootstrap.sh"))
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bootstrap accepted an incomplete bundle:\n%s", output)
	}
	if !strings.Contains(string(output), "missing bundled binary") {
		t.Fatalf("bootstrap did not identify the incomplete bundle:\n%s", output)
	}
	if _, err := os.Stat(stableExecutorPath); !os.IsNotExist(err) {
		t.Fatalf("incomplete bundle installed executor before validation, err=%v", err)
	}
}

func TestBootstrapValidatesMacOSDesktopAppBeforeInstalling(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tmp := t.TempDir()
	bundleRoot := filepath.Join(tmp, "bundle")
	if err := os.MkdirAll(filepath.Join(bundleRoot, "Executor Desktop.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(bundleRoot, "executor"), "#!/usr/bin/env bash\nprintf 'setup unexpectedly ran\\n' >&2\nexit 99\n")
	writeStub(t, filepath.Join(bundleRoot, "executor-kill"), "#!/usr/bin/env bash\nexit 0\n")
	tokenPath := filepath.Join(tmp, "cloudflare.token")
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableExecutorPath := filepath.Join(tmp, "installed", "executor")
	env := append(os.Environ(),
		"EXECUTOR_TARGET=macos",
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_STATE_DIR="+filepath.Join(tmp, "state"),
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+filepath.Join(tmp, "installed", "executor-kill"),
		"EXECUTOR_MACOS_DESKTOP_APP_PATH="+filepath.Join(tmp, "installed", "Executor Desktop.app"),
		"CLOUDFLARE_API_TOKEN_FILE="+tokenPath,
		"CLOUDFLARED_BIN=/usr/bin/true",
	)
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "bootstrap.sh"))
	cmd.Dir = root
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bootstrap accepted an incomplete macOS Desktop app:\n%s", output)
	}
	if !strings.Contains(string(output), "missing bundled macOS Desktop app file") {
		t.Fatalf("bootstrap did not identify incomplete macOS app contents:\n%s", output)
	}
	if _, err := os.Stat(stableExecutorPath); !os.IsNotExist(err) {
		t.Fatalf("incomplete macOS app installed executor before validation, err=%v", err)
	}
}

func TestWindowsDesktopTaskScriptsTrackScheduledTaskLifecycle(t *testing.T) {
	t.Parallel()

	bootstrap := readFile(t, filepath.Join(repoRoot(t), "scripts", "bootstrap.ps1"))
	rollback := readFile(t, filepath.Join(repoRoot(t), "scripts", "rollback.ps1"))
	uninstall := readFile(t, filepath.Join(repoRoot(t), "scripts", "uninstall.ps1"))

	for _, want := range []string{
		"Join-Path $env:ProgramData \"Executor\"",
		"$DesktopTaskName = \"ExecutorDesktop\"",
		"$DesktopUser = if ($env:EXECUTOR_DESKTOP_USER)",
		"function Resolve-ActiveConsoleUser",
		"function Resolve-DesktopStartupPath",
		"Get-CimInstance Win32_ComputerSystem",
		"ProfileList",
		"$DesktopStartup = Resolve-DesktopStartupPath -UserName $DesktopUser",
		"throw \"Unable to determine the active desktop user.",
		"$BundleRoot = if ($env:EXECUTOR_BUNDLE_ROOT)",
		"$ExecutorInstallPath = if ($env:EXECUTOR_INSTALL_BINARY_PATH)",
		"$ExecutorKillInstallPath = if ($env:EXECUTOR_KILL_INSTALL_BINARY_PATH)",
		"function Assert-CloudflarePreflight",
		"Assert-CloudflarePreflight",
		"Install-ManagedFile -Source $BundledExecutorPath -Destination $ExecutorInstallPath",
		"Install-ManagedFile -Source $BundledExecutorKillPath -Destination $ExecutorKillInstallPath",
		"$PendingReplacementCleanup = @()",
		"$Previous = $Destination + \".executor-old.\" + $PID",
		"Move-Item -Path $Destination -Destination $Previous",
		"Move-Item -Path $Previous -Destination $Destination",
		"$script:PendingReplacementCleanup += $Previous",
		"& $ExecutorInstallPath @SetupArgs",
		"& $ExecutorInstallPath render-service-bundle",
		"& $ExecutorInstallPath permissions request-all",
		"--binary-path $ExecutorInstallPath",
		"--desktop-user $DesktopUser",
		"Export-ScheduledTask -TaskName $DesktopTaskName",
		"Add-ManifestRecord -Kind \"scheduled-task\" -PathValue $DesktopTaskName",
		"Record-ScheduledTaskState -DesktopTaskName $DesktopTaskName",
		"Record-ServiceState -Name \"ExecutorDashboard\"",
		"Record-ServiceState -Name \"ExecutorCloudflared\"",
		"$LegacyOwnedCloudflared = $OwnedServices -contains \"cloudflared\"",
		"$LegacyCloudflaredBackupPath",
		"Set-ItemProperty -Path \"HKLM:\\SYSTEM\\CurrentControlSet\\Services\\cloudflared\"",
		"Start-Service -Name \"cloudflared\"",
		"sc.exe delete cloudflared",
		"$LASTEXITCODE",
		"Legacy cloudflared service deletion did not complete",
		"service-imagepath",
		"$OwnedServicesPath = Join-Path $StateDir \"owned-services.txt\"",
		"Add-Content -Path $OwnedServicesPath -Value $Name",
		"if ($OwnedServices -contains $Name)",
		"keep-running",
		"Refusing to replace unmanaged Windows service",
		"function Invoke-PowerShellScript",
		"throw \"PowerShell script failed with exit code $LASTEXITCODE",
		"Invoke-PowerShellScript -Path (Join-Path $InstallRoot 'windows\\install-services.ps1')",
		"ExecutorDashboard",
		"icacls $StateDir /grant:r",
		"${WindowsAgentService}:(OI)(CI)(M)",
		"${DesktopUser}:(OI)(CI)(RX)",
		"icacls $ConfigPath /inheritance:r /grant:r",
		"icacls $SecretsPath /inheritance:r /grant:r",
		"icacls $CloudflaredTokenPath /inheritance:r /grant:r",
		"SYSTEM:(OI)(CI)(F)",
		"Invoke-PowerShellScript -Path (Join-Path $InstallRoot 'windows\\register-desktop-startup.ps1')",
		"Invoke-PowerShellScript -Path (Join-Path $InstallRoot 'windows\\configure-cloudflared.ps1')",
		"$PermissionRetryAttempts = if ($env:EXECUTOR_PERMISSION_RETRY_ATTEMPTS)",
		"for ($PermissionAttempt = 1; $PermissionAttempt -le $PermissionRetryAttempts; $PermissionAttempt++)",
	} {
		if !strings.Contains(bootstrap, want) {
			t.Fatalf("bootstrap.ps1 missing %q:\n%s", want, bootstrap)
		}
	}
	serviceInstall := strings.Index(bootstrap, "Invoke-PowerShellScript -Path (Join-Path $InstallRoot 'windows\\install-services.ps1')")
	stateACL := strings.Index(bootstrap, "icacls $StateDir /grant:r")
	serviceStart := strings.Index(bootstrap, "Start-OrRestartService -Name \"ExecutorAgent\"")
	if serviceInstall < 0 || stateACL < 0 || serviceStart < 0 || !(serviceInstall < stateACL && stateACL < serviceStart) {
		t.Fatalf("Windows bootstrap must create service identity before ACLs and start only afterward")
	}
	preflight := strings.Index(bootstrap, "\nAssert-CloudflarePreflight\n")
	firstStateWrite := strings.Index(bootstrap, "New-Item -ItemType Directory -Path $StateDir")
	firstBinaryInstall := strings.Index(bootstrap, "Install-ManagedFile -Source $BundledExecutorPath")
	if preflight < 0 || firstStateWrite < 0 || firstBinaryInstall < 0 || !(preflight < firstStateWrite && preflight < firstBinaryInstall) {
		t.Fatal("Windows Cloudflare preflight must finish before state or installed binaries change")
	}
	newTunnelStart := strings.LastIndex(bootstrap, "windows\\configure-cloudflared.ps1")
	legacyMigration := strings.LastIndex(bootstrap, "if ((Test-Path $LegacyCloudflaredBackupPath)")
	if newTunnelStart < 0 || legacyMigration < 0 || newTunnelStart >= legacyMigration {
		t.Fatal("Windows legacy cloudflared migration must run after the isolated tunnel starts")
	}

	for _, want := range []string{
		"$OwnedServicesPath = Join-Path $StateDir \"owned-services.txt\"",
		"$ManagedServices = @()",
		"$OwnedServices -contains $Parts[1]",
		"Select-Object -Unique",
		"$IsUninstall = $env:EXECUTOR_UNINSTALL -eq \"1\"",
		"$DeleteService = $Owned -and ($Mode -eq \"delete\" -or $IsUninstall)",
		"$ServicesToRestart += $PathValue",
		"Stop-ScheduledTask -TaskName $PathValue",
		"Unregister-ScheduledTask -TaskName $PathValue -Confirm:$false",
		"Register-ScheduledTask -TaskName $PathValue -Xml",
		"service-imagepath",
		"Set-ItemProperty -Path (\"HKLM:\\SYSTEM\\CurrentControlSet\\Services\\\" + $PathValue)",
		"service deletion did not complete",
	} {
		if !strings.Contains(rollback, want) {
			t.Fatalf("rollback.ps1 missing %q:\n%s", want, rollback)
		}
	}
	if strings.Contains(rollback, "foreach ($service in @(\"ExecutorAgent\"") {
		t.Fatal("rollback.ps1 must not stop services before proving ownership")
	}

	if !strings.Contains(uninstall, "rollback.ps1") || !strings.Contains(uninstall, "$env:EXECUTOR_UNINSTALL = \"1\"") {
		t.Fatalf("uninstall.ps1 should invoke rollback first:\n%s", uninstall)
	}
}

func TestWindowsBootstrapResolvesCloudflaredExecutablePathForService(t *testing.T) {
	t.Parallel()

	bootstrap := readFile(t, filepath.Join(repoRoot(t), "scripts", "bootstrap.ps1"))
	for _, want := range []string{
		"Get-Command $CloudflaredBinInput -ErrorAction Stop",
		"$CloudflaredBin = $CloudflaredCommand.Source",
	} {
		if !strings.Contains(bootstrap, want) {
			t.Fatalf("Windows bootstrap does not resolve cloudflared to a service-safe absolute path; missing %q", want)
		}
	}
}

func TestUnixBootstrapResolvesCloudflaredExecutablePathForService(t *testing.T) {
	t.Parallel()

	bootstrap := readFile(t, filepath.Join(repoRoot(t), "scripts", "bootstrap.sh"))
	for _, want := range []string{
		`CLOUDFLARED_BIN_RESOLVED="$(command -v "${CLOUDFLARED_BIN}" 2>/dev/null)"`,
		`/opt/homebrew/bin/cloudflared`,
		`CLOUDFLARED_BIN="${CLOUDFLARED_BIN_RESOLVED}"`,
	} {
		if !strings.Contains(bootstrap, want) {
			t.Fatalf("Unix bootstrap does not resolve cloudflared to a service-safe absolute path; missing %q", want)
		}
	}
}

func TestUnixScriptsAutoDetectWSLFromEnvironmentAndProcVersion(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	bundleRoot := filepath.Join(tmp, "bundle")
	stateDir := filepath.Join(tmp, "state")
	installRoot := filepath.Join(tmp, "install-root")
	commandLog := filepath.Join(tmp, "commands.log")
	procVersionPath := filepath.Join(tmp, "proc-version")
	tmpBundle := filepath.Join(tmp, "tmp-bundle")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(procVersionPath, []byte("Linux version 5.15.167.4-microsoft-standard-WSL2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apiTokenPath := filepath.Join(tmp, "api-token.txt")
	if err := os.WriteFile(apiTokenPath, []byte("api-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executorStub := filepath.Join(bundleRoot, "executor")
	executorKillStub := filepath.Join(bundleRoot, "executor-kill")
	stableExecutorPath := filepath.Join(tmp, "stable-bin", "executor")
	stableKillPath := filepath.Join(tmp, "stable-bin", "executor-kill")
	systemctlStub := filepath.Join(binDir, "systemctl")
	runuserStub := filepath.Join(binDir, "runuser")
	idStub := filepath.Join(binDir, "id")
	chownStub := filepath.Join(binDir, "chown")
	chmodStub := filepath.Join(binDir, "chmod")
	mktempStub := filepath.Join(binDir, "mktemp")

	writeStub(t, executorStub, "#!/usr/bin/env bash\nset -euo pipefail\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$COMMAND_LOG\"\nif [[ \"$1\" == \"setup\" ]]; then\n  mkdir -p \"$(dirname \"$EXECUTOR_CONFIG_PATH\")\" \"$(dirname \"$CLOUDFLARED_TOKEN_PATH\")\"\n  printf '{\"bootstrap_secret\":\"secret\"}\\n' > \"${EXECUTOR_STATE_DIR}/secrets.json\"\n  printf 'cf-token\\n' > \"$CLOUDFLARED_TOKEN_PATH\"\n  printf '{\"version\":1,\"state_dir\":\"%s\",\"domain\":\"%s\",\"agent_address\":\"127.0.0.1:8787\",\"dashboard_address\":\"127.0.0.1:8788\",\"broker_endpoint\":\"/tmp/broker.sock\",\"desktop_endpoint\":\"/tmp/desktop.sock\",\"audit_retention_hours\":168,\"cloudflare\":{\"account_id\":\"acct-1\",\"zone_id\":\"zone-1\",\"tunnel_id\":\"tunnel-1\",\"tunnel_name\":\"executor\",\"dns_record_id\":\"dns-1\",\"token_file_path\":\"'\"$CLOUDFLARED_TOKEN_PATH\"'\",\"hostname\":\"'\"$EXECUTOR_DOMAIN\"'\"}}\\n' \"$EXECUTOR_STATE_DIR\" \"$EXECUTOR_DOMAIN\" > \"$EXECUTOR_CONFIG_PATH\"\n  exit 0\nfi\nif [[ \"$1\" == \"render-service-bundle\" ]]; then\n  shift\n  output=''\n  target=''\n  while [[ $# -gt 0 ]]; do\n    case \"$1\" in\n      --output) output=\"$2\"; shift 2 ;;\n      --target) target=\"$2\"; shift 2 ;;\n      *) shift ;;\n    esac\n  done\n  printf 'render-target=%s\\n' \"$target\" >> \"$COMMAND_LOG\"\n  mkdir -p \"$output/systemd\" \"$output/systemd-user\"\n  printf '[Service]\\n' > \"$output/systemd/executor-agent.service\"\n  printf '[Service]\\n' > \"$output/systemd/executor-broker.service\"\n  printf '[Service]\\n' > \"$output/systemd/executor-dashboard.service\"\n  printf '[Service]\\n' > \"$output/systemd/executor-cloudflared.service\"\n  printf '[Service]\\n' > \"$output/systemd-user/executor-desktop.service\"\n  mkdir -p \"$output/wsl\"\n  printf 'WSL README\\n' > \"$output/wsl/README.txt\"\n  exit 0\nfi\nexit 1\n")
	writeStub(t, executorKillStub, "#!/usr/bin/env bash\nexit 0\n")
	writeStub(t, systemctlStub, "#!/usr/bin/env bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, runuserStub, "#!/usr/bin/env bash\nprintf 'runuser %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n")
	writeStub(t, idStub, "#!/usr/bin/env bash\nif [[ \"$1\" == \"-u\" && \"$2\" == \"jamie\" ]]; then printf '501\\n'; exit 0; fi\nif [[ \"$1\" == \"-gn\" && \"$2\" == \"jamie\" ]]; then printf 'jamie\\n'; exit 0; fi\nif [[ \"$1\" == \"-un\" ]]; then printf 'root\\n'; exit 0; fi\nif [[ \"$1\" == \"-u\" ]]; then printf '0\\n'; exit 0; fi\nexit 0\n")
	writeStub(t, chownStub, "#!/usr/bin/env bash\nexit 0\n")
	writeStub(t, chmodStub, "#!/usr/bin/env bash\nexit 0\n")
	writeStub(t, mktempStub, "#!/usr/bin/env bash\nmkdir -p \""+tmpBundle+"\"\nprintf '%s\\n' \""+tmpBundle+"\"\n")

	env := append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"COMMAND_LOG="+commandLog,
		"EXECUTOR_STATE_DIR="+stateDir,
		"EXECUTOR_INSTALL_ROOT="+installRoot,
		"EXECUTOR_BUNDLE_ROOT="+bundleRoot,
		"EXECUTOR_INSTALL_BINARY_PATH="+stableExecutorPath,
		"EXECUTOR_KILL_INSTALL_BINARY_PATH="+stableKillPath,
		"EXECUTOR_DOMAIN=executor.example.com",
		"EXECUTOR_CONFIG_PATH="+filepath.Join(stateDir, "config.json"),
		"CLOUDFLARED_TOKEN_PATH="+filepath.Join(stateDir, "cloudflared", "executor.token"),
		"SYSTEMCTL_BIN="+systemctlStub,
		"RUNUSER_BIN="+runuserStub,
		"ID_BIN="+idStub,
		"CHOWN_BIN="+chownStub,
		"CHMOD_BIN="+chmodStub,
		"MKTEMP_BIN="+mktempStub,
		"EXECUTOR_PROC_VERSION_PATH="+procVersionPath,
		"EXECUTOR_PERMISSION_RETRY_ATTEMPTS=2",
		"EXECUTOR_PERMISSION_RETRY_DELAY=0",
		"CLOUDFLARE_API_TOKEN_FILE="+apiTokenPath,
		"SUDO_USER=jamie",
		"WSL_INTEROP=/run/WSL/123_interop",
	)

	runScript(t, filepath.Join(root, "scripts", "bootstrap.sh"), env)

	log := readFile(t, commandLog)
	if !strings.Contains(log, "render-target=wsl") {
		t.Fatalf("bootstrap should render WSL bundle when WSL is detected:\n%s", log)
	}
	if !strings.Contains(log, "systemctl daemon-reload") {
		t.Fatalf("bootstrap should still manage systemd on WSL:\n%s", log)
	}

	manifest := filepath.Join(stateDir, "service-manifest.txt")
	if err := os.WriteFile(manifest, []byte("file|"+stableExecutorPath+"|remove\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runScript(t, filepath.Join(root, "scripts", "rollback.sh"), env)
	log = readFile(t, commandLog)
	if !strings.Contains(log, "systemctl stop executor-agent.service executor-broker.service executor-dashboard.service executor-cloudflared.service") {
		t.Fatalf("rollback should use the WSL/Linux systemd path after auto-detection:\n%s", log)
	}
}

func runScript(t *testing.T, script string, env []string) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Env = append(env, "CLOUDFLARED_BIN=/usr/bin/true")
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

func replaceInFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	body := readFile(t, path)
	if !strings.Contains(body, old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, old, replacement)), 0o755); err != nil {
		t.Fatal(err)
	}
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
