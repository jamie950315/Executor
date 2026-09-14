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

## UI error retention verification

The UI retains Files during offline fleet updates so pending responses can finish and failed previews remain unsaveable. Other workspaces retain their existing offline cleanup behavior. A browser-memory operation notice carries a bounded error code and validated request ID, survives reconnect and navigation to the fleet, and clears only when dismissed or the page is reloaded. It never renders arbitrary upstream error text. No host operation is automatically retried. Preview transport errors no longer suggest binary download as a remedy.

Regression tests first failed on the old forced workspace removal and on the old binary-download suggestion, then passed after each fix. Final applicable Dashboard coverage is 303 passing tests and one existing skip (163 unit, 76 UI, 64 Worker), with TypeScript, ESLint, client build and Worker dry-run passing. Worker/Go sources and installed binaries were not changed; existing native and cross-build evidence remains applicable. The pinned dependency install reports six existing npm audit findings; dependency upgrades are outside this UI-only change.

Final public browser acceptance verified:

- Normal 25-byte UTF-8 and zero-byte file reads, plus exact equality for 212,000 bytes / 4,000 Unicode lines. Network evidence shows four successful reads at offsets 0, 65536, 131072, 196608 with 65536-byte limits.
- A real pending FIFO read through Worker → Go relay → Beta helper, followed by stopping only the dedicated Beta Dashboard LaunchAgent, returned HTTP 502 and `relay_stream_failed`. The visible notice included request ID `a3745157-b8ec-4a17-81a6-4ce9da3b1b98`, an unconfirmed outcome, and no retry claim.
- Offline fleet refresh retained Files with disabled controls; reconnect preserved the same notice and Save remained disabled. Network capture recorded exactly one FIFO request. Dismissing the notice did not enable Save. A deliberate new normal-file read after reconnect succeeded.
- Both temporary FIFO waits used during this follow-up were released and the dedicated relay restored. Beta secrets/OAuth state and production config/secrets hashes remained unchanged. Production Worker and D1/DO bindings remained unchanged. No setup, Kill, rotation, or helper restart was performed.

The client intentionally uses validated error headers and cancels recognized error bodies; the public raw JSON body was not separately read. The unchanged Worker JSON boundary is covered by Worker tests. The earlier field incident's original trigger remains unconfirmed; this follow-up verifies error presentation and recovery, not a new cause for that historical incident.

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

For an explicitly requested enrollment rollback, stop only the new relay using `launchctl bootout gui/501/com.executor.beta.dashboard`, then restore only prior `unified_dashboard` configuration after checking for intervening changes. Preserve all other configuration and credentials; move aside only its exact LaunchAgent plist if automatic login startup must stop. Do not run Setup, Kill, Rotate, or Resume: generic lifecycle labels target production. The pre-existing Beta `stop.py` does not include this new LaunchAgent. Retain isolated cloud resources unless removal is requested. Never change production resources as part of Beta rollback.
