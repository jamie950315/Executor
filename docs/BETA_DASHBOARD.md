# Isolated Mac Beta Dashboard

## Current deployment

The Beta at https://beta-executor-dashboard.0ruka.dev uses UI revision `b5d6b29` from `codex/beta-relay-error-ui`, based on `b39d818`. The original verified `b39d818` Worker program is unchanged; only client assets changed. No production deployment was performed.

| Resource | Beta identity |
| --- | --- |
| Worker | `beta-executor-dashboard` |
| Current Worker version | `530fafe6-3806-4b9c-8ccf-36ef586238a2` |
| Current deployment | `e205f950-2076-4517-b929-5176765fa0b4` |
| D1 | `50019b68-5402-4809-8648-d692f6a3f210` |
| Durable Object namespace | `955e3ce6b7b9454f9a5300f8835ec385` |
| Owner Access application | `4f9a2756-c343-4afe-b45b-6b476aea4edd` |
| Signed-device Access application | `95cb5ea5-f52f-47d8-a1a9-d462deb6a4b2` |

The owner application allows only the owner's existing email identity. The more-specific `/api/device/*` application delegates enrollment and signed relay authentication to the Worker. Workers.dev and preview URLs are disabled. Two migrations were applied only to the new Beta D1 database.

The Mac continues using the existing `bundle-ec5f53d/executor`. A new user LaunchAgent, `/Users/jamie/Library/LaunchAgents/com.executor.beta.dashboard.plist`, runs the Dashboard/Go relay using the existing Beta config, with its local listener at `127.0.0.1:18788`. Agent, Broker, Desktop, and the shared Tunnel were not restarted. Only `unified_dashboard` changed in the Beta config. Enrollment was disabled after registration and its temporary bearer file was removed by the enrollment CLI.

## Beta finalization (2026-09-14)

Both finalization tasks passed on `codex/beta-finalization`, based on the deployed UI branch at `929c9e1`. Main remains at `e8b5a250484ccc0b657bcc764d16dacf71396670` with its existing three unpushed commits; no merge, push, release, dependency upgrade or production deployment was performed. Worker version, UI assets, D1/DO/Access bindings, enrollment-disabled state and generation-2 credentials remain unchanged.

### A. Installed four-service stop helper

Canonical source is `scripts/beta_stop.py` (`c4a7142`); `scripts/install_beta_stop.py` performs a same-directory atomic replacement with a complete prior-file backup and ownership/mode preservation. The installed `/Users/jamie/Library/Application Support/Executor Beta/stop.py` exactly matches the version-controlled source.

Validated stop order is Dashboard → Desktop → Agent → Broker:

1. `gui/501/com.executor.beta.dashboard`
2. `gui/501/com.executor.beta.desktop`
3. `system/com.executor.beta.agent`
4. `system/com.executor.beta.broker`

All four exact plist labels, program paths, role/config arguments, filesystem ownership, non-symlink paths and loaded-service identities are checked before mutation. Unknown/denied service probes fail closed; a proven absent service is reported as `already_unloaded`. Stop failures name the target and preserve the disabled marker. Existing markers are not truncated, and no shared cloudflared or production target is included.

Backup directory: `/Users/jamie/Library/Application Support/Executor Beta/finalization-20260914.K4Grf0/stop-backup`. It contains the original `stop.py` and `metadata.json` (UID 501, GID 20, mode 0644):

- Original SHA-256: `2147628a931fcf67824ec48362833b9f51fb7f96c0c1817352fe89e1ce1b9abd`.
- Installed SHA-256: `e8cbaf0217b1ba9ddfd407ebdbc87f9ddf1a3af87bb7ecc65ab39e53c63879f9`.

The original three-target omission failed its regression test before implementation. All 22 stop/installer tests now pass, including exact four-target stop order, a fake full stop/start cycle, read-only checks, absent services, failures, denial, malformed identity, symlinks, ownership and atomic replacement. Four independent recovery-guard tests also pass. The real installed helper was invoked only with `--check`; all four targets reported loaded. Before/after-check inventory proved no PID, marker, configuration or credential changes.

To check the installation without mutation:

```sh
python3 -B '/Users/jamie/Library/Application Support/Executor Beta/stop.py' --check
```

If explicit rollback of this helper is requested, run `scripts/install_beta_stop.py --restore-from <the backup directory above> --backup-dir <a new directory beneath the private finalization directory>`. This verifies backup identity/hash, backs up the current helper and atomically restores the original ownership/mode. It does not start/stop services, but restores the original three-target limitation. Rollback was not performed.

### B. Complete public 502 JSON body

