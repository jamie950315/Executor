# Pi5-only Executor Hub

## Pilot deployment status

Pi5 native aarch64 tests passed for isolated two-helper signed HTTP/IPC file
read/write, grant/request verification, cross-process replay persistence and
owner delegation/revocation. The source `b3a93e2` Linux binary hash is
`bc94fb69f69babc8f0cfef7fbac4cce82d6f1e5a3827cd07ebef840cb02f395e`,
staged at `/home/jamie/.local/share/executor-hub-pilot/bin/b3a93e2/executor`.
No Pi5 Hub services or real pilot credentials have been created yet; existing
Executor services remain active.

The isolated Beta Dashboard has migration 0003 and Worker version
`9e4d801f-7600-45ba-b26d-18e3de79f2f8`. Its authenticated public UI displays
the existing Mac Beta and empty Hub registry. Access/enrollment rejection
checks pass. Native Mac Beta relay upgrade, machine-API edge routing, Hub
initialization and physical ChatGPT multi-device acceptance remain pending.
This is not completion of the Pi5 Hub goal. See `BETA_DASHBOARD.md` for rollback.

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
It is wired into the Agent as an opt-in Hub mode, but not deployed or exposed
to ChatGPT yet.

`SignedRelay` now verifies each device-signed grant against the approved device
key, Hub identity and grant generation/version before signing every call with
a fresh nonce. Directory authorization is also intersected with current local
grant validity. `DashboardRelay` accepts signed envelopes only; its earlier
unsigned call method was removed, not retained as a fallback.

An integration test runs two isolated native Desktop helpers, each with separate
state, IPC keys and device approval. Hub -> signed HTTP request -> fixture
gateway -> real device adapter/proof/replay checks -> authenticated IPC -> native
filesystem read/write passes, with independent byte verification. This verifies
the client/device path but not the Cloudflare Worker, physical multi-host routing
or ChatGPT. All fixture files and helper lifetimes are test-owned.

The Hub-side Dashboard HTTP adapter now targets only the fixed machine API
paths `/api/hub/devices` and `/api/hub/devices/{id}/call`. It removes cookie-jar
authentication, rejects redirects, bounds request/response bodies and uses an
explicit machine bearer credential. Its real-loopback HTTP tests verify target
selection and credential/redirect boundaries. The corresponding Worker API is
implemented in the Worker source as described below; the adapter alone grants
no device authority.

Device-signed Hub delegations use the distinct `executor-hub-grant+jwt` type,
binding device ID, Hub ID/key ID, device generation, delegation version and
expiry. Tests reject wrong Hub/device/key, stale generations, expired grants
and browser/Hub token substitution. Execution still needs verified Hub request
proof plus persisted delegation state; signing primitives are not enrollment
or a complete authorization path.

Hub request proofs now bind the device, Hub, caller/session, request ID, method,
exact input-byte hash and delegation-token hash with a domain-separated ES256
signature. Proofs expire within 60 seconds and permit at most five seconds of
future clock skew. Verification derives the Hub key ID from RFC 7638 canonical
JWK members and rejects tampering before consuming a nonce. The replay-consumer
contract requires atomic durable consumption before execution and fails closed
when absent or unavailable. Cryptographic tests use an isolated replay fixture;
the device adapter integration below uses the file-backed store.

`FileReplayStore` now provides a dedicated protected local nonce directory with
cross-process locking, exclusive record creation, write-through/file sync,
bounded capacity and expiry pruning. It stores only hashed nonce IDs and expiry
times, never request contents or credentials. Reopen and six-process contention
tests pass on macOS; corrupted state and symlink entries fail closed. Windows
uses file locking/write-through and inherits its protected state-parent ACL;
native Windows and power-loss behavior remain unverified. The source device
adapter now consumes this store for `hub.call`; no installed device has been
upgraded or granted Hub authority yet.

The macOS initialization failure was later captured as lock-open ENOENT while
the nonce directory still existed. Lock initialization now uses an exclusive
creator followed by a separate, identity-checked existing-file opener, rather
than concurrent ordinary O_CREATE. Thirty multiprocess repeats and three related
race-suite runs pass after this change. Stage/syscall diagnostics remain; the
underlying filesystem behavior is not asserted beyond that observed boundary.
The failure occurred before execution, not as acceptance of a replayed request.

