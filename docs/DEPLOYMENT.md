# Executor Deployment

## Deployment entrypoints

Use one of these paths:

- Release archive deployment: unpack the target platform release bundle and run the included bootstrap script.
- Clone deployment: use `scripts/deploy-from-source.sh` on macOS, Linux, or WSL, or `scripts/deploy-from-source.ps1` on Windows.

The source-deployment entrypoints are the canonical wrappers for a cloned repository. They are expected to build the local `executor` and `executor-kill` binaries from source, assemble the bundle assets required by the current platform, and then hand off to `scripts/bootstrap.sh` or `scripts/bootstrap.ps1`. A coding agent should use those wrapper names for clone deployments rather than calling `executor setup` alone.

If a checkout does not yet contain those wrapper scripts, treat that clone as missing its source-deployment entrypoints. In that case, either switch to a packaged release bundle or perform the same source-build-plus-bootstrap flow explicitly before claiming the clone is deployable.

Web ChatGPT cannot perform this first-time installation by itself. Until Executor is already running, ChatGPT on the web has no MCP access to the target machine, cannot install privileged services, and cannot satisfy the local secret and permission prompts needed for bootstrap.

## Prerequisites

- Go 1.24 or newer for source deployment
- `cloudflared` 2025.4.0 or newer installed on the target host and resolvable by the bootstrap script
- Administrator or root access
- Python 3 on macOS, Linux, and WSL
- `systemd` on Linux and WSL
- A Cloudflare-controlled hostname for the target machine
- A Cloudflare API token file kept outside the repository

On Unix targets, the Cloudflare API token file must be a regular file with mode `0600`. Setup rejects broader permissions.

## Centralized Unified Dashboard

Deploy the centralized Dashboard before enrolling any host. A local coding agent with terminal access can complete this from a fresh clone; web ChatGPT cannot install the first MCP or privileged services before a host connection exists.

Dashboard deployment additionally requires Node 20.19+, Node 22.13+, or Node 24+ and npm. Create a dedicated Cloudflare API token with only the selected account/zone and these capabilities:

- Account Workers Scripts write for the `executor-dashboard` Worker and its secret.
- Account D1 write for the exact `executor-dashboard` database and migrations.
- Zone Workers Routes edit, including Workers Custom Domains, for the requested Dashboard hostname.
- Access: Apps and Policies write for one hostname-based self-hosted application and one exact-email allow policy.
- Access organization read access so deployment can obtain the account's `auth_domain` as `ACCESS_TEAM_DOMAIN`.

The token file must remain outside the repository. Unix requires a regular mode-`0600` file. Windows requires an ACL restricted to the current owner, Administrators, and SYSTEM. The entrypoints reject inline token values and never print the token.

```bash
./scripts/deploy-dashboard-from-source.sh deploy \
  --hostname dashboard.example.com \
  --account-id 0123456789abcdef0123456789abcdef \
  --api-token-file /secure/cloudflare-dashboard.token \
  --allowed-email owner@example.com
```

```powershell
.\scripts\deploy-dashboard-from-source.ps1 deploy `
  -Hostname "dashboard.example.com" `
  -AccountId "0123456789abcdef0123456789abcdef" `
  -ApiTokenFile "C:\secure\cloudflare-dashboard.token" `
  -AllowedEmail "owner@example.com"
```

No tracked file needs editing. The same values can be supplied through `EXECUTOR_DASHBOARD_HOSTNAME`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN_FILE`, and `EXECUTOR_DASHBOARD_ALLOWED_EMAIL`. Optional `EXECUTOR_DASHBOARD_STATE_FILE` and `EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_FILE` paths must also remain outside the repository.

The canonical entrypoint performs all local package validation before remote mutation. It uses pinned Wrangler v4, checks Cloudflare authentication, creates or reuses one exact D1 database, creates only state-owned Access resources, renders a temporary ignored Wrangler config, applies all remote D1 migrations, and deploys Worker + Static Assets + SQLite Durable Object with a Workers Custom Domain route using `custom_domain: true`. It obtains `ACCESS_AUD` from the Access application response and `ACCESS_TEAM_DOMAIN` from Access organization state. `ENROLLMENT_TOKEN_HASH` receives only SHA-256 output; the generated bearer remains only in its protected local file.

Persistent non-secret state records resource IDs, ownership, schema/migration version, and the last completed stage. Retries are idempotent. Newer state or migrations fail closed. Ordinary retry never deletes Cloudflare resources. `rollback` targets only the Worker whose ownership is proven by state; D1 and Access removal is never automatic. Any destructive cleanup must be explicit and limited to IDs proven by Executor state.

### Enroll hosts

Transfer the generated enrollment file with an encrypted file-transfer mechanism. Do not print it, paste it into chat or a terminal command, put it in the repository, or copy its contents through the clipboard. Protect the received file before running the host wrapper.

```bash
sudo -E ./scripts/deploy-from-source.sh \
  --dashboard-url https://dashboard.example.com \
  --dashboard-enrollment-token-file /secure/dashboard-enrollment.copy \
  --dashboard-enrollment-token-temporary
