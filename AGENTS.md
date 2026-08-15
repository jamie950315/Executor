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
- Release archives install stable `executor` and `executor-kill` binaries plus persistent Agent, Broker, Dashboard, Desktop, and cloudflared services with manifest-backed rollback.
- macOS, Linux, and Windows amd64/arm64 builds are verified by cross-compilation. Windows service/Scheduled Task and Linux systemd/X11/Wayland behavior still require real target-machine runtime verification.
- Live Cloudflare deployment is not verified because no deployment API token file or hostname has been supplied.
- No system service has been installed from this checkout yet; macOS process-level end-to-end verification is the next local runtime gate.

## Runtime targets

- macOS: LaunchDaemon for agent/broker, LaunchAgent for desktop helper.
- Windows: Windows Services for agent/broker, active-user startup for desktop helper.
- Linux: systemd services plus active-user desktop helper.
- WSL: Linux terminal/filesystem support; WSLg GUI only. Windows desktop requires the Windows companion.
