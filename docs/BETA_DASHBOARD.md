# Isolated Mac Beta Dashboard

## Current deployment

The verified `b39d8188c213f042061f1ae61460d21d4a03582f` Worker and client artifacts were deployed on 2026-09-14 to https://beta-executor-dashboard.0ruka.dev. No production deployment was performed.

| Resource | Beta identity |
| --- | --- |
| Worker | `beta-executor-dashboard` |
| Current Worker version | `5356a08f-be79-4764-9fef-372954f4e721` |
| Current deployment | `bfadf7cc-5a7c-4744-9bf0-d10f22c2a36b` |
| D1 | `50019b68-5402-4809-8648-d692f6a3f210` |
| Durable Object namespace | `955e3ce6b7b9454f9a5300f8835ec385` |
| Owner Access application | `4f9a2756-c343-4afe-b45b-6b476aea4edd` |
| Signed-device Access application | `95cb5ea5-f52f-47d8-a1a9-d462deb6a4b2` |

The owner application allows only the owner's existing email identity. The more-specific `/api/device/*` application delegates enrollment and signed relay authentication to the Worker. Workers.dev and preview URLs are disabled. Two migrations were applied only to the new Beta D1 database.

The Mac continues using the existing `bundle-ec5f53d/executor`. A new user LaunchAgent, `/Users/jamie/Library/LaunchAgents/com.executor.beta.dashboard.plist`, runs the Dashboard/Go relay using the existing Beta config, with its local listener at `127.0.0.1:18788`. Agent, Broker, Desktop, and the shared Tunnel were not restarted. Only `unified_dashboard` changed in the Beta config. Enrollment was disabled after registration and its temporary bearer file was removed by the enrollment CLI.

## Evidence and limits

- All 14 prepared artifact hashes matched; source HEAD is exactly `b39d818` and its checkout remains clean.
- The 11 supplemental stream tests passed again on this Mac, including 500 cancellation/abort cases and Unicode split preservation. These are local tests, not public end-to-end acceptance.
- CLI `dashboard status --json` returned enrolled/connected. The actual Access-authenticated browser displayed exactly one Beta device, the Beta MCP URL, and `RELAY LIVE`.
- An unauthenticated Dashboard request redirects to the configured Access team; an unauthenticated enrollment POST returns 401. Beta and production MCP OAuth metadata return their distinct issuers.
- Beta secrets/OAuth-state hashes and production config/secrets hashes matched before and after enrollment. Production Worker remains `eb25ffbb-7c88-4c04-b72f-1d9a3556658e`, with its separate original D1/DO bindings.
- The initial enrollment command failed; a subsequent explicit enrollment succeeded. Its original HTTP response was not retained, so the transient cause is unconfirmed. No automatic operation retries were added.
- Following owner unlock, actual browser reads passed for an 83-byte UTF-8 fixture, a zero-byte file, and a 212,000-byte Unicode fixture (4,000 lines, exact whole-content equality). The large read exceeds the 64 KiB page size. No production files were used as fixtures.
- Two scoped disconnect injections used a disposable FIFO read and stopped only `gui/501/com.executor.beta.dashboard` before the pending read returned data. The public browser received HTTP 502; the first response had `x-executor-relay-error: relay_stream_failed`, JSON content type, and request ID `d5f4165d-2a5c-4921-ad11-a311f856e881`. This verifies the pre-first-byte status/error-header path, not complete error-body delivery.
- The current browser client deliberately cancels recognized relay-error bodies in `src/ui/api.ts`. During the injections, fleet revalidation returned the UI to the device list, so a persistent operation-level error message and complete JSON body were not verified. A temporary response interception attempt did not retain a readable body and was cleared. Do not claim this acceptance item fully passed or that the original field incident is explained.
- Both FIFO waits were released; the Beta Dashboard LaunchAgent was restored and CLI status returned connected. The original browser unlock remained valid and a repeat normal-file read matched exactly. Beta secrets/OAuth and production config/secrets hashes remained unchanged. No key was retrieved, generated, or rotated during this deployment or follow-up.

## Recovery boundary

The private staging directory is `/Users/jamie/Library/Application Support/Executor Beta/dashboard-b39d818`. It contains `wrangler.beta.json`, `DEPLOYMENT.json`, the unchanged historical `PREPARATION.json`, and `config.pre-enrollment.json` as a mode-preserving backup. Do not use the generic Dashboard deployment script: its resource names target production.

If rollback is requested, first stop only the new relay using `launchctl bootout gui/501/com.executor.beta.dashboard`. Restore only the prior `unified_dashboard` configuration from the backup after checking for intervening changes; preserve all other config and credential state. Remove or move aside only this exact LaunchAgent plist to prevent it loading next login. Do not run Setup, Kill, Rotate, or Resume: generic lifecycle labels target production. The pre-existing Beta `stop.py` does not include this new LaunchAgent.

This is the first Beta Worker deployment, so there is no earlier known-good Beta Worker to roll back to. The uploaded initial version `d8c7d549-65bb-4605-b55c-c5c5c0502fe4` precedes enrollment configuration; it is not a fully configured rollback target. After stopping the Beta relay, retain the isolated cloud resources for diagnosis unless their removal is explicitly requested. Never change production resources as part of Beta rollback.