```

Equivalent environment variables are `EXECUTOR_DASHBOARD_URL`, `EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_FILE`, and `EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_TEMPORARY=1`.

```powershell
.\scripts\deploy-from-source.ps1 `
  -DashboardUrl "https://dashboard.example.com" `
  -DashboardEnrollmentTokenFile "C:\secure\dashboard-enrollment.copy" `
  -DashboardEnrollmentTokenTemporary
```

The wrapper waits until local bootstrap, `executor status`, and `executor doctor --full` succeed. It then calls `executor dashboard enroll --url ... --token-file ...`, checks `executor dashboard status --json` and local service status, and deletes only the explicitly designated temporary copy after successful enrollment. Without the temporary flag it passes a separate protected helper-owned copy, so the source file is preserved. Enrollment failure does not invoke local rollback and does not remove a healthy existing installation. Loading the config through the CLI safely migrates v1/v2 state before enrollment.

After the last host is enrolled, close the enrollment window:

```bash
./scripts/deploy-dashboard-from-source.sh disable-enrollment \
  --hostname dashboard.example.com \
  --account-id 0123456789abcdef0123456789abcdef \
  --api-token-file /secure/cloudflare-dashboard.token \
  --allowed-email owner@example.com
```

Use `rotate-enrollment` with the same arguments to create a new protected bearer and invalidate every previous enrollment copy. Transfer the new file only to hosts that still require enrollment.

### Authentication and rescue boundaries

Cloudflare Access authentication opens the centralized workspace. The device recovery key separately unlocks sensitive operations for that one device. A control grant lasts at most 30 days and is bound to device ID, browser ID, Access subject, and credential generation. Rotate or Kill advances the generation and invalidates the old grant.

The authenticated loopback page on `127.0.0.1` is labeled emergency rescue. It shows local host/service state and exposes Resume, Rotate, and Kill with one-time replacement recovery material. It intentionally has no ordinary workspace or permission-management UI. Permission initialization remains available through `executor permissions`, MCP `device_permissions`, and the centralized Dashboard.

Representative clone-deployment commands:

```bash
# macOS, Linux, or WSL
export EXECUTOR_DOMAIN=executor.example.com
export CLOUDFLARE_API_TOKEN_FILE=/secure/cloudflare.token
sudo -E ./scripts/deploy-from-source.sh
```

```powershell
# Windows PowerShell
$env:EXECUTOR_DOMAIN = "executor.example.com"
$env:CLOUDFLARE_API_TOKEN_FILE = "C:\secure\cloudflare.token"
powershell.exe -ExecutionPolicy Bypass -File .\scripts\deploy-from-source.ps1
```

Optional inputs are shared by release and source deployments:

- `CLOUDFLARE_ACCOUNT_ID` when the token can see more than one Cloudflare account
- `CLOUDFLARE_ZONE_ID` when more than one zone could match the requested hostname
- `CLOUDFLARE_TUNNEL_NAME` to override the default tunnel name `executor`
- `EXECUTOR_STATE_DIR` to override the persistent state path consistently across setup, services, rollback, and lifecycle commands
- `EXECUTOR_DESKTOP_USER` on Windows only when automatic active-console-user detection cannot select the intended logged-in user

Do not treat `executor setup` by itself as a complete installation. `executor setup` prepares config, secrets, and optional Cloudflare metadata, but the packaged services are installed by `scripts/bootstrap.sh` or `scripts/bootstrap.ps1`.

## Permission initialization

