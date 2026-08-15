# Executor

Executor is a self-hosted MCP server that gives an authenticated AI full terminal, filesystem, administrator, and active-desktop control of a machine.

The project is under active construction. Do not deploy it on a production machine yet.

The packaging bundle currently installs persistent local services for `executor agent`, `executor broker`, `executor dashboard`, the active-user `executor desktop` helper, and an Executor-owned Cloudflare tunnel service. It never replaces a host's existing generic `cloudflared` service. Release artifacts also include the independent `executor-kill` emergency binary, and bootstrap installs both binaries into stable local paths before creating services.

Supported MCP transports are remote Streamable HTTP at `https://<domain>/mcp` and local stdio through `executor stdio`. Legacy SSE is not currently exposed.

## Intended workflow

```bash
git clone <repository-url>
cd Executor
```

Then ask your local coding agent to read `AGENTS.md` and deploy Executor on the current machine.

After setup, Executor prints the domain, Streamable HTTP endpoint, local stdio command, one-time recovery key, and loopback-only Dashboard URL. Record the one-time values in a secure local password manager. The recovery key is retained only as a verifier; service credentials needed at runtime remain in the host's permission-restricted secret store.

## Security model

Executor intentionally provides unrestricted device control. The security boundary is strong authentication, local process separation, auditability, credential rotation, and an independent Kill Switch—not command or directory allowlists.
