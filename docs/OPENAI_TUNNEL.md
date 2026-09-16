# OpenAI Secure MCP Tunnel

## Current accepted route — 2026-09-15

The **Executor Tunnel Beta** ChatGPT app now connects through
`executor-openai-tunnel` to `https://beta-executor-mac.0ruka.dev/mcp`.
Beta Agent source `61aeb58` is installed in `bundle-tunnel-csp`, executable
SHA-256 `b29e5e7da9f55a4e5981e4f30ca8c54dcad93ded186b9787b59f540615447cb4`.
Only the Agent was restarted. Broker/Desktop/Dashboard retain their old bundle;
Beta credentials, production hashes/PIDs and Cloudflare routes are preserved.

OAuth and nine-tool discovery now pass in ChatGPT. Two fixes are required:
exact operator-configured transport resource aliases, and consent-page CSP
allowing the already-validated callback origin after the POST redirect.
Neither OAuth nor write annotations were disabled. The app retains its default
approval policy. The older **Executor Tunnel** app remains unlinked; use the
Beta app instead.

The ChatGPT AI itself completed creation, read-back, overwrite, modification,
append and complete final read-back in this conversation:
https://chatgpt.com/c/6aa911c9-b578-83e8-a86a-194d451bae89

Independent local verification confirms the expected final three-line UTF-8
file is 71 bytes, SHA-256
`8bdccabe98d2a3cca21fab72a07ebc4ec46dbdcc671aa917bfa43d01a8a8e423`.
Metadata audit independently records the successful reads/writes. Cross-page
and binary acceptance also pass in the same conversation, using only
disposable data under `/tmp/executor-tunnel-chatgpt.if56Fl`.

The AI read a 70,000-byte file in 65,536 + 4,464-byte pages, appended a 20-byte
UTF-8 tail, and read the exact tail back. Independent local verification confirms
70,020 bytes and SHA-256 `9e647184cd080d8c3636c904115db628a97657f6c3527ab9e6ed7369fe7228d8`.
Binary bytes `00 01 02 03 fe ff` also match exactly after MCP base64 round-trip.
The AI reported two extra calls on an already-closed session; those correctly
returned not-found. The corrected lifecycle follow-up completed with zero errors:
create, read exact `tunnel-lifecycle-ok` output, inspect exit code 0, then close
once. The AI's full final reply was received before concluding acceptance.

Full Go tests, focused race tests, vet, native build, six cross-builds and
50 Python tests pass. Final post-acceptance inventory confirms production
files and PIDs remain unchanged, the deployed binary matches its recorded
hash, and all four Beta services pass the dedicated stop helper's read-only check.
The runtime remains locally supervised rather than a boot service; reboot
durability is not verified. It still depends on the existing public Beta HTTPS
route, rather than a fully private OAuth-server deployment.

Beta's installed `stop.py --check` validates the mixed bundle paths.
Administrator-only `scripts/beta_csp_upgrade.py --rollback` restores the
alias-only Agent; then `scripts/beta_tunnel_upgrade.py --rollback` restores
the original Agent/configuration. They refuse independently changed files.
Backups are retained under Beta's `deployment-backups/tunnel-agent-*`.
A live rollback was not exercised. Do not use production lifecycle commands.

## Historical pre-deployment notes (superseded by current status above)

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

Deployment preflight on 2026-09-15 confirms the existing Beta Agent is a
system LaunchDaemon. Noninteractive sudo requires a password, so no Beta
service or configuration was changed. The original Beta recovery credential
remains required for its owner-consent flow; no credential reset was attempted.
The alias HTTP regression also rejects an unapproved resource at token exchange
without consuming the authorization code; ten race-enabled repeats pass.

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
