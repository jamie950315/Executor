# OpenAI Secure MCP Tunnel

This optional transport uses the official `tunnel-client` with Executor's
existing authenticated HTTPS or loopback HTTP endpoint. It does not bypass ChatGPT
permissions, relabel writes as reads, or replace Executor OAuth.

Official reference: https://developers.openai.com/api/docs/guides/secure-mcp-tunnels

## Local helper

`python3 scripts/openai_tunnel.py --help` lists check, inspect, doctor,
connect and status actions. Credential input must be an owner-owned regular
file with mode 0600 containing exactly one tunnel ID and runtime API key.
The helper passes the key through the child environment, not command arguments
or the generated profile. Do not enable raw HTTP or payload logging.

Connect requires an explicit authenticated HTTPS or loopback `/mcp` URL. Existing aliases
or tunnel registrations are refused to prevent rebinding another runtime.
The official client supervises the process; this is not an installed boot service.
An environment-key reference alone does not provide credentials after reboot.

## Verification on 2026-09-15

Follow-up: the installed runtime now targets the existing canonical HTTPS MCP
endpoint `https://executor-mac.0ruka.dev/mcp`. Loopback transport with metadata
advertising this different origin prevented OAuth auto-registration. Adding a
host regex did not fix that origin boundary and was reverted. Using the
canonical endpoint registered seven OAuth targets with the unmodified official
client; health, readiness and process-running checks pass. No private-host
guard was removed. ChatGPT app creation and DCR now pass. OAuth authorization
fails with `resource does not match this Executor`: ChatGPT requests the
Tunnel resource rather than Executor's canonical MCP resource. The existing
app settings expose no OAuth-resource edit control. No resource validation
was weakened; authenticated ChatGPT tool calls and write acceptance remain pending.

## Branch-only OAuth compatibility

The optional `oauth_resource_aliases` configuration array identifies exact
alternate transport URLs for the same logical Executor resource. It is empty
by default, limited to eight unique HTTPS URLs, and rejects credentials,
queries, fragments and wildcards. Configure only a verified operator-owned
transport address; never copy aliases automatically from incoming requests.
Authorization and token exchange enforce exact matching, including rejection
of duplicate resource parameters. Owner consent, PKCE, client authentication
and the canonical token audience are unchanged. Aliases are equivalent names
for one resource, not independent tenants or permission scopes.

This source change has not been installed into any running Executor service.
The isolated native loopback HTTP test covers DCR, wrong-owner rejection,
authorization, PKCE token exchange, canonical token verification and state
reload. Full Go tests, focused race tests, vet, native build and six
CGO-disabled platform builds pass. ChatGPT acceptance requires a separately
deployed test endpoint and is not implied by these local checks.

- Branch `tunnel`; official client 0.0.14 installed.
- 18 helper tests pass, including credential handling and alias collision checks.
- Existing Mac endpoint `http://127.0.0.1:8787/mcp` rejects unauthenticated
  access with 401 and retains its public OAuth issuer.
- Doctor passes endpoint reachability and OAuth discovery.
- Alias `executor-openai-tunnel` uses the canonical HTTPS endpoint.
- ChatGPT Plugins contains Executor Tunnel; app creation and DCR succeeded,
  but owner authorization is blocked by the installed server's resource check.
- Follow-up status reports process-running, healthy and ready all true.
  Startup initially reported not ready. An
  unauthenticated initialization probe returned Unauthorized. This is not proof
  of successful authenticated MCP execution.

No existing Executor service, binary, OAuth credential or Cloudflare route was
replaced. End-to-end ChatGPT read/create/modify/read-back acceptance remains
pending. Limit acceptance writes to disposable test data and retain normal
write approval semantics. Do not claim full access from transport health alone.