The existing Access-authenticated, generation-2-unlocked browser issued one FIFO file-read control request through the unchanged public Worker, real Go relay and Beta helper. The temporary `scripts/beta_body_probe.js` observer used `Response.clone().body.getReader()` on that same HTTP response; the UI consumed/cancelled its original branch normally. This added no second network request and read no cookies/tokens. The injected function's SHA-256 matched the tested source: `f31a3584728a4d1384776cbfcea5594fa8ca6ae5759b006eafdd1d93dd9daf9a`.

| Check | Actual result |
| --- | --- |
| HTTP status / content type | 502 / `application/json; charset=utf-8` |
| Body size | 155 bytes, measured from reader chunks |
| Reader termination | Normal EOF, no read exception |
| UTF-8 / JSON | Fatal UTF-8 decoder and complete JSON parse succeeded |
| `code` | `relay_stream_failed` |
| `outcome` | `unconfirmed` |
| Body and header request ID | Both `b03d2d3e-4af8-4a55-8663-18c32bf2d7b0` |
| FIFO request count | 1, independently confirmed by browser network events |

The independent local `scripts/beta_body_guard.py` was running before fault injection. It waited at most 90 seconds for a stop request, stopped only the exact Dashboard LaunchAgent, automatically restored it after a five-second fault window, released the FIFO nonblockingly in `finally`, and imposed a further 90-second limit for completion/fixture cleanup. Each launchctl call is bounded at ten seconds; the browser observer has a 20-second request limit and an 8 KiB response cap. No request used the relay it was stopping as its control channel.

The UI retained the correlated unconfirmed-outcome notice and prevented saving the failed preview. After recovery, one explicit normal-file request and one explicit empty-file request both returned HTTP 200 and normal EOF: file contents were exactly 44 bytes and zero bytes respectively. Their response bodies were 384 and 321 bytes. The browser stayed unlocked at generation 2; all three fixture request counts were exactly one. Three isolated observer tests cover complete EOF despite UI cancellation, mismatched request identity and invalid/empty JSON.

The observer was removed, original `window.fetch` restored, and no readers remained pending. The guard exited with code 0, released the FIFO, and removed `/private/tmp/executor-beta-final-jqjoct86` and all its fixtures. The test notice was dismissed after verification. The final browser showed `UNLOCKED` / `RELAY LIVE`; CLI relay status was `connected`.

Private metadata-only inventories and the guard report are retained under `/Users/jamie/Library/Application Support/Executor Beta/finalization-20260914.K4Grf0`. Across 12 services and 30 file records, only the expected Dashboard PID changed (`9143` → `19293`) and only `stop.py` changed bytes. Production and Fetch Proxy PIDs, executable/plist/config/credential hashes, Beta config/secrets/OAuth hashes, and disabled-marker absence were preserved. There was no OAuth-state drift or credential rotation to explain. No HTTP/UI core patch or redeployment was needed. The original historical field incident's trigger remains unconfirmed.

### Re-running the narrow acceptance

Run `python3 -B -m unittest discover -s scripts/tests -p 'test_beta*.py'` and `node --test scripts/tests/beta_body_probe.test.mjs` from this worktree. `beta_body_guard.py prepare` creates new private fixtures. Start its `watch --directory <fixture> --report-dir <private metadata directory>` in an independent local process and confirm its ready result before requesting a fault. In the already-authorized Beta browser, install the checked-in observer for that exact fixture directory, issue the UI FIFO read once, and confirm one pending request before creating `stop.request`. Read the observer's metadata snapshot, confirm restoration, then explicitly read the two normal fixtures. Always call observer cleanup and delete its temporary browser handle, create the fixture `complete` marker, and wait for guard exit/cleanup. Abort on access denial; never copy browser credentials or weaken Access. Only the dedicated Dashboard relay may be stopped during this live acceptance.

## UI error retention verification

The UI retains Files during offline fleet updates so pending responses can finish and failed previews remain unsaveable. Other workspaces retain their existing offline cleanup behavior. A browser-memory operation notice carries a bounded error code and validated request ID, survives reconnect and navigation to the fleet, and clears only when dismissed or the page is reloaded. It never renders arbitrary upstream error text. No host operation is automatically retried. Preview transport errors no longer suggest binary download as a remedy.

Regression tests first failed on the old forced workspace removal and on the old binary-download suggestion, then passed after each fix. Final applicable Dashboard coverage is 303 passing tests and one existing skip (163 unit, 76 UI, 64 Worker), with TypeScript, ESLint, client build and Worker dry-run passing. Worker/Go sources and installed binaries were not changed; existing native and cross-build evidence remains applicable. The pinned dependency install reports six existing npm audit findings; dependency upgrades are outside this UI-only change.

Final public browser acceptance verified:

