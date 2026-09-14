# Relay Result Validation Review — 2026-09-14

## Scope

- Local branch: `fix/relay-result-validation`, based on `main` commit `80194106622960a45bd2e806fa09681dab2a6084`.
- Work performed through the existing production Mac Executor MCP in `/Users/jamie/Executor`.
- Production changes are limited to outgoing Go relay result validation and its explicit failure response. Dashboard production code remains unchanged; its additional coverage is test-only.
- Existing device installations, running services, credentials, Dashboard deployment, release tags, and `main` are preserved. This change is local and has not been pushed or deployed.

## Confirmed defect and correction

The old `resultMessages()` checked for a zero-length result after its single-response fast path. A nil or zero-length `json.RawMessage` was omitted from `ResponsePayload` by `omitempty`; the outer payload still contained `request_id`, so envelope creation succeeded and returned before the empty-result check. Malformed JSON could also fall through to base64 stream chunks.

The corrected function checks the raw byte limit first, then requires one complete UTF-8 JSON value before either encoding path. Valid JSON values such as `null`, `""`, `{}`, `[]`, `false`, `0`, and an empty-file result remain supported. Invalid data returns a dedicated internal sentinel. `connection.writeResult()` translates that sentinel to a correlated `response` with `failure.code = "invalid_result"`, preserving the existing `result_too_large` behavior. Failure responses contain only fixed metadata, and transport write errors propagate without retry.

The existing large-payload chunking fixture now serializes a valid JSON string while retaining its byte-bound, ordering, final-chunk, and lossless reconstruction assertions.

## Test-driven evidence

Before changing production behavior, the new Go regressions produced 24 failing subcases: 16 encoder rejection checks and eight explicit-failure checks. They cover nil, zero bytes, whitespace, incomplete JSON, trailing JSON, plain text, large malformed JSON, and invalid UTF-8.

After the correction, all relay tests pass. Additional cases verify valid empty values, multibyte string chunking, raw-size error precedence, connection reuse after an invalid result, fixed failure metadata, and write-error propagation without retry.

Dashboard coverage adds 14 parser tests and seven Worker tests. These check missing/empty bodies, omitted result/failure fields, explicit `invalid_result` failures, valid empty values, transport errors and incomplete streams, as well as normal/final-chunk/failure delivery across Durable Object RPC, close/error events, connection replacement, and timeout before the first chunk.

In the installed local workerd runtime, injected RPC stream failures replace the source error text with a generic premature-disconnection error. The cross-RPC tests assert rejected consumption, independently of exact runtime wording; the same-object timeout test checks its specific timeout reason. Fault-injection runs emit workerd `device offline` diagnostics while Vitest completes successfully.

## Validation completed on Mac

| Check | Result |
| --- | --- |
| `go test ./... -count=1` | Passed |
| `go test -race ./internal/relayclient ./internal/relay -count=1` | Passed |
| Focused result regression suite, `-count=100` | Passed |
| `go vet ./...` | Passed |
| Native `go build ./...` | Passed |
| `GOOS`/`GOARCH` cross-builds with `CGO_ENABLED=0` | Passed for darwin, linux and windows, each amd64 and arm64 |
| Dashboard `npm test` | 268 passed, one existing skipped test: unit 137, UI 74, Worker 57 |
| Dashboard `npm run check` | TypeScript and ESLint passed |
| Dashboard `npm run build` | Client build and Worker `--dry-run` passed |
| `TestClientUsesRealTLSWebSocketTransportForHandshakeRefreshAndHeartbeat` | Passed against a local TLS WebSocket test server |
| `TestRunStdioDispatchesWithoutOAuth` | Passed using the repository's isolated stdio fixture |
| `git diff --check` | Passed |

Toolchain: Go 1.24.3 on darwin/arm64, Node.js 24.10.0, and the repository's pinned Dashboard dependencies. Cross-builds establish compilation coverage; native runtime validation in this change is confined to Mac.

## Incident boundary

This work proves and fixes an outgoing-result validation defect. The original intermittent empty HTTP/relay body still lacks a correlated production request trace establishing its cause. The new tests establish successful delivery and error propagation for the exercised local paths; they do not establish the root cause of that production incident or a production fix for it. Production request retry, WebSocket lifecycle logic, and UI error wording are unchanged.
