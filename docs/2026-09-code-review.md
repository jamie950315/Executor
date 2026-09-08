# September 2026 code review

## Scope and delivery boundary

Reviewed the host runtime and dispatch, OAuth/MCP/IPC and relay code, terminal,
filesystem and desktop boundaries, configuration and secret storage, lifecycle,
CLI, deployment scripts, service templates, packaging, and Dashboard application.
Changes target reproducible defects and measured costs, not a wholesale rewrite.
This is a source-review delivery, not an upgrade of the installed services.
No installed service, enrollment, tunnel, or owner credential was changed.

## Corrections

- OAuth token issuance cannot upgrade a revoked grant to the new generation;
  code and refresh-token expiry is enforced at the deadline. Concurrent state
  saves use unique temporary files and same-Core ordering.
- IPC replay protection retains future-dated nonces for their entire acceptance
  period. DCR rejects extra JSON and over-limit trailing data before registration;
  MCP rejects invalid request-ID types before tool dispatch.
- File writes reject missing or non-string content before touching the host.
  Explicit empty strings remain supported. Write/append handling is shared.
- Cancelled relay request IDs remain reserved until their handlers finish.
- Terminal completion waits for final output. Unix output pipes are not closed
  prematurely by process waiting. Failed or timed-out termination reports an
  error and retains the session for retry instead of silently losing control.
- Terminal output uses at most two bulk copies per append/read rather than a
  modulo operation per byte. Capacity, truncation, and cursor behavior are retained.
- Dashboard slow output polling is allowed to finish; stale lists and file reads
  cannot overwrite newer selections or manually edited paths. Invalid responses
  no longer become successful empty results. Failed previews disable saving.
  Editing the destination path preserves unsaved text.
- Lost control responses are described as unconfirmed outcomes. Audit storage
  failures and unexpectedly stopped relays produce fixed diagnostic codes without
  raw errors, credentials, file paths, or tool output. A completed operation's
  result is preserved to avoid prompting a destructive retry.
- Lifecycle CLI commands reject extra arguments before side effects. JSON-output
  write failures propagate; service-operation failures identify the component.
- Windows uninstall stops if rollback fails, preserving state and credentials.
  macOS service XML escapes special characters. Linux/WSL distinguish quoted
  executable arguments from unquoted whole working-directory paths, escape
  expansions, and reject values the latter setting cannot represent safely.

## Verification

Each defect correction includes a regression test run before the production fix.
Validation includes the complete Go race suite, Go vet, six platform/architecture
binary builds (Darwin builds include native CGO), Dashboard unit/component/Worker
tests, type checking, linting, client build and Worker dry-run, packaging tests,
shell syntax checks, and whitespace checks.

Final Dashboard results: 90 unit tests, 36 component tests, and 44 Worker tests
passed; one Windows-only unit test was skipped on macOS. The complete Go race
suite and all six binary builds passed after integration corrections.

Runtime checks include authenticated local helper IPC, persistent terminal and
filesystem operations, local stdio dispatch, and audit-failure injection. The
terminal suite also ran on the Pi's actual Linux runtime. A harmless isolated
systemd fixture was parsed on the Pi without starting a service; its working
directory preserved both spaces and a literal percent sign. An initially incorrect
quoted WorkingDirectory was caught by that native check and corrected.

On this Mac, repeated 100 ms microbenchmarks changed 4 KiB output-buffer appends
from roughly 10.1–10.5 microseconds to 61–63 nanoseconds, and 8 MiB reads from
roughly 4.3–4.8 milliseconds to 0.22–0.24 milliseconds. These are isolated buffer
measurements, not whole-application speedups or production throughput promises.

## Limits and retained safeguards

- Windows-specific PowerShell execution and Windows/macOS installed-service
  upgrades were not performed. The Windows rollback subprocess regression is
  included for Windows CI. The Windows cross-volume deployment test is skipped
  on macOS; cross-compilation is not a Windows runtime test.
- Dashboard component interactions use real React components with controlled
  slow/out-of-order responses, and Worker tests use workerd. No authenticated
  production browser session or physical desktop-input workflow was exercised.
- Concurrent OAuth-file tests do not establish cross-process transactional
  coordination between independent stale in-memory snapshots.
- Network reconnect backoff, platform capability checks, safe cleanup, recovery
  rollback, and the independent local rescue page are retained intentionally.
  Removing these would reduce reliability; failures should be diagnosed without
  exposing sensitive data or falsely claiming a completed host operation failed.
- Passing this review is not a guarantee that every possible defect has been
  eliminated. Target-host validation is still required before deployment.