The device adapter reads owner-provisioned `hub-delegations.json` public-key
approvals from its protected state directory. It checks the signed inner/outer
request ID, approved Hub key, current device generation/delegation revision,
request proof, expiry and persisted nonce before dispatch. It then rechecks
mutable approval/generation/disabled state. Missing approval files deny Hub
requests by default; browser grants cannot reach this execution path. Tests
verify approved execution, revocation, disabled-device rejection and rejection
of replay after rebuilding the adapter. Owner-facing approval issuance is still
implemented at the device relay layer as `hub.delegate` and `hub.revoke`.
These owner actions use the existing unlocked browser grant, revalidate it while
holding the delegation-state lock, and increment a persistent delegation
revision. Delegation signs a Hub-key-bound certificate; revocation disables the
local approval immediately. Device approval state contains public keys/revisions
only, not recovery keys or grants. Atomic writes preserve Unix ownership and use
Windows write-through replacement. The Worker now prepares these actions from
the registered Hub public key, not a browser-supplied replacement key, and
validates the device certificate before updating the registry. Revocation leaves
a versioned tombstone so a late older response cannot re-enable routing. A
registry failure after a device action returns an unconfirmed outcome without
automatic replay. The Dashboard management UI is implemented as described below.

Terminal routing now replaces remote IDs with opaque Hub session IDs and binds
them to the originating MCP caller session, target device and owner/admin
privilege. Cross-scope use is rejected before relay dispatch. Session listings
only show that caller's bindings; inspect responses must match the requested
remote ID. Confirmed close removes a binding, while an unconfirmed close retains
it for explicit recovery. Bindings are in memory with a 2,048-entry cap; Hub
restart recovery/persistence and device-side enforcement remain pending.

The Worker machine API now has independent `hubs` and `hub_devices` registry
tables (migration 0003). It stores machine-token hashes and device-signed,
Hub-key-bound delegation certificates, not recovery keys or machine bearer
tokens. Each directory/call authenticates the Hub
anew. Calls require an active, current-generation device link and a valid
device-signed Hub grant matching its delegation revision. Only signature-bearing
proof envelopes bound to the authenticated Hub and requested device are relayed;
the device remains responsible for verifying the request proof and replay state.
No browser cookies or public device URLs are used. Existing browser APIs remain
unchanged. Ordinary calls retain 10-second relay deadlines; interactive Hub
desktop/permission calls inherit their existing 120-second class.

Worker tests exercise machine revocation, wrong targets, signed-grant checking,
precise routing and stale delegation rejection using test D1 and a relay stub.
Full Dashboard tests/checks and dry-run build pass. Migration 0003 is now applied
only to the Beta database. Public machine access still requires an explicitly scoped
machine-API Access route and an owner-approved registration/provisioning flow.

Real relay responses are NDJSON envelopes, not plain JSON. The Worker and browser
now share the same bounded reader for response/chunk validation, sequence checks,
request correlation and complete EOF. The machine API unwraps a fully verified
result to JSON; owner actions re-emit a correlated NDJSON result for the existing
Dashboard client. Tests now use actual response and stream-chunk envelopes and
reject mismatched IDs or truncated streams. Earlier plain-JSON relay stubs did
not cover this integration boundary and are no longer used for those tests.

Owner registry endpoints `/api/hubs` and `/api/hubs/{id}/disable` require the
verified Cloudflare Access identity; mutations additionally require same-origin
JSON. Registration accepts only a public P-256 JWK and machine token hash.
Existing IDs cannot be rebound to another key/hash, and replaying registration
does not re-enable a disabled Hub. No machine private key or bearer token is sent
to these owner endpoints. Worker tests cover these boundaries and late-response
ordering after revocation.

## Agent runtime configuration

The CLI now provides dedicated, non-deploying initialization:

