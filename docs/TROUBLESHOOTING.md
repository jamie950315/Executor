# Executor Troubleshooting

This page records the verified causes of the OAuth and rotation failures encountered during the first ChatGPT deployments. Diagnose by boundary: Cloudflare edge, Executor OAuth, local state ownership, and browser consent are separate systems.

## Before the first install

Web ChatGPT cannot self-install Executor onto a fresh machine. Before MCP exists, ChatGPT on the web has no host connection, cannot build local binaries, cannot install privileged services, and cannot complete the required local secret and permission steps. Perform the first deployment from a local coding agent, a direct terminal session, or another already-installed host tool.

For clone deployments, the documented source-deployment entrypoints are `scripts/deploy-from-source.sh` on macOS, Linux, and WSL, and `scripts/deploy-from-source.ps1` on Windows. Those wrappers are the canonical source-build path; `executor setup` alone is not a complete installation.

## Recommended validation order

1. Run `executor status` and confirm the host is `armed`.
2. Run `executor doctor --full`. All local checks and `remote OAuth DCR` should pass.
3. Run `executor permissions status` and finish every required operating-system approval or dependency.
4. Confirm `GET /.well-known/oauth-protected-resource` and `GET /.well-known/oauth-authorization-server` return JSON through the public hostname.
5. Confirm unauthenticated `/mcp` returns HTTP 401 with `WWW-Authenticate` rather than a Cloudflare HTML challenge.
6. Link the exact hostname in ChatGPT and enter that host's newest recovery key.

## Permission request completed but status is not ready

**Observed symptom:** `executor permissions request-all`, the centralized Dashboard, or MCP `device_permissions action=request_all` returns a report with `requested: true`, but one or more required permissions are still `pending`, `denied`, `manual`, or `unavailable`.

**Expected cause:** Operating systems do not allow Executor to silently approve owner consent. On macOS, the native request calls return before the owner finishes System Settings. On Linux and WSL, missing desktop tools must be installed or configured; the AT-SPI bus and `ydotoold` authorization are checked live, and X11 also requires `wmctrl`. On Windows, the active-user helper must run on the unlocked interactive `Default` desktop.

**Correct fix:** Complete the shown prompts or dependency instructions, restart the active-user Desktop helper when the report requests it, and run `executor permissions status` again. Linux tool discovery is captured when the helper starts, so installing a newly missing executable requires that restart. Grant macOS permissions to `/Library/Application Support/Executor/Executor Desktop.app`; permissions granted only to Terminal or `/usr/local/bin/executor` do not authorize the helper.

## Public DCR returns HTTP 403

**Observed symptom:** ChatGPT could not register an OAuth client. Direct DCR requests returned HTTP 403, and no matching OAuth registration reached Executor.

**Verified cause:** Cloudflare Bot Fight Mode challenged non-browser API traffic before it reached the tunnel origin. This was not an Executor OAuth rejection.

**Correct fix:** First inspect Cloudflare Security > Analytics > Events and identify the service that produced the 403. If the event identifies Bot Fight Mode, disable it for the zone, use Super Bot Fight Mode with a narrowly scoped Skip rule for the Executor hostname and OAuth/MCP paths, or move Executor to a dedicated zone. Free-plan Bot Fight Mode cannot be skipped by WAF custom rules or Page Rules. If the event identifies Access or WAF instead, adjust that matching policy rather than changing Bot Fight Mode. Validate with `executor doctor --full`; the check does not create a client.

## `authorization denied`

**Observed symptom:** The Executor consent page loaded, but submitting it returned `authorization denied`.

**Verified cause:** Executor returns this response only when the submitted recovery key does not match the current verifier. Kill and rotation generate a new key and invalidate the old key immediately. With several hosts, a valid key for one hostname is invalid for every other hostname.

**Correct fix:** Use the newest recovery key produced for that exact hostname. An AI-managed setup or rotation must return the complete new key in the same private owner chat. Store keys in a password manager labeled by hostname; never store them in the repository, logs, issues, or public channels.