- Normal 25-byte UTF-8 and zero-byte file reads, plus exact equality for 212,000 bytes / 4,000 Unicode lines. Network evidence shows four successful reads at offsets 0, 65536, 131072, 196608 with 65536-byte limits.
- A real pending FIFO read through Worker → Go relay → Beta helper, followed by stopping only the dedicated Beta Dashboard LaunchAgent, returned HTTP 502 and `relay_stream_failed`. The visible notice included request ID `a3745157-b8ec-4a17-81a6-4ce9da3b1b98`, an unconfirmed outcome, and no retry claim.
- Offline fleet refresh retained Files with disabled controls; reconnect preserved the same notice and Save remained disabled. Network capture recorded exactly one FIFO request. Dismissing the notice did not enable Save. A deliberate new normal-file read after reconnect succeeded.
- Both temporary FIFO waits used during this follow-up were released and the dedicated relay restored. Beta secrets/OAuth state and production config/secrets hashes remained unchanged. Production Worker and D1/DO bindings remained unchanged. No setup, Kill, rotation, or helper restart was performed.

The client intentionally uses validated error headers and cancels recognized error bodies. The separate complete public JSON-body observation was subsequently completed in the finalization above. The earlier field incident's original trigger remains unconfirmed; these checks verify the reproduced failure path and its delivery, not a new cause for that historical incident.

## Initial deployment evidence

- All 14 prepared artifact hashes matched; source HEAD is exactly `b39d818` and its checkout remains clean.
- The 11 supplemental stream tests passed again on this Mac, including 500 cancellation/abort cases and Unicode split preservation. These are local tests, not public end-to-end acceptance.
- CLI `dashboard status --json` returned enrolled/connected. The actual Access-authenticated browser displayed exactly one Beta device, the Beta MCP URL, and `RELAY LIVE`.
- An unauthenticated Dashboard request redirects to the configured Access team; an unauthenticated enrollment POST returns 401. Beta and production MCP OAuth metadata return their distinct issuers.
- Beta secrets/OAuth-state hashes and production config/secrets hashes matched before and after enrollment. Production Worker remains `eb25ffbb-7c88-4c04-b72f-1d9a3556658e`, with its separate original D1/DO bindings.
- The initial enrollment command failed; a subsequent explicit enrollment succeeded. Its original HTTP response was not retained, so the transient cause is unconfirmed. No automatic operation retries were added.
- Following owner unlock, actual browser reads passed for an 83-byte UTF-8 fixture, a zero-byte file, and a 212,000-byte Unicode fixture (4,000 lines, exact whole-content equality). The large read exceeds the 64 KiB page size. No production files were used as fixtures.
- Two scoped disconnect injections used a disposable FIFO read and stopped only `gui/501/com.executor.beta.dashboard` before the pending read returned data. The public browser received HTTP 502; the first response had `x-executor-relay-error: relay_stream_failed`, JSON content type, and request ID `d5f4165d-2a5c-4921-ad11-a311f856e881`. This verifies the pre-first-byte status/error-header path, not complete error-body delivery.
- Before the UI follow-up, fleet revalidation returned the UI to the device list and hid operation-level errors. The UI limitation is superseded by the final acceptance above. A temporary response interception attempt did not retain a readable body and was cleared.
- Both FIFO waits were released; the Beta Dashboard LaunchAgent was restored and CLI status returned connected. The original browser unlock remained valid and a repeat normal-file read matched exactly. Beta secrets/OAuth and production config/secrets hashes remained unchanged. No key was retrieved, generated, or rotated during this deployment or follow-up.

## Recovery boundary

Current UI assets and Beta-only config are under `/Users/jamie/Library/Application Support/Executor Beta/dashboard-b5d6b29`. The original staging directory `/Users/jamie/Library/Application Support/Executor Beta/dashboard-b39d818` retains the Worker program, historical preparation, and `config.pre-enrollment.json` as a mode-preserving backup. Do not use the generic Dashboard deployment script: its resource names target production.

For an explicitly requested UI rollback, use Wrangler rollback with the Beta-only config and prior verified version `5356a08f-be79-4764-9fef-372954f4e721`. That restores the original `b39d818` UI, including its known notice-loss behavior, without changing device enrollment or credentials. Verify current remote ownership and version before rolling back. Do not use the initial pre-enrollment upload `d8c7d549-65bb-4605-b55c-c5c5c0502fe4` as a fully configured rollback target.

For an explicitly requested enrollment rollback, stop only the new relay using `launchctl bootout gui/501/com.executor.beta.dashboard`, then restore only prior `unified_dashboard` configuration after checking for intervening changes. Preserve all other configuration and credentials; move aside only its exact LaunchAgent plist if automatic login startup must stop. Do not run Setup, Kill, Rotate, or Resume: generic lifecycle labels target production. The updated Beta `stop.py` now manages all four Beta services; use it only when a full Beta stop is explicitly requested, not for an isolated relay test. Retain isolated cloud resources unless removal is requested. Never change production resources as part of Beta rollback.
