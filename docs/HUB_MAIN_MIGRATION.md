# Hub mainline migration

Local main merge: `923013c`. The merge resolved documentation only; source code
matches the accepted Hub branch. The four-host service cutover is complete;
this is not a new tagged release. Credentials, enrolled device IDs and the
existing pilot-state locations are retained.

## Current MCP endpoint

The canonical Hub URL is **`https://executor.0ruka.dev/mcp`**. On October 1,
2026, the existing Pi5 Cloudflare Tunnel gained this proxied hostname with
the same `http://127.0.0.1:28787` origin. Tunnel configuration version 4 preserves
all earlier ingress rules. Hub OAuth issuer, resource and endpoint metadata now
use `https://executor.0ruka.dev`; the existing OpenAI Tunnel profile targets the
new MCP URL while retaining its registration and environment-key reference.

The previous `https://beta-executor-hub-pi5.0ruka.dev/mcp` remains an ingress to
the same Hub. Its original OAuth resource
`https://beta-executor-hub-pi5.0ruka.dev` is an explicit resource alias alongside
the existing OpenAI Tunnel alias. Existing access tokens with the old issuer
require refresh; clients may need reauthorization if they cannot refresh. Use
the new URL for new or updated client connections. A Hub restart invalidates its
terminal IDs without stopping the device-side terminal processes; list existing
sessions again after reconnecting.

Public HTTPS checks from Mac and Pi5 pass OAuth discovery with the new issuer
and resource, plus an unauthenticated MCP 401. The OpenAI Tunnel is live and
ready, and authenticated MCP discovery after its target switch lists all four
devices online and authorized. A Mac admin argv session through the Hub cleared
the local DNS cache, exited with code 0 and was closed. Pi5 DNS cache was cleared
with an AdGuard Home restart; its configuration hash is unchanged. No new binary
was installed. Credential, Hub identity and device-delegation hashes match the
pre-change snapshots; Pi5 device helpers, relay and cloudflared kept their process IDs.

Rollback data is in
`/home/jamie/.local/share/executor-hub-pilot/deployment-backups/hostname-executor-20261001`
on Pi5. Restore `config.before.json` to `hub-state/config.json`, preserving its
owner and mode, and restore only the `mcp` section of the existing OpenAI Tunnel
profile from `tunnel-mcp.before.json`. Restart `executor-hub.service` and
`executor-hub-tunnel.service`. `cloudflare.before.json` records the original DNS
and Tunnel configuration. The retained old ingress supports this rollback;
credentials, registration and device services do not need replacement.

## Installed new startup services

- Pi5: persistent enabled `executor-hub.service` and
  `executor-hub-device-{broker,desktop,relay}.service`. Replaces the transient
  pilot units. Existing Hub state and device state under
  `/home/jamie/.local/share/executor-hub-pilot` are retained.
- WSL: persistent enabled `executor-hub-device-{broker,desktop,relay}.service`.
  Uses the existing isolated state, not `/var/lib/executor`.
- CTPS: automatic LocalSystem services `ExecutorHubBroker` and
  `ExecutorHubRelay`; duplicate broker/dashboard logon tasks are disabled.
  `ExecutorHubPilot-desktop` remains an interactive-user logon task.
- Mac: `com.executor.hub.broker` and `com.executor.hub.relay` LaunchDaemons,
  plus `com.executor.hub.desktop` user LaunchAgent. Uses the existing Beta state
  and `bundle-main-923013c`, signed with the same developer identity. Native
  build metadata was verified to identify clean merge revision 923013c.
  Superseded Beta broker/dashboard/desktop labels were disabled and unloaded;
  the Beta Agent, original production roles and Fetch Proxy roles are now disabled
  and unloaded. Mac device configuration is relay-only; the prior config is kept
  as `state/config.before-hub-only.json`.

Backups are under each pilot root's `main-rollout-backup-923013c`; on Mac they
are under the Executor Beta installation. The one-shot Mac rollout job was
removed after success; the separate cutover-once job is disabled. Operational scripts remain outside tracked source in
the original checkout's ignored `.worktrees/hub-main-rollout` directory.

## Tunnel startup and old-service retirement

Pi5 `executor-hub-tunnel.service` is enabled and running, with Restart=on-failure.
Its approved runtime key is encrypted with the systemd host key at
`/etc/credstore.encrypted/executor-hub-api-key`, mode 0600, in a root-only directory.
LoadCredentialEncrypted supplies it without a plaintext persistent profile.
The old managed tmux runtime is stopped; never run both against the same Tunnel.
The new service's upstream readiness check passes.

Old Pi5 Agent/Broker/Dashboard and user Desktop units are stopped and masked.
WSL additionally masks the old Executor Cloudflared unit. Original system unit
files are retained as `retired-*.service` in each deployment backup. Pi5's
Cloudflared service is retained because it carries the Hub OAuth HTTPS ingress.
CTPS old ExecutorAgent/ExecutorBroker/ExecutorDashboard/ExecutorCloudflared
services are Stopped/Disabled, and its old ExecutorDesktop task is disabled.
Mac old production/Beta/Fetch Proxy launchd roles are disabled and unloaded.
No automatic old-device or direct-MCP fallback is configured.

ChatGPT's existing tunnel app was renamed to **Executor Hub** and refreshed
without changing its OAuth identity. Old ChatGPT connection records were not
deleted; their retired endpoints no longer provide the old Executor service.
Use only Executor Hub for current operations.

After cutover, ChatGPT completed one mkdir/write/read sequence on each of Pi5,
Mac, WSL and CTPS. All four returned the exact 20-byte content
`Executor Hub main OK`; independent host reads matched. No retries, admin
operations or configuration changes were part of that acceptance. Evidence:
https://chatgpt.com/c/6aa9aee8-094c-83e8-83ef-669181b56612

Background services start at boot; desktop control requires a logged-in,
unlocked user desktop. WSL services start when its distribution is started;
Windows task `ExecutorHubWSLStartup` is configured AtStartup with S4U (no stored
Windows password) and keeps Debian alive using a sleep process. Its manual
start is running successfully. No cold reboot of the four machines was performed;
startup configuration and independent service starts were verified.
