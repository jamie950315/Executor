# Executor

Executor is a self-hosted MCP server that gives an authenticated AI full terminal, filesystem, administrator, and active-desktop control of a machine.

The project is under active construction. Do not deploy it on a production machine yet.

The packaging bundle currently installs persistent local services for `executor agent`, `executor broker`, `executor dashboard`, the active-user `executor desktop` helper, and `cloudflared`. Release artifacts also include the independent `executor-kill` emergency binary, and bootstrap installs both binaries into stable local paths before creating services.

## Intended workflow

```bash
git clone <repository-url>
cd Executor
```

Then ask your local coding agent to read `AGENTS.md` and deploy Executor on the current machine.

## Security model

Executor intentionally provides unrestricted device control. The security boundary is strong authentication, local process separation, auditability, credential rotation, and an independent Kill Switch—not command or directory allowlists.