If repeated setup reports that the existing recovery key remains unchanged, no new key was created and the old key cannot be displayed again. Use the newest known valid key or rotate credentials explicitly.

## Consent button does not submit on mobile Safari

**Observed symptom:** Pressing Return in the recovery-key field submitted the form, but tapping the styled authorization button did not.

**Verified boundary:** The recovery-key form and server endpoint were working because keyboard submission reached the server. The failed interaction was the button activation path in the observed mobile Safari flow.

**Applied fix:** Executor renders a native `<input type="submit">` control with mobile touch behavior instead of relying on the previous styled `<button>`. The form also preserves the OAuth `resource` parameter.

## Agent logs show permission denied after root rotation

**Observed symptom:** On macOS, a root-initiated Kill or rotation was followed by repeated failures to read `oauth-state.json`; the same ownership model applies to Linux and WSL.

**Verified cause:** Atomic state replacement created the new Unix file with the rotating process's ownership. The normal-user Agent then lost read access.

**Applied fix:** Unix OAuth state replacement preserves the existing owner and group before the atomic rename. macOS, Linux, and WSL use this common path. Windows has no Unix uid/gid transition and keeps state under `%ProgramData%\Executor` with the installed service permissions. Resume verifies that the Agent, Broker, and Desktop loaded the rotated credentials before restarting the tunnel.

## macOS screenshot fails only under LaunchAgent

**Observed symptom:** `desktop_observe` reports `screenshot unavailable`, while `screencapture` works from an interactive Terminal. The Desktop helper log may report `could not create image from display`.

**Verified cause:** Screen Recording is granted by macOS TCC to a stable signed application identity. A standalone ad-hoc helper binary launched from changing build paths does not provide the stable bundle identifier and signing requirement needed for persistent LaunchAgent permission.

**Correct fix:** Install the release's signed `/Library/Application Support/Executor/Executor Desktop.app`, then grant Screen Recording and Accessibility to that app in System Settings > Privacy & Security. Restart the `com.executor.desktop` LaunchAgent after permission changes. Production archives should be signed with a stable `Developer ID Application` identity through `EXECUTOR_MACOS_SIGN_IDENTITY`; ad-hoc builds may require permission to be granted again after upgrades. Do not grant these permissions only to Terminal or to `/usr/local/bin/executor`.

## Clone deployment fails before services start

**Observed symptom:** A source deployment from a fresh clone fails before the services are installed.

**Common verified causes:**

- Go 1.24 or newer is missing.
- `cloudflared` 2025.4.0 or newer is not installed or not resolvable on the target machine.
- On macOS, Linux, or WSL, the Cloudflare API token file is not a regular file with mode `0600`.
- On Linux or WSL, `systemd` is not available.
- On macOS, Linux, or WSL, Python 3 is missing from the bootstrap path.

**Correct fix:** Satisfy the prerequisite that failed, then rerun the source-deployment entrypoint. If bootstrap already replaced files or installed services before the failure, run the platform rollback script before retrying.

## Unified Dashboard deployment or enrollment fails

Diagnose the exact stage printed by `scripts/deploy-dashboard-from-source.sh` or `.ps1`. The entrypoint preserves non-secret retry state outside the repository and does not delete unrelated Cloudflare resources.

