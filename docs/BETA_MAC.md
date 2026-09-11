# Temporary Mac Beta MCP

Deployed from `pro-fix` source `7c2d522` on 2026-09-11. This deployment is for direct ChatGPT MCP testing. The production installation remains in place.

## Connection

- MCP URL: `https://beta-executor-mac.0ruka.dev/mcp`
- Authentication: OAuth with DCR, PKCE S256, and the separate Beta recovery key.
- Production URL: `https://executor-mac.0ruka.dev/mcp`.
- The newly generated recovery key was delivered privately to the owner. It is excluded from this document and repository.

## Isolation and installed files

The installation root is `/Users/jamie/Library/Application Support/Executor Beta`.

- `bundle/`: native source-built executor and signed `Executor Desktop.app`.
- `state/`: independent configuration, protected secret store, OAuth state, IPC endpoints, and metadata-only audit.
- `prepare.py`: one-time scoped preparation; refuses existing state.
- `verify.py`: public OAuth/MCP acceptance checks with hidden recovery-key input and in-memory tokens.
- `stop.py`: validated Beta-only stop helper.

The Beta agent binds to `127.0.0.1:18787`. Port 18788 is reserved in its config; no Beta dashboard or centralized enrollment was started. Agent and broker are separate LaunchDaemons named `com.executor.beta.agent` and `com.executor.beta.broker`; the desktop helper is the owner's LaunchAgent `com.executor.beta.desktop`. The agent runs as `jamie`, and the broker runs as root.

The existing Mac Cloudflare Tunnel `2cd75fa6-9062-490a-a794-bbc3008085a0` carries one additional ingress rule for Beta. The original configuration was re-read and verified unchanged after excluding the new rule. Tunnel configuration became version 2; Beta's proxied CNAME record is `530784cb3c3fac81017d55ae0bf8e157` in zone `7afcba621ca7186896e24f2cd4ce4acd`. Production continues to route to port 8787. Tunnel credentials, existing DNS records, and zone security settings were preserved.

## Verified behavior

Verification used the public HTTPS endpoint, not only loopback:

- Unauthenticated MCP requests return HTTP 401.
- DCR registration, consent page, recovery-key authorization, PKCE code exchange, and refresh-token exchange pass.
- Production rejects a Beta-issued access token with HTTP 401.
- All nine MCP tools are advertised with the new pagination and side-effect contracts.
- Owner and admin file writes, exact byte reads, rejected invalid bounds, and fixture cleanup pass.
- Actual owner UID 501 and admin UID 0 are confirmed through command-mode terminals.
- Terminal output stays within four-byte pages and reports actual exit code 7.
- Desktop permission prerequisites are ready; a public screenshot returns 1512 by 982 display points. No screenshot was saved.

The verification client uses its own `Executor-Beta-Acceptance/1.0` User-Agent. The default Python urllib identifier received Cloudflare error 1010 on both Beta and production; explicit client identification worked with unchanged edge protection. Actual ChatGPT connection and model-level tool rejection behavior remain for the owner's comparison test. Pointer and keyboard input were not exercised during this deployment.

## Stop and lifecycle boundary

The standard Executor lifecycle CLI uses production service names. Use the dedicated Beta helper for this temporary deployment; do not invoke the bundled `executor kill`, `executor rotate`, `executor resume`, or `executor-kill` as Beta management commands.

Validate the stop targets:

```bash
python3 '/Users/jamie/Library/Application Support/Executor Beta/stop.py' --check
```

Stop only Beta:

```bash
sudo python3 '/Users/jamie/Library/Application Support/Executor Beta/stop.py'
```

This creates Beta's disabled marker, unloads its three services, and preserves its state, recovery key, DNS record, and the shared production Tunnel. Resume or full removal should be explicitly requested and scoped to these exact Beta resources. No automatic expiry or background cleanup is configured.
