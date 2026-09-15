# Pi5-only Executor Hub

## Approved direction

Branch `codex/pi5-hub` explores a single Pi5 MCP gateway. Only Pi5 runs the
OpenAI tunnel client. ChatGPT connects to one Hub; enrolled devices retain
Executor helpers and their Dashboard relay connection. The Hub must never
fall back to a device's public MCP endpoint, another device, or local execution.
Existing installations are not removed or changed by work on this branch.

Logical route: ChatGPT -> OpenAI Tunnel -> Pi5 Hub -> Dashboard relay ->
explicit device -> native Executor helper. Responses follow the reverse route.
The existing Cloudflare-hosted Dashboard remains the enrollment/directory and
relay service; moving its hosting to Pi5 is not part of this scope.

## Contracts

- Fixed MCP tools plus `devices_list`; every device operation requires an exact
  `deviceId`. Device names are display labels, not routing authority.
- Device discovery means authenticated synchronization of enrolled devices,
  not network scanning or automatic authorization of unknown hosts.
- Directory availability, identity uniqueness, authorization and online state
  are required before routing. Relay execution must recheck current grants.
- Each terminal session and capture must remain bound to both device and caller.
- No automatic retry after an ambiguous write response; return unconfirmed.
- Preserve read/write annotations, pagination, binary bytes, standard MCP errors
  and device-side owner/admin authorization.
- Hub credentials must be independent of browser sessions. Existing Dashboard
  device grants bind `access_subject` and `browser_id`; those cannot be reused
  as machine credentials or silently converted into Hub grants.
- Owner-approved Hub enrollment and per-device delegation must support expiry,
  revocation and generation changes. Do not store device recovery keys in Hub.

## Implementation status

`internal/hub` provides the offline relay-only router and tool schemas, with
tests for explicit selection, missing/unknown/offline/unauthorized devices,
fresh directory lookup, no retry and truthful write annotations.
It is not wired into a daemon or exposed to ChatGPT yet.

Remaining implementation: machine-authenticated Dashboard API; device-side Hub
delegation validation; protocol adapter; device/caller-bound sessions and capture
handling; Pi5 daemon/CLI integration; enrollment UX; isolated multi-device and
real ChatGPT read/write acceptance. No live deployment or permission changes
have been performed for this branch.