- **Access JWT rejected:** confirm the Access application response AUD matches Worker `ACCESS_AUD`. Confirm `ACCESS_TEAM_DOMAIN` is the account organization `auth_domain`, such as `team.cloudflareaccess.com`, without a scheme or path. A valid Access login for another application or team does not satisfy this Worker.
- **Access organization lookup returns 403:** either grant the API token Access organization read permission or pass the public Zero Trust team domain through `--access-team-domain` / `-AccessTeamDomain`. Apps and Policies write permission alone does not grant organization read permission.
- **Dashboard hostname does not route:** inspect the temporary config contract and deployed Worker routes. The exact hostname must be a Workers Custom Domain with `custom_domain: true`, not a path route accidentally attached to a per-device tunnel. The centralized hostname must not replace any device MCP hostname.
- **D1 migration failure:** verify the state-owned D1 UUID and inspect the remote `d1_migrations` names in order. Executor refuses unknown/newer names, missing older names, reordered history, and a missing migration table on an adopted database. A newly created Executor-owned D1 database may legitimately have no table before its first migration. Never point Executor migrations at a similarly named foreign database and never edit the table to force a retry.
- **Browser works but device enrollment or WebSocket gets an Access response:** verify two separate account-level self-hosted applications. The exact hostname must have only the Executor-owned exact-email `allow` policy. The more-specific `<hostname>/api/device/*` application must have only the Executor-owned `bypass` policy. Cloudflare applies the more-specific path without inheriting the hostname policy. Never bypass `/api/devices/*`, `/api/session`, assets, or UI routes; the Worker continues to reject invalid enrollment bearers, origins, device identities, and signed challenges.
- **Enrollment returns 401:** enrollment may be disabled, the transferred copy may be stale after `rotate-enrollment`, or the Worker secret may not contain the hash for the current protected file. Do not print the bearer to compare it. Rotate a new file centrally, transfer it securely, protect it, and retry.
- **Enrollment succeeded but token cleanup fails:** the token path changed after Executor read it, so the replacement file was deliberately preserved instead of being deleted. Keep the replacement protected, confirm it is the intended current enrollment file, and rerun enrollment; the saved cleanup fingerprint prevents a duplicate enrollment POST when the original token is still present.
- **Enrollment succeeded but relay is offline:** run `executor dashboard status --json`, `executor status --json`, and local service logs. `relay` is live runtime state, not a configuration marker; stale or crashed Dashboard status reports `disconnected`. Confirm the Dashboard origin is HTTPS and that only the exact `/api/device/*` Access application bypasses the owner login flow.
- **Windows Desktop task exits with result 1 and `load config: open config lock`:** install the latest bundle and rerun `bootstrap.ps1`. The bootstrap grants only the resolved active Desktop user and Executor service identities the modification rights required on `.config.lock` and `.secrets.lock`; it does not make `config.json`, `secrets.json`, or the state directory broadly writable. Then start `ExecutorDesktop` and rerun `executor doctor --full`.

Cloudflare Access login and device recovery-key unlock are separate. Access identifies the centralized user; the per-device key creates a 30-day grant bound to the device, browser, Access subject, and credential generation. A new browser, Access user, or post-Rotate generation requires another unlock.

After a full Kill disconnects the relay, open the authenticated localhost rescue URL on that host. The localhost rescue page remains available on `127.0.0.1` for service state, Resume, Rotate, and Kill; it is not the centralized workspace. Save any one-time recovery result immediately.

For rollback, use only the explicit Dashboard `rollback` command, an explicitly recorded Executor-owned `--version-id`, and the protected state belonging to the same account and hostname. The current remote deployment must still match state before rollback. Do not delete a D1 database, Access application, policy, Custom Domain, device Tunnel, or DNS record unless Executor state proves ownership and the owner explicitly requested destructive cleanup.

## Cloudflare account or zone selection fails

**Observed symptom:** Setup fails while choosing the Cloudflare account or zone for the requested hostname.

**Verified cause:** Automatic selection only works when the token can see exactly one account and the requested hostname matches at least one accessible zone. With multiple accounts, or with no matching zone suffix, setup cannot infer the target reliably.

**Correct fix:** Supply `CLOUDFLARE_ACCOUNT_ID` or `--cloudflare-account-id` when the token can see multiple accounts. Supply `CLOUDFLARE_ZONE_ID` or `--cloudflare-zone-id` when the hostname cannot be matched automatically. Keep the hostname inside a zone that the same token can manage.

## ChatGPT receives the first screenshot but does not call `desktop_control`

**Observed symptom:** ChatGPT shows the first capture ID or image, then stops without executing the requested batch. Executor's metadata-only audit contains a successful `desktop_observe` but no `desktop_control` attempt.

**Boundary:** No request reached Executor, so this is not a Desktop helper, capture validation, or input failure. `desktop_control` is intentionally declared destructive because the same tool can click and type; Executor must not mislabel it to suppress a client-side approval boundary.

