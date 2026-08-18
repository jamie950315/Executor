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
- Never persist, log, commit, or publish secrets, tokens, recovery keys, command output, or Cloudflare credentials. The one explicit exception is delivering a newly generated recovery key directly to the requesting owner in the same private AI chat that performed the deployment or credential rotation.
- When an AI agent runs setup, Kill, rotate, or another operation that generates a recovery key on the user's behalf, it MUST reproduce the complete recovery key verbatim in its final private-chat response. It MUST NOT redact, mask, omit, or tell the user to retrieve the key from a Terminal they did not operate. Clearly label the value as sensitive and shown once, and tell the user to save it immediately. This exception applies only to the recovery key and never permits writing it to repository files, configuration, persistent logs, Issues, pull requests, or public channels.
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
- macOS, Linux, and Windows amd64/arm64 builds are verified by cross-compilation. Real target runtime validation now covers macOS LaunchDaemons/LaunchAgent, Windows Services/Scheduled Task with ConPTY through the privileged Broker, Raspberry Pi OS systemd, and WSL systemd; Linux X11/Wayland variants beyond the tested Pi and WSL environments remain platform-dependent.
- Live isolated Cloudflare named-Tunnel deployments are validated at `executor-pi5.0ruka.dev`, `executor-mac.0ruka.dev`, `executor-windows.0ruka.dev`, and `executor-wsl.0ruka.dev`. Each publishes valid OAuth metadata and rejects unauthenticated MCP requests. The temporary deployment API token was revoked and API-token files were removed after validation.
- A real macOS arm64 process-level test verified OAuth DCR + PKCE, MCP initialization, owner filesystem and terminal calls, window observation, a 3024x1964 screenshot, immediate Kill quiescing, secret rotation, old-token rejection after Resume, and immediate Dashboard key rollover. The installed LaunchDaemon/LaunchAgent deployment additionally verifies Agent, privileged Broker, Dashboard, active-user Desktop, and Executor-owned cloudflared runtime health.
- Lifecycle CLI and installers share stable default state locations (`/var/lib/executor` on Unix and `%ProgramData%\Executor` on Windows), while preserving `EXECUTOR_STATE_DIR` overrides.
- Metadata-only audit events are now written for remote and stdio tool attempts/outcomes without command text, file content, or output.
- OAuth DCR accepts standard metadata but restricts callback hosts to ChatGPT or loopback, and the consent page displays the requesting client and redirect destination. MCP stdio uses newline-delimited JSON; Streamable HTTP validates Origin and protocol headers.
- OAuth authorization-server metadata currently directs ChatGPT through DCR because repeated real ChatGPT web CIMD callbacks stopped before token exchange. The CIMD resolver remains implemented but is not advertised until that callback path is interoperable.
- ChatGPT CIMD token exchange supports both public-client `none` and signed `private_key_jwt` with trusted-origin JWKS verification. The authorization form uses a mobile-Safari-compatible native submit control and preserves the MCP `resource` parameter.
- OAuth token attempts and outcomes are recorded as metadata-only audit events with the authentication method, grant type, result code, and credential-free reason. Client IDs, authorization codes, assertions, tokens, and recovery keys are never recorded.
- AI-managed deployment and credential-rotation instructions require the agent to return each newly generated recovery key verbatim in the requesting owner's private chat while keeping it out of files, logs, Git, and public collaboration surfaces.
- OAuth state replacement preserves the existing Unix owner and group, so a root-initiated Kill or rotate cannot leave the normal-user Agent unable to read `oauth-state.json`; a real root regression test covers the ownership boundary.
- Resume verifies that Broker, Desktop, and Agent loaded the rotated credentials before starting the tunnel or removing the disabled marker.
- Linux/WSL units install under the standard systemd system/user directories. Darwin release jobs build on macOS with native CoreGraphics desktop input.
- Linux terminal shutdown freezes and terminates the complete `/proc` descendant tree, including background jobs that util-linux `script` places in separate process groups; regression tests run on real Pi5 Linux in addition to macOS.
- Windows terminal sessions use the native ConPTY API with persistent input/output, case-insensitive environment overrides, cwd preservation, live resize, and bounded process-tree teardown through a kill-on-close Job Object. Real Windows amd64 tests verify PowerShell state, LF command input, resize, inherited Ctrl+C recovery, no-parent-console startup, child-process termination, Windows Service execution, and the active-user Desktop Scheduled Task.
- Setup preserves the preferred loopback ports when available and automatically persists free loopback ports when another process owns them. Agent and Dashboard status/doctor probes require the authenticated Executor health marker instead of treating any listener as healthy.
- Installed services are active and healthy on the Pi5, this Mac, Windows `ctps`, and its Debian WSL instance. macOS service rendering uses the native `wheel` administrator group, and Unix credential rotation preserves the installed secret store owner and mode.

## Runtime targets

- macOS: LaunchDaemon for agent/broker, LaunchAgent for desktop helper.
- Windows: Windows Services for agent/broker, active-user startup for desktop helper.
- Linux: systemd services plus active-user desktop helper.
- WSL: Linux terminal/filesystem support; WSLg GUI only. Windows desktop requires the Windows companion.
