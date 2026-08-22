# Executor

Executor is a self-hosted MCP server that gives an authenticated AI full terminal, filesystem, administrator, and active-desktop control of a machine.

Executor is security-sensitive software under active development. Review the unrestricted-control security model and keep the local Kill Switch available on every installed host.

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

## Deployment prerequisites

- Go 1.24 or newer for source builds
- `cloudflared` 2025.4.0 or newer on the target machine
- Administrator or root privileges for service installation
- Python 3 on macOS, Linux, and WSL because the Unix bootstrap path uses it while validating Cloudflare metadata
- `systemd` on Linux and WSL for the packaged services
- A Cloudflare API token stored in a dedicated file; on macOS, Linux, and WSL that file must be a regular file with mode `0600`
- A hostname in a Cloudflare zone that you control

Desktop control also needs an active graphical login:

- macOS: grant Screen Recording and Accessibility to the installed `Executor Desktop.app`
- Windows: keep the target user logged in so the active-user desktop helper can run
- Linux X11: install a screenshot provider, `gdbus`, and `xdotool`
- Linux Wayland: install a screenshot provider, `gdbus`, and `wtype` or `ydotool`; advanced mouse actions remain compositor-dependent
- WSL: terminal and filesystem support work through WSL, while Windows desktop control requires the Windows companion and Linux GUI control requires WSLg

## Intended workflow

```bash
git clone <repository-url>
cd Executor
```

Then ask a local coding agent on that machine to read `AGENTS.md` and deploy Executor on the current machine.

Do not ask web ChatGPT to self-install Executor from a fresh clone. Before Executor exists, ChatGPT on the web has no MCP connection to the target host, cannot install privileged services, cannot satisfy the local secret-handling requirements, and cannot inspect or restart the resulting host services. Use a local coding agent, terminal session, or another already-installed host tool to perform the first deployment.

Executor supports two deployment entry paths:

1. Release archive deployment: download a packaged release for the target OS and run the included bootstrap script.
2. Clone deployment: use the source-deployment entrypoints `scripts/deploy-from-source.sh` on macOS, Linux, or WSL and `scripts/deploy-from-source.ps1` on Windows. Those wrappers are the canonical source-build entrypoints for a cloned repository. They are expected to build the local `executor` and `executor-kill` binaries, prepare the macOS helper app when required, and then invoke the existing bootstrap logic.

If a local checkout predates those source-deployment wrappers, the coding agent should treat that clone as not yet self-contained for source deployment and either use a release archive or perform the same build-plus-bootstrap flow explicitly before claiming deployment support.

## Clone deployment summary

Set the deployment inputs before invoking the source-deployment wrapper:

- `EXECUTOR_DOMAIN` as the public hostname
- `CLOUDFLARE_API_TOKEN_FILE` as the Cloudflare API token file path
- optional `CLOUDFLARE_ACCOUNT_ID` when the token can see multiple accounts
- optional `CLOUDFLARE_ZONE_ID` when more than one zone could match the hostname
- optional `CLOUDFLARE_TUNNEL_NAME` to override the default tunnel name `executor`

Representative commands:

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

Every setup attempt must be watched for recovery-key output. Capture a newly printed recovery key immediately, return it verbatim to the requesting owner in the same private chat if an AI performed the deployment, and store it in a password manager labeled by hostname. A repeated setup with existing credentials states that the existing key remains unchanged and cannot display it again; use the latest known valid key or rotate credentials explicitly.

On first setup, Executor prints the domain, Streamable HTTP endpoint, local stdio command, one-time recovery key, and loopback-only Dashboard URL. When a coding agent performs the deployment, it must copy the complete recovery key into its private response to the requesting owner instead of directing the owner to an unattended Terminal. The owner should save it immediately in a secure password manager. The key must not be written to repository files, configuration, persistent logs, issues, pull requests, or public channels. Executor retains only a verifier; service credentials needed at runtime remain in the host's permission-restricted secret store.

Every Kill or credential rotation creates a new one-time recovery key and immediately invalidates the previous key. Keep keys labeled by hostname when managing multiple Executor machines.

## Validation and recovery

After deployment:

1. Run `executor status` and confirm the host reports `armed`.
2. Run `executor doctor --full` and confirm every local check plus `remote OAuth DCR` passes.
3. Confirm the public OAuth metadata endpoints return JSON and unauthenticated `/mcp` returns HTTP 401 instead of a Cloudflare challenge.
4. Link the exact hostname in ChatGPT and use that host's newest recovery key.

If installation fails after service files were touched, run the platform rollback script from the same checkout or release bundle:

- macOS, Linux, WSL: `sudo ./scripts/rollback.sh`
- Windows: `powershell.exe -ExecutionPolicy Bypass -File .\scripts\rollback.ps1`

To remove the local installation and state entirely:

- macOS, Linux, WSL: `sudo ./scripts/uninstall.sh`
- Windows: `powershell.exe -ExecutionPolicy Bypass -File .\scripts\uninstall.ps1`

## Security model

Executor intentionally provides unrestricted device control. The security boundary is strong authentication, local process separation, auditability, credential rotation, and an independent Kill Switch—not command or directory allowlists.
