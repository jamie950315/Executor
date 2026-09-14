# Empty HTTP body investigation and first-byte guard — 2026-09-14

## Scope and confirmed mechanism

Work continues on local branch `fix/relay-result-validation`, starting from `1d05709`. This change is in the Dashboard Worker, Durable Object diagnostics and browser response parser. The installed Mac production and Beta binaries, their service configuration, credentials, domains and running central Cloudflare Dashboard were left unchanged. A rebuilt Mac executable alone will not activate this Worker/UI correction.

The previous `handleCall()` returned a successful NDJSON HTTP Response immediately after obtaining a Durable Object stream and recording a forwarded audit event. The stream could still contain zero bytes. When the device WebSocket closed, failed, was replaced, or reached its existing request deadline before its first response, `DeviceRelay.failPending()` errored that stream. At that point the HTTP success response had already been constructed; there was no response-boundary handler to convert the upstream failure into a nonempty error response.

This is a reproducible mechanism with the same outward symptom as the reported incident. It establishes a local implementation defect; attribution of the earlier production incident still requires its request-correlated trace. No claim is made that every empty response shares this cause.

## Reproduction and corrected behavior

Three regression tests were added and run before production source changes. All three failed against the original code: the application returned HTTP 200 before relay bytes existed, and injected WebSocket close/error produced status 200 with zero consumed body bytes and a rejected body read in the in-process Worker test transport.

An additional native harness uses real loopback HTTP and WebSocket sockets, the installed workerd/Miniflare runtime, the actual Dashboard routing/security headers, and real Durable Object RPC streams. In this transport the same controlled pre-first-byte disconnect reproduced HTTP 200 with a zero-byte body and a *normal EOF* rather than a rejected body read. This transport difference is recorded explicitly; the test does not infer network header-flush timing solely from application Response construction.

| Controlled pre-first-byte loss | Before | After |
| --- | --- | --- |
| HTTP status | 200 | 502 |
| Body bytes | 0 | 155 |
| Real loopback body consumption | Normal EOF | Valid JSON |
| Correlation | No HTTP request-ID header | Server-generated request ID in header and JSON |
| Reported host-operation outcome | Ambiguous transport result | `unconfirmed` |

Five additional independent baseline/fixed native HTTP pairs reproduced the same status/byte-count difference. The tests use a fixed, synthetic failure point rather than disturbing any installed service.

## Correction

- `relay-response.ts` waits for the first nonzero upstream byte chunk before returning HTTP 200. Empty EOF and a first-read exception return bounded JSON 502 errors, tagged `relay_empty_body` or `relay_stream_failed`. Request abort is propagated as cancellation; oversized responses retain the existing byte limit.
- Success responses remain streaming, with one retained first chunk and backpressure. Partial data followed by transport loss stays an error/incomplete outcome. No automatic host-request retry is added, and the existing ordinary/long-running request deadlines are preserved.
- A failed audit insert after dispatch cannot replace a completed device response with a generic HTTP error. A Worker regression injects a temporary audit failure and confirms that the response survives without replay.
- `x-executor-request-id` joins HTTP/browser errors to Durable Object diagnostics. Fixed metadata identifies dispatch, first byte, stream completion, cancellation and failures, with byte counts and elapsed time. Socket close, socket error, connection replacement and timeout reasons are distinguished. Response content, file paths, command text, tokens, recovery keys, browser cookies and raw remote exception text are excluded from the new diagnostic events.
- The browser distinguishes missing body, zero-byte EOF, incomplete NDJSON, transport-read error and mismatched request correlation. It displays only whitelisted transport codes and validated UUID request IDs. Existing invalid-preview Save protection remains active.

## Verification on Mac

| Check | Result |
| --- | --- |
| Red tests on original Worker code | Three expected failures |
| Final Dashboard suite | 301 passed, one existing skipped test: unit 163, UI 74, Worker 64 |
| Additional coverage in this change | 33 tests: 26 shared/parser/stream tests and seven Worker HTTP tests |
| Worker fault paths | Before-first-byte close/error/replacement; real 10-second deadline; midstream loss; subsequent explicit reconnect call; audit failure |
| Stream handling | Empty EOF, zero-length chunks, split UTF-8, bounds before/after headers, backpressure, cancellation, diagnostic-sink failure |
| TypeScript / ESLint | Passed |
| Client build / Worker dry-run build | Passed |
| Full native `go test ./... -count=1` | Passed |
| Relay and relay-client race detector | Passed |
| Go vet / native build | Passed |
| Six `CGO_ENABLED=0` cross-builds | darwin/linux/windows, amd64/arm64 passed |
| Native loopback baseline/fixed comparison | Reproduced; five additional independent pairs passed |
| Installed Beta helper data through fixed local HTTP path | 100 full reads, 400 page requests, 198,000 bytes; all SHA-256 reconstructions identical |
| Empty-file result | Valid zero-byte EOF preserved |

Toolchains: Go 1.24.3 darwin/arm64; the final Dashboard and native HTTP checks use Node.js 24.10.0 and locked repository dependencies. Fault injection can emit workerd `device offline` diagnostics; all final test processes exited successfully. Early harness setup issues involving the Miniflare v5 options converter, single-module input and a required fixture JWT `nbf` claim were corrected in the harness before these passing runs.

The Beta-backed native HTTP test is deliberately scoped: an ephemeral authenticated WebSocket test peer forwards only files created by that run through the installed Beta's documented local stdio interface. The Worker/HTTP/DO streams and Beta helper file results are real. The peer is a deterministic test fixture, not the deployed Go relay client's WebSocket connection, and this is not a Cloudflare-edge browser acceptance test. Ephemeral test identities and temporary fixture files are isolated and cleaned up; the existing Beta recovery key is not consumed or changed.

## Re-running

From `dashboard`, use Node.js satisfying the repository's engine constraint:

```sh
npm test
npm run check
npm run build
node scripts/verify-relay-http.mjs --iterations 100
```

The native harness can exercise installed Beta helper results explicitly:

```sh
node scripts/verify-relay-http.mjs --iterations 100 \
  --beta-binary '/Users/jamie/Library/Application Support/Executor Beta/bundle-ec5f53d/executor' \
  --beta-config '/Users/jamie/Library/Application Support/Executor Beta/state/config.json'
```

For baseline comparison, export `1d05709` to a temporary directory and pass its Dashboard directory as `--source <directory>/dashboard --baseline`. The harness never deploys a Worker or modifies an installed service. It binds only loopback, uses an ephemeral registry, and permits its Beta adapter to read only its own fixture paths.

## Deployment boundary and next acceptance

The patch is committed locally for review; the central production Worker and deployed Beta executable remain at their pre-test versions. Public activation requires deploying the Worker/UI changes to a separately isolated Beta Dashboard and enrolling the Beta relay there, followed by browser-origin HTTP acceptance and correlated trace collection. Existing production hosts, the current central Dashboard and recovery credentials should be preserved through that step. Until that deployment is explicitly performed, the public central Dashboard still uses its original response-boundary behavior.

Credential-free detailed execution evidence remains in `/tmp/executor-empty-body-20260914-93430/` on the Mac, including the original failing Worker run, baseline/fixed native HTTP summaries, Beta-backed 100-read run and final test/build output.
