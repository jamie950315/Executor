# Executor Deployment

## Cloudflare Tunnel

Executor expects a remotely-managed Cloudflare Named Tunnel and a proxied CNAME record that points to `<tunnel-id>.cfargotunnel.com`.

- Create the tunnel with the Cloudflare Tunnel API.
- Apply remote ingress rules with a catch-all `http_status:404`.
- Point the public hostname at the tunnel target with a proxied CNAME.
- Fetch the tunnel token and store it in a mode-600 token file.
- Run `cloudflared tunnel run --token-file <path>` instead of storing a local credentials JSON or inline token.

This matches the current Cloudflare documentation for remotely-managed tunnels, including the tunnel token endpoint and `--token-file` support for `cloudflared` 2025.4.0 or later.

`executor setup` can now perform the packaging-time Cloudflare preparation step when given:

- `--cloudflare-token-file <path>` for a mode-600 API token file
- optional `--cloudflare-account-id <id>`
- optional `--cloudflare-zone-id <id>`
- optional `--cloudflare-tunnel-name <name>`

The packaging build never stores the Cloudflare API token in config. It persists only deployment metadata such as the selected account ID, zone ID, tunnel ID, DNS record ID, hostname, and managed tunnel token file path.

Executor runs its remotely managed tunnel through an isolated service: `com.executor.cloudflared` on macOS, `executor-cloudflared.service` on Linux/WSL, and `ExecutorCloudflared` on Windows. Normal bootstrap, rollback, Resume, and Kill Switch operations manage only that service. A one-time upgrade migration may stop a generic `cloudflared` service only when an earlier Executor manifest, ownership record, or Executor-created ImagePath backup proves that Executor previously created or replaced it; unrelated host services remain untouched, and restored host services return to their prior running state.

## Service Templates

The service bundle renderer produces:

- macOS LaunchDaemons for `executor agent` and `executor broker`
- macOS LaunchDaemon for `executor dashboard`
- macOS LaunchAgent for `executor desktop`, loaded into the real console owner's `gui/<uid>` session rather than `gui/0`
- macOS LaunchDaemon `com.executor.cloudflared` using `--token-file`
- Linux systemd units for `agent` and `broker`
- Linux systemd unit for `dashboard`
- Linux user unit for `desktop`, enabled through the invoking desktop user with `XDG_RUNTIME_DIR`
- Linux systemd unit `executor-cloudflared.service` using `--token-file`
- Windows PowerShell scripts for Executor service install, including `ExecutorDashboard` as a LocalSystem service, per-user desktop startup, and the isolated `ExecutorCloudflared` service with `--token-file`; the agent service uses a virtual service account instead of a persisted plaintext password
- WSL guidance that keeps desktop control on the Windows companion

Release artifacts include both `executor` and `executor-kill`. The bootstrap scripts install those bundled binaries into stable locations before creating services or tasks:

- Unix defaults: `/usr/local/bin/executor` and `/usr/local/bin/executor-kill`
- Windows defaults: `<install-root>/executor.exe` and `<install-root>/executor-kill.exe`

Persistent state also uses a stable default so lifecycle commands continue to work from a new terminal: `/var/lib/executor` on macOS/Linux/WSL and `%ProgramData%\Executor` on Windows. Set `EXECUTOR_STATE_DIR` to override it consistently for setup, services, rollback, and CLI commands.

New installations prefer `127.0.0.1:8787` for the Agent and `127.0.0.1:8788` for the Dashboard. If another process already owns either port, setup selects and persists an available loopback port before configuring the Cloudflare Tunnel. Status and doctor checks authenticate the Executor health responses, so unrelated services on the configured ports cannot be reported as a healthy Agent or Dashboard.

Setup reports the remote Streamable HTTP endpoint and local `executor stdio` command. Legacy SSE is not part of the current release. Every successful credential rotation also returns a new loopback Dashboard bootstrap URL; Dashboard authentication follows the current on-disk key immediately, so an old cookie stops working after an external `executor-kill` rotation.

Service templates always point at the stable installed `executor` path, never at the temporary extracted archive location.

## Rollback

- `scripts/bootstrap.sh` and `scripts/bootstrap.ps1` write a service manifest plus per-file backups under the state directory before replacing managed files or runtime startup entries.
- The bootstrap scripts also back up and replace the stable `executor` and `executor-kill` binaries through the same manifest workflow, so rollback restores or removes them together with the service files.
- Both bootstrap scripts require either a secure Cloudflare API token file for setup or a config that already contains completed Cloudflare deployment metadata. If that metadata is incomplete, they stop before installing or starting `cloudflared`.
- `scripts/bootstrap.sh` and `scripts/bootstrap.ps1` install and start the persistent local dashboard service together with agent, broker, desktop, and `cloudflared`.
- `scripts/rollback.sh` and `scripts/rollback.ps1` stop managed services, including the persistent local dashboard service, restore backed up files when present, and remove files or services that were created by the current install.
- The Unix packaging scripts do not create or delete a dedicated service identity; they bind the agent service to the real desktop owner account instead.
- `scripts/uninstall.sh` and `scripts/uninstall.ps1` run rollback first, then remove the local deployment state directory.
- The Go Cloudflare client deletes newly created DNS and tunnel resources in reverse order when a deployment step fails after creation, and removes any freshly written token file.

## GitHub Actions

- `.github/workflows/go.yml` runs `go test ./...` on Linux, macOS, and Windows.
- `scripts/build-release-artifacts.sh` cross-builds both `executor` and `executor-kill` for `darwin`, `linux`, and `windows` on `amd64` and `arm64`, packages each install bundle, and writes `SHA256SUMS.txt`.
- Darwin release jobs run on macOS with CGO enabled so desktop input uses native CoreGraphics rather than the no-CGO Swift fallback.
- `.github/workflows/release.yml` runs the build matrix, uploads per-target artifacts, and publishes consolidated checksums.

## Current platform verification boundary

- macOS arm64 has process-level end-to-end evidence for OAuth, MCP, owner terminal/filesystem, desktop observation, screenshot, Kill, Resume, and Dashboard key rollover. System LaunchDaemon installation and root Broker execution have not been exercised from this checkout.
- Linux and WSL currently have automated tests and cross-build evidence only.
- Windows terminal execution uses the native ConPTY API on Windows 10 version 1809, Windows Server 2019, or newer. Each terminal process tree is assigned to a kill-on-close Windows Job Object, so forced shutdown does not depend on `ClosePseudoConsole` returning promptly. A real Windows amd64 runtime test launched through WSL interoperability verifies persistent PowerShell state, case-insensitive environment overrides, working-directory preservation, live terminal resizing, LF command input, Ctrl+C interruption even when the launcher inherited an ignored Ctrl+C state, bounded Kill, and child-process termination. The same test verifies startup when the launcher has no parent console, as expected for a Windows Service. Windows service and active-desktop installation still require a full target-machine test.