```sh
executor hub init --state-dir /absolute/dedicated/hub-state \
  --hub-id pi5-hub --domain hub.example.com \
  --dashboard-url https://dashboard.example.com --listen 127.0.0.1:28787
executor hub registration --state-dir /absolute/dedicated/hub-state
executor hub run --state-dir /absolute/dedicated/hub-state
```

Add `--resource-alias <exact-verified-Tunnel-resource>` when required for OAuth.
Initialization creates its own protected signing/OAuth state and random machine
token, and prints the one-time Hub recovery key for the owner. An AI running
initialization must deliver that new key in the same private chat, including on
a later partial-init error. The separate registration command outputs only the
public key, Hub ID and machine-token hash for the Dashboard form.
Repeated identical initialization preserves credentials; existing ordinary
device state or mismatched configuration is refused. No service, Cloudflare
route, Dashboard record or device permission is changed by initialization.
`hub run` requires valid Hub state and does not fall back to a device runtime.

New private devices can use `executor relay-device init --state-dir <dedicated>
--listen 127.0.0.1:29788`. This creates separate helper/relay state and a one-time
owner recovery key, with no public hostname, Agent listener, Cloudflare tunnel
or OpenAI tunnel client. Enrollment explicitly sends an empty `mcp_url`; the
Dashboard labels this as relay-only while retaining signed enrollment checks.
Run only its scoped Broker/Desktop/Dashboard components. Generic remote
Kill/Rotate/Resume is unavailable for this mode until instance-specific service
ownership is provisioned, preventing it from touching production service labels.
Hub and relay-only device modes cannot be combined in one state directory.

Read-only deployment preflight confirmed the Pi5 is aarch64, its existing
Executor services are active, SSH and administrator access are available, and
the proposed Hub port 28787 was unused. The existing Beta Dashboard deployment
matches its documented resource identity; Wrangler and the Cloudflare connector
can read the owning account. No runtime or cloud resource was changed by this
preflight. Use the Beta-only Dashboard configuration, never the generic
production deployment defaults.

The fleet page now has a collapsed **Manage Pi5 Hub** panel using the existing
Dashboard design. It supports public-identity registration, Hub disable, and
authorization/revocation for unlocked online devices. Mutations require a
separate confirmation step; device generation changes invalidate a pending
confirmation. Private-key registration JSON is rejected before transmission.
Read failures and unconfirmed mutations do not trigger automatic operation
retries. Unlocking for Hub management returns to the fleet rather than opening
an unrelated device workspace.

Component tests cover confirmation, generation changes, private-key rejection
and no-retry errors. A local browser fixture verified confirmation/cancellation,
focus restoration, updated authorization status, locked/offline controls and
390px responsive layout without horizontal overflow. The fixture has no real
device/network mutations and is not part of the production asset build. Live
Dashboard owner operations still require deployment/provisioning acceptance.

`hub_enabled: true` in a dedicated Agent configuration selects Hub-only dispatch.
`executor agent --config <path>` retains the normal OAuth-protected HTTP MCP
entrypoint but exposes `devices_list` plus device-addressed tools. Missing or
invalid Hub configuration fails startup; it never falls back to native dispatch.
The local stdio entrypoint also uses Hub schemas when explicitly selected, but
remote multi-client deployment should use the OAuth HTTP entrypoint so caller
scopes include authenticated OAuth client identity and MCP session.

The dedicated state directory must contain owner-protected `hub.json`:

```json
{
  "version": 1,
  "hub_id": "pi5-hub",
  "dashboard_url": "https://dashboard.example",
  "machine_token_file": "hub-machine.token"
}
```

The machine credential is read from the protected referenced file within that
state directory; the Hub signing key comes from its own secret store. Do not
reuse an unrelated device's state. Grants and enrollment-registry device keys come
from the authenticated `/api/hub/devices/{id}/delegation` endpoint and are
cryptographically checked before signing. A new owner-approved device therefore
requires no hand-edited routing table. This runtime does not create credentials
or approve devices by itself; owner provisioning remains a separate step.

Remaining implementation: machine-API deployment/provisioning; device/caller-bound sessions and capture
handling; Pi5 daemon/CLI integration; enrollment UX; isolated multi-device and
real ChatGPT read/write acceptance. No live deployment or permission changes
have been performed for this branch.
