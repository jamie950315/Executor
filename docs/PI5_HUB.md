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

The Hub-side Dashboard HTTP adapter now targets only the fixed machine API
paths `/api/hub/devices` and `/api/hub/devices/{id}/call`. It removes cookie-jar
authentication, rejects redirects, bounds request/response bodies and uses an
explicit machine bearer credential. Its real-loopback HTTP tests verify target
selection and credential/redirect boundaries. The corresponding Worker API is
not implemented yet; the adapter alone grants no device authority.

Device-signed Hub delegations use the distinct `executor-hub-grant+jwt` type,
binding device ID, Hub ID/key ID, device generation, delegation version and
expiry. Tests reject wrong Hub/device/key, stale generations, expired grants
and browser/Hub token substitution. Execution still needs verified Hub request
proof plus persisted delegation state; signing primitives are not enrollment
or a complete authorization path.

Terminal routing now replaces remote IDs with opaque Hub session IDs and binds
them to the originating MCP caller session, target device and owner/admin
privilege. Cross-scope use is rejected before relay dispatch. Session listings
only show that caller's bindings; inspect responses must match the requested
remote ID. Confirmed close removes a binding, while an unconfirmed close retains
it for explicit recovery. Bindings are in memory with a 2,048-entry cap; Hub
restart recovery/persistence and device-side enforcement remain pending.

Remaining implementation: machine-authenticated Dashboard API; device-side Hub
proof/replay checks and delegated execution; protocol adapter wiring; device/caller-bound sessions and capture
handling; Pi5 daemon/CLI integration; enrollment UX; isolated multi-device and
real ChatGPT read/write acceptance. No live deployment or permission changes
have been performed for this branch.