**Correct diagnosis:** Give ChatGPT an explicit instruction naming the exact allowed batch and approve the tool call if the client presents a confirmation. Use the audit log to distinguish a missing client call from an Executor rejection. If there is no `desktop_control` attempt and the client gives a truncated response, retry in a new chat or report the ChatGPT client behavior; do not weaken the tool annotation.

## CIMD flow stops before token exchange

**Observed symptom:** Real ChatGPT web attempts using advertised CIMD completed consent but did not reach a successful token exchange.

**Known boundary:** The exact external callback failure was not proven to be an Executor protocol defect. During diagnosis, Executor added standards-compatible `none` and `private_key_jwt` handling, trusted ChatGPT JWKS verification, and `resource` preservation. Repeated real tests still did not complete through the advertised CIMD path.

**Current compatibility mode:** Executor does not advertise CIMD and directs ChatGPT through DCR, which completed a real authorization-code + PKCE exchange. The CIMD resolver remains implemented for future interoperability testing. Do not re-advertise CIMD based only on unit tests; require a real ChatGPT callback and token-exchange success first.

## Cross-platform build boundary

OAuth, consent, recovery-key validation, state persistence, and remote DCR diagnostics are shared Go code. They are not separate macOS, Windows, Linux, or WSL implementations. Platform-specific service installers only determine how the shared binary is started and where state is stored.

### Windows Desktop helper stops after three days

**Observed symptom:** Owner terminals, `device_status action=summary`, and desktop tools return an internal error while the Agent, Broker, and Cloudflare services remain healthy. `ExecutorDesktop` is `Ready`, its last result is `0x41306`, and no active-user `executor.exe` process is running.

**Cause:** `New-ScheduledTaskSettingsSet` defaults to a 72-hour execution limit. A long-running Desktop helper is terminated when that limit expires.

**Correct fix:** Reinstall the current service bundle. The generated `ExecutorDesktop` task sets `ExecutionTimeLimit` to zero, which Windows represents as an unlimited duration. Start the task inside the active user's logged-in session and confirm that owner terminal and desktop IPC calls succeed.

### ChatGPT initially shows a truncated or empty answer after successful tools

**Observed symptom:** Executor's audit records show successful `device_status`, `terminal`, and `terminal_output` calls, but ChatGPT displays only the first word or an empty final answer.

**Executor compatibility gap:** Older builds supplied `structuredContent` but left `content` empty. MCP recommends a serialized text representation for backwards compatibility, so current builds return one while preserving explicit multimodal content for screenshots.

**Verified ChatGPT Safari behavior:** A completed Work response can remain visually stuck on its first streamed token even for a no-tool prompt. Reloading the same conversation reveals the complete stored response and tool details. This rendering problem is outside Executor; do not treat the initially visible fragment as proof that an MCP call failed.

**Correct verification:** Reload the ChatGPT conversation if streaming stops on a fragment, then verify the complete stored response, the returned MCP payload, and the host-side metadata-only audit record.

## Deployment failed after files or services changed

**Observed symptom:** Bootstrap replaced files, installed services, or changed Cloudflare-managed state, but validation did not pass.

**Correct fix:** Use rollback before retrying:

- macOS, Linux, WSL: `sudo ./scripts/rollback.sh`
- Windows: `powershell.exe -ExecutionPolicy Bypass -File .\scripts\rollback.ps1`

Use uninstall only when you want to remove the local installation and state entirely:

- macOS, Linux, WSL: `sudo ./scripts/uninstall.sh`
- Windows: `powershell.exe -ExecutionPolicy Bypass -File .\scripts\uninstall.ps1`

Before publishing a change, run the complete Go tests, race tests, vet, shell syntax checks, and `scripts/build-release-artifacts.sh`. The release script must produce `executor` and `executor-kill` archives for Darwin, Linux, and Windows on both amd64 and arm64. Real host validation remains required for launchd, systemd, Windows Services, ConPTY, WSL, and each public Cloudflare hostname.