Both bootstrap scripts start the active-user Desktop helper, wait for its authenticated local IPC endpoint with a short bounded retry, and then call `executor permissions request-all`. On macOS, the permission owner is therefore the installed `Executor Desktop.app`, not the root bootstrap process, Terminal, the CLI binary, or the Dashboard daemon. If no active unlocked desktop is available before the retry window ends, bootstrap keeps the service installation successful, prints a warning, and the owner or deploying agent must run the same command after sign-in.

Available interfaces use the same report and the same Desktop-helper boundary:

- CLI: `executor permissions status [--json]` and `executor permissions request-all [--json]`
- MCP: `device_permissions` with `action: "status"` or `action: "request_all"`
- Centralized Unified Dashboard after device enrollment

`requested: true` means Executor invoked the available operating-system request mechanisms. It does not mean the owner approved them. A report is `ready: true` only when every required item is `granted` or `not_required`. On macOS, Screen Recording, Accessibility, and Input Control may remain `pending` until the owner approves System Settings prompts; the report may also request a helper restart. Full Disk Access has no supported automatic grant API, remains an optional `manual` item, and opens its System Settings page. Windows verifies that the helper can open the active `Default` input desktop, so a locked, disconnected, or secure desktop is not reported ready; Screen Capture and Input Control require no separate consent grant. Linux and WSL verify the live AT-SPI bus and, on Wayland, `ydotoold` access rather than trusting executable presence alone. X11 also requires `wmctrl`. When newly installed tools differ from the helper's startup inventory, the report remains not ready and requests a helper restart. The current Wayland backend does not create an unused XDG Desktop Portal session.

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

The Cloudflare token must be able to:

- list accessible accounts when an explicit account ID is not provided
- list zones for the selected account when an explicit zone ID is not provided
- create or reuse a remotely-managed named tunnel
- fetch the tunnel runtime token
- write the tunnel ingress configuration
- create or update the proxied CNAME record for the public hostname

Selection rules:

- If the token can see exactly one account, setup can infer it.
- If the token can see more than one account, provide `CLOUDFLARE_ACCOUNT_ID` or `--cloudflare-account-id`.
- If the hostname matches exactly one accessible zone suffix, setup chooses the longest matching zone name.
- If no accessible zone matches, setup fails; provide the correct hostname or `CLOUDFLARE_ZONE_ID`.

Executor runs its remotely managed tunnel through an isolated service: `com.executor.cloudflared` on macOS, `executor-cloudflared.service` on Linux/WSL, and `ExecutorCloudflared` on Windows. Normal bootstrap, rollback, Resume, and Kill Switch operations manage only that service. A one-time upgrade migration may stop a generic `cloudflared` service only when an earlier Executor manifest, ownership record, or Executor-created ImagePath backup proves that Executor previously created or replaced it; unrelated host services remain untouched, and restored host services return to their prior running state.

### Cloudflare edge protection and OAuth

ChatGPT's DCR and token requests are API traffic. Cloudflare Bot Fight Mode may challenge or reject that traffic before it reaches Executor. Cloudflare documents that Bot Fight Mode applies across the entire zone and cannot be bypassed with WAF Skip, Bypass, Allow, or Page Rules. Use one of these supported configurations:

- Disable Bot Fight Mode for the zone. On a shared zone, this changes protection for every hostname in that zone.
- Use Super Bot Fight Mode and create a narrowly scoped Skip rule for the Executor hostname and OAuth/MCP paths.
- Use a dedicated Cloudflare zone for Executor if the shared zone must retain Bot Fight Mode.

Do not treat a browser-visible Cloudflare challenge or a public `POST /oauth/register` HTTP 403 as an Executor recovery-key failure. Run `executor doctor --full` after the tunnel is live. Its `remote OAuth DCR` check posts an intentionally invalid empty registration document. A healthy public route returns Executor's `invalid_client_metadata` response without creating an OAuth client. For HTTP 403, inspect Cloudflare Security > Analytics > Events to identify the matching service. Disable Bot Fight Mode or replace it with Super Bot Fight Mode plus a Skip rule only when the event identifies Bot Fight Mode; otherwise adjust the matching Access or WAF policy.

