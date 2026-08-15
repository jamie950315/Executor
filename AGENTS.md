# Executor Agent Guide

## Mission

Executor is a self-hosted, cross-platform MCP server that lets an authenticated AI control the host computer with unrestricted owner and administrator capabilities. It supports macOS, Windows, Linux, and WSL.

## Approved product requirements

- Public repository designed for a coding agent to deploy after clone.
- Official product and binary name: `Executor` / `executor`.
- Remote access uses a user-owned Cloudflare account, zone, named Tunnel, and hostname.
- MCP transports: Streamable HTTP and local stdio; legacy SSE compatibility may be enabled.
- Authentication: OAuth 2.1 by default, optional explicitly unsafe URL-secret compatibility mode.
- No directory allowlist or command allowlist. Owner and administrator terminals may access the whole host.
- Persistent terminal sessions retain cwd, environment, background processes, PTY input, and output.
- Active logged-in desktop control is in v1: screenshots, accessibility tree, mouse, keyboard, windows, and applications.
- GUI control stops when the desktop is locked or logged out; terminal/admin control remains available.
- Components: unprivileged `executor-agent`, privileged `executor-broker`, active-user `executor-desktop`, CLI `executor`, and independent `executor-kill`.
- Local dashboard binds only to `127.0.0.1`.
- Kill Switch stops the tunnel and all sessions, revokes OAuth tokens, rotates URL/IPC/recovery credentials, and does not depend on the main agent being healthy.

## Engineering rules

- Use test-driven development: add and run a failing test before production behavior.
- Keep platform-specific code behind narrow interfaces and build tags.
- Never log or commit secrets, tokens, recovery keys, command output, or Cloudflare credentials.
- Secret input must use an interactive no-echo prompt or OS secret store.
- Preserve unrelated host configuration and make setup idempotent with rollback.
- README files are written in English.
- Before reporting completion, run tests, builds, cross-builds, and real local runtime checks.

## Current status

- Architecture approved in conversation on 2026-08-15.
- Repository initialized on branch `main`.
- Config, private fallback secret store, metadata-only audit store, OAuth 2.1 with CIMD/DCR, authenticated localhost dashboard, Streamable HTTP MCP, and local stdio MCP are implemented with tests.
- Agent, privileged Broker, active-user Desktop helper, persistent terminal/filesystem/desktop tools, lifecycle control, independent Kill Switch, and Cloudflare named-Tunnel setup are implemented.
- Release archives install stable `executor` and `executor-kill` binaries plus persistent Agent, Broker, Dashboard, Desktop, and Executor-owned cloudflared services with manifest-backed rollback. Existing generic cloudflared services are not replaced or stopped except for an ownership-proven one-time migration from an older Executor install; replaced host services are restored and returned to their prior running state.
- macOS, Linux, and Windows amd64/arm64 builds are verified by cross-compilation. Windows service/Scheduled Task and Linux systemd/X11/Wayland behavior still require real target-machine runtime verification.
- Live Cloudflare deployment is not verified because no deployment API token file or hostname has been supplied.
- A real macOS arm64 process-level test has verified separate Agent/Broker/Desktop/Dashboard processes, OAuth DCR + PKCE, MCP initialization, owner filesystem and terminal calls, window observation, a 3024x1964 screenshot, immediate Kill quiescing, secret rotation, old-token rejection after Resume, and immediate Dashboard key rollover. This did not install system services or prove root execution.
- Lifecycle CLI and installers share stable default state locations (`/var/lib/executor` on Unix and `%ProgramData%\Executor` on Windows), while preserving `EXECUTOR_STATE_DIR` overrides.
- Metadata-only audit events are now written for remote and stdio tool attempts/outcomes without command text, file content, or output.
- OAuth DCR accepts standard metadata but restricts callback hosts to ChatGPT or loopback, and the consent page displays the requesting client and redirect destination. MCP stdio uses newline-delimited JSON; Streamable HTTP validates Origin and protocol headers.
- Resume verifies that Broker, Desktop, and Agent loaded the rotated credentials before starting the tunnel or removing the disabled marker.
- Linux/WSL units install under the standard systemd system/user directories. Darwin release jobs build on macOS with native CoreGraphics desktop input.
- Windows terminal sessions use the native ConPTY API with persistent input/output, case-insensitive environment overrides, cwd preservation, live resize, and bounded process-tree teardown through a kill-on-close Job Object. A real Windows amd64 test launched through WSL interoperability verifies PowerShell state, LF command input, resize, inherited Ctrl+C recovery, no-parent-console startup, and child-process termination; full Windows service and active-desktop installation still require target-machine verification.
- Setup preserves the preferred loopback ports when available and automatically persists free loopback ports when another process owns them. Agent and Dashboard status/doctor probes require the authenticated Executor health marker instead of treating any listener as healthy.
- No system service has been installed from this checkout yet.

## Runtime targets

- macOS: LaunchDaemon for agent/broker, LaunchAgent for desktop helper.
- Windows: Windows Services for agent/broker, active-user startup for desktop helper.
- Linux: systemd services plus active-user desktop helper.
- WSL: Linux terminal/filesystem support; WSLg GUI only. Windows desktop requires the Windows companion.
