# Executor

Executor is a self-hosted MCP server that gives an authenticated AI full terminal, filesystem, administrator, and active-desktop control of a machine.

The project is under active construction. Do not deploy it on a production machine yet.

The packaging bundle currently installs persistent local services for `executor agent`, `executor broker`, `executor dashboard`, the active-user `executor desktop` helper, and an Executor-owned Cloudflare tunnel service. It never replaces a host's existing generic `cloudflared` service. Release artifacts also include the independent `executor-kill` emergency binary, and bootstrap installs both binaries into stable local paths before creating services.

Supported MCP transports are remote Streamable HTTP at `https://<domain>/mcp` and local stdio through `executor stdio`. Legacy SSE is not currently exposed.

## Computer Use

Executor exposes an MCP-native observe → act → observe loop for ChatGPT and other MCP clients:

1. Call `desktop_observe` with `action: "screenshot"` and no path. Executor returns a short text summary containing the complete `captureId`, the screen as an MCP image content block, and image dimensions, MIME type, and capture time in structured metadata. Both screenshot tools declare an MCP output schema for this metadata. PNG is used normally; an oversized high-detail capture is re-encoded as JPEG without changing its coordinate dimensions.
2. Call `desktop_control` with `action: "batch"`, that `captureId`, and ordered actions. Supported Computer Use actions are `click`, `double_click`, `move`, `drag`, `scroll`, `type`, `keypress`, `wait`, and `screenshot`.
3. Executor executes the validated batch and automatically returns a fresh screenshot and `captureId`. A capture ID is single-use and only the newest capture for the MCP session is accepted. Any desktop control from any session invalidates every older capture.

Screenshot bytes and typed text are sensitive. They are returned only to the authenticated caller and are not written to Executor's audit log. The audit store records tool metadata and outcomes only.

Successful tool calls return both structured MCP data and a serialized text content block for client compatibility. Multimodal Computer Use results keep their explicit text and image blocks.

Desktop control requires an active, unlocked graphical login. macOS and Windows currently capture the primary display so screenshot coordinates match input coordinates; macOS Retina captures are normalized to display points. Linux X11 supports the complete action set. Wayland support depends on the compositor and installed tools; unreliable advanced mouse actions return an explicit unavailable error. WSL terminal and filesystem control work independently, while GUI control requires WSLg or the Windows companion.

On macOS, grant Screen Recording and Accessibility to the installed `Executor Desktop.app`, not to Terminal or the standalone `executor` binary. Release bundles install this signed helper at `/Library/Application Support/Executor/Executor Desktop.app` so macOS can retain permissions across service restarts. See [Deployment](docs/DEPLOYMENT.md) for release-signing requirements.

Remote authentication uses OAuth 2.1 with PKCE. Executor uses Dynamic Client Registration (DCR) for current ChatGPT compatibility and retains implemented support for Client ID Metadata Documents (CIMD), public-client `none`, and ChatGPT-signed `private_key_jwt` token exchange.

Before linking ChatGPT, run `executor doctor --full`. The full check sends an invalid, non-registering request through the public hostname to confirm that Cloudflare allows ChatGPT's DCR request to reach Executor. Cloudflare Bot Fight Mode can challenge API traffic and cannot be bypassed with a WAF custom rule; disable Bot Fight Mode for the zone or use Super Bot Fight Mode with an OAuth-path skip rule. See [Troubleshooting](docs/TROUBLESHOOTING.md) for the verified failure signatures and recovery steps.

## Intended workflow

```bash
git clone <repository-url>
cd Executor
```

Then ask your local coding agent to read `AGENTS.md` and deploy Executor on the current machine.

After setup, Executor prints the domain, Streamable HTTP endpoint, local stdio command, one-time recovery key, and loopback-only Dashboard URL. When a coding agent performs the deployment, it must copy the complete recovery key into its private response to the requesting owner instead of directing the owner to an unattended Terminal. The owner should save it immediately in a secure password manager. The key must not be written to repository files, configuration, persistent logs, issues, pull requests, or public channels. Executor retains only a verifier; service credentials needed at runtime remain in the host's permission-restricted secret store.

Every Kill or credential rotation creates a new one-time recovery key and immediately invalidates the previous key. Keep keys labeled by hostname when managing multiple Executor machines.

## Security model

Executor intentionally provides unrestricted device control. The security boundary is strong authentication, local process separation, auditability, credential rotation, and an independent Kill Switch—not command or directory allowlists.