Relevant paths are `/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `/oauth/register`, `/oauth/authorize`, `/oauth/token`, and `/mcp`. Keep OAuth, PKCE, recovery-key consent, and bearer-token enforcement enabled even when edge bot protection is skipped.

Cloudflare reference: [Bot Fight Mode limitations](https://developers.cloudflare.com/bots/get-started/bot-fight-mode/). OpenAI reference: [MCP authentication](https://developers.openai.com/plugins/build/auth).

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
- Windows PowerShell scripts for Executor service install, including `ExecutorDashboard` as a LocalSystem service, per-user desktop startup without Windows' default 72-hour task limit, and the isolated `ExecutorCloudflared` service with `--token-file`; the agent service uses a virtual service account instead of a persisted plaintext password
- WSL guidance that keeps desktop control on the Windows companion

Release artifacts include both `executor` and `executor-kill`. The bootstrap scripts install those bundled binaries into stable locations before creating services or tasks:

- Unix defaults: `/usr/local/bin/executor` and `/usr/local/bin/executor-kill`
- Windows defaults: `<install-root>/executor.exe` and `<install-root>/executor-kill.exe`

Persistent state also uses a stable default so lifecycle commands continue to work from a new terminal: `/var/lib/executor` on macOS/Linux/WSL and `%ProgramData%\Executor` on Windows. Set `EXECUTOR_STATE_DIR` to override it consistently for setup, services, rollback, and CLI commands.

New installations prefer `127.0.0.1:8787` for the Agent and `127.0.0.1:8788` for the Dashboard. If another process already owns either port, setup selects and persists an available loopback port before configuring the Cloudflare Tunnel. Status and doctor checks authenticate the Executor health responses, so unrelated services on the configured ports cannot be reported as a healthy Agent or Dashboard.

Setup reports the remote Streamable HTTP endpoint and local `executor stdio` command. Legacy SSE is not part of the current release. Every successful credential rotation also returns a new loopback Dashboard bootstrap URL; Dashboard authentication follows the current on-disk key immediately, so an old cookie stops working after an external `executor-kill` rotation.

## Per-platform deployment notes

### macOS

- Run the source or release deployment path from a local Terminal session with `sudo`.
- Source deployments need Go 1.24+, Python 3, and `cloudflared`.
- Grant Screen Recording and Accessibility to the installed `/Library/Application Support/Executor/Executor Desktop.app`.
- Clone deployments should use `scripts/deploy-from-source.sh`; packaged releases should use `scripts/bootstrap.sh`.

### Linux

- Requires `systemd`, Python 3, Go 1.24+ for source deployment, and `cloudflared`.
- Run deployment as root or through `sudo` so the system units and user desktop unit can be installed.
- X11 desktop control needs ImageMagick `import`, `gdbus` with a live AT-SPI bus, `xdotool`, and `wmctrl`.
- Wayland desktop control needs `grim` or `gnome-screenshot`, `gdbus` with a live AT-SPI bus, and `wtype` or an authorized `ydotoold`; advanced mouse actions remain limited by compositor support.

### WSL

- Requires `systemd` inside the WSL distribution, Python 3, Go 1.24+ for source deployment, and `cloudflared`.
- Use the Linux deployment path for terminal, filesystem, OAuth, and tunnel services.
- Linux GUI control requires WSLg. Windows desktop control still belongs on the Windows companion installation.

### Windows

- Run deployment from an elevated PowerShell session.
- Source deployments need Go 1.24+ and `cloudflared`.
- The packaged services install the desktop helper as an active-user startup path instead of a Windows Service.
- Clone deployments should use `scripts/deploy-from-source.ps1`; packaged releases should use `scripts/bootstrap.ps1`.

## Computer Use runtime

The active-user Desktop helper provides the screenshot and input boundary. A Computer Use client first calls `desktop_observe` with `action=screenshot`, then sends the returned `captureId` to `desktop_control` with `action=batch`. Executor validates the complete batch and platform capability before input begins, checks every coordinate against the captured image, consumes the capture ID once, serializes desktop observation and control in both the Agent and Desktop helper, and returns a new screenshot after the batch. Remote capture continuity is bound to the authenticated client identity because ChatGPT may create a fresh MCP session for each tool call; local stdio capture continuity remains bound to its MCP session. Any control from any MCP session invalidates all older captures. IPC disconnects cancel the in-flight helper request, and batch wait limits keep normal execution inside the IPC window.

Screenshot data is carried in MCP image content. Each screenshot result starts with a short text block containing the complete capture ID and dimensions, while structured content contains metadata only: the capture ID, dimensions, MIME type, and capture time. Both screenshot-producing tools declare the matching MCP output schema. PNG is preferred; oversized high-detail PNGs are re-encoded as JPEG at the same dimensions before IPC transfer. Capture files live inside a mode-700 temporary directory, are changed to mode 600 after the platform screenshot tool exits, and are removed immediately after reading. Screenshot bytes, typed text, command text, file contents, and tool output are excluded from the metadata-only audit log.

The Desktop helper must run inside an active, unlocked user session with screen-recording and accessibility/input permissions granted by the operating system. Terminal, filesystem, Broker, Dashboard, and Kill Switch operation do not depend on the desktop being unlocked.

- macOS captures the primary display and normalizes Retina pixels to display-point coordinates before returning the image.
- Windows captures the primary display so `SetCursorPos` coordinates match the returned image. Permission status opens and names the current input desktop, and reports ready only for the interactive `Default` desktop. The active-user Scheduled Task remains required and is configured with no execution-time limit; a Windows Service cannot interact with the logged-in desktop.
- Linux X11 supports the complete action set when the documented screenshot, accessibility, `xdotool`, and `wmctrl` dependencies are available.
- Wayland support is compositor-dependent. Executor reports unsupported modifier-assisted mouse, double-click, drag, and scroll actions instead of silently claiming success when the available `ydotool` path cannot execute them reliably.
- WSL uses WSLg for Linux GUI control. Use the Windows companion to control the Windows desktop.

### macOS Desktop helper signing and permissions

Darwin release archives contain `Executor Desktop.app` with bundle identifier `dev.0ruka.executor.desktop`. Bootstrap installs the complete app at `/Library/Application Support/Executor/Executor Desktop.app`, and the active-user LaunchAgent runs its embedded `executor-desktop` executable. Grant Screen Recording and Accessibility to this installed app in System Settings > Privacy & Security. Permissions granted to Terminal, an extracted temporary binary, or an older helper identity do not authorize the LaunchAgent.

Set `EXECUTOR_MACOS_SIGN_IDENTITY` when building Darwin release artifacts. Public production releases should use a valid `Developer ID Application` identity so the bundle identifier, Team ID, and designated requirement remain stable across upgrades. Local development may use an `Apple Development` identity. If the variable is omitted, the build script uses ad-hoc signing; that is suitable only for local testing and macOS may require Screen Recording and Accessibility to be granted again after an upgrade.

If an AI agent performs setup, Kill, rotation, or another action that generates a recovery key, the agent must reproduce that recovery key verbatim in its final private response to the requesting owner. It must not redact the key or direct the owner to an unattended Terminal. The response must identify the key as sensitive and shown once, and instruct the owner to save it immediately. This delivery exception does not allow the key to be stored in files, configuration, persistent logs, issues, pull requests, or public channels.

Treat every setup attempt as recovery-key-sensitive. Watch the setup output directly. If a recovery key is printed, capture it immediately and treat it as the newest valid key for that host. Repeated setup with an existing secret store states that the existing key remains unchanged and cannot display it again; keep using the newest known valid key or rotate credentials deliberately.

OAuth authorization metadata currently directs ChatGPT through Dynamic Client Registration (DCR), because repeated real ChatGPT web callback attempts stopped before token exchange when CIMD was advertised. The CIMD resolver remains implemented but is not advertised until that callback path is interoperable. The token endpoint accepts both public-client `none` with PKCE and ChatGPT's `private_key_jwt` method with RS256 verification against the JWKS published by the trusted ChatGPT CIMD origin.

The authorization form uses a native HTML submit input for mobile Safari reliability and preserves ChatGPT's `resource` parameter through authorization and token exchange. An `authorization denied` response is generated only when the submitted recovery key does not match the current verifier. Because every Kill or rotation invalidates the previous key immediately, always use the newest key for the exact hostname being linked.

Service templates always point at the stable installed `executor` path, never at the temporary extracted archive location.

## Rollback

- `scripts/bootstrap.sh` and `scripts/bootstrap.ps1` write a service manifest plus per-file backups under the state directory before replacing managed files or runtime startup entries.
- The bootstrap scripts also back up and replace the stable `executor` and `executor-kill` binaries through the same manifest workflow, so rollback restores or removes them together with the service files.
- Both bootstrap scripts validate the complete bundle and require either a secure, nonblank Cloudflare API token file or a config with completed Cloudflare metadata plus its runtime tunnel token. Invalid input stops before binaries, state, credentials, or services are changed.
- `scripts/bootstrap.sh` and `scripts/bootstrap.ps1` install and start the persistent local dashboard service together with agent, broker, desktop, and `cloudflared`.
- `scripts/rollback.sh` and `scripts/rollback.ps1` stop managed services, including the persistent local dashboard service, restore backed up files when present, and remove files or services that were created by the current install.
- The Unix packaging scripts do not create or delete a dedicated service identity; they bind the agent service to the real desktop owner account instead.
- `scripts/uninstall.sh` and `scripts/uninstall.ps1` run rollback first, then remove the local deployment state directory.
- The Go Cloudflare client deletes newly created DNS and tunnel resources in reverse order when a deployment step fails after creation, and removes any freshly written token file.

Use these commands after a failed or unwanted installation:

```bash
# macOS, Linux, or WSL rollback
sudo ./scripts/rollback.sh
```

```bash
# macOS, Linux, or WSL full uninstall
sudo ./scripts/uninstall.sh
```

```powershell
# Windows rollback
powershell.exe -ExecutionPolicy Bypass -File .\scripts\rollback.ps1
```

```powershell
# Windows full uninstall
powershell.exe -ExecutionPolicy Bypass -File .\scripts\uninstall.ps1
```

Rollback is the right first response when bootstrap replaced files or installed services but the deployment did not pass validation. Uninstall is for removing the local installation and state after rollback.

## Validation checklist

After deployment:

1. Run `executor status` and confirm the host reports `armed`.
2. Run `executor doctor --full`; every local check and `remote OAuth DCR` must pass.
3. Confirm `GET /.well-known/oauth-protected-resource` and `GET /.well-known/oauth-authorization-server` return JSON through the public hostname.
4. Confirm unauthenticated `POST /mcp` or `GET /mcp` returns HTTP 401 with `WWW-Authenticate`, not a Cloudflare HTML challenge.
5. Link the exact hostname in ChatGPT and enter that host's newest recovery key.
6. If desktop control is required, verify one screenshot and one explicit follow-up action after the platform permissions are granted.

## GitHub Actions

- `.github/workflows/go.yml` runs `go test ./...` on Linux, macOS, and Windows.
- `scripts/build-release-artifacts.sh` cross-builds both `executor` and `executor-kill` for `darwin`, `linux`, and `windows` on `amd64` and `arm64`, packages each install bundle, and writes `SHA256SUMS.txt`.
- OAuth, recovery-key, and remote DCR diagnostics live in OS-neutral Go packages. The test suite exercises the same implementation used by every target, while the release build verifies that it compiles into all six platform/architecture archives.
- Darwin release jobs run on macOS with CGO enabled so desktop input uses native CoreGraphics rather than the no-CGO Swift fallback.
- `.github/workflows/release.yml` runs the build matrix, uploads per-target artifacts, and publishes consolidated checksums.

## Current platform verification boundary

- macOS arm64 has process-level end-to-end evidence for OAuth, MCP, owner terminal/filesystem, desktop observation, screenshot, Kill, Resume, and Dashboard key rollover. A persistent installation also verifies the Agent, root Broker, Dashboard, active-user Desktop LaunchAgent, and isolated cloudflared LaunchDaemon.
- Raspberry Pi OS and Debian WSL installations verify systemd Agent, root Broker, Dashboard, active-user Desktop helper, isolated cloudflared, OAuth discovery, and authenticated MCP protection. Other Linux desktop environments and display-server combinations remain platform-dependent.
- Windows terminal execution uses the native ConPTY API on Windows 10 version 1809, Windows Server 2019, or newer. Each terminal process tree is assigned to a kill-on-close Windows Job Object, so forced shutdown does not depend on `ClosePseudoConsole` returning promptly. Real Windows amd64 tests verify persistent PowerShell state, case-insensitive environment overrides, working-directory preservation, live terminal resizing, LF command input, Ctrl+C interruption even when the launcher inherited an ignored Ctrl+C state, bounded Kill, child-process termination, Windows Service execution, and the active-user Desktop Scheduled Task.
- Isolated named Tunnels are live-tested at separate Pi5, macOS, Windows, and WSL hostnames. Every public endpoint publishes valid OAuth metadata and rejects unauthenticated MCP requests. Deployment API tokens are not retained after setup; each host keeps only its own permission-restricted tunnel runtime token.
