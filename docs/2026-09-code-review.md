# September 2026 code review

## Scope and delivery boundary

Reviewed the host runtime and dispatch, OAuth/MCP/IPC and relay code, terminal,
filesystem and desktop boundaries, configuration and secret storage, lifecycle,
CLI, deployment scripts, service templates, packaging, and Dashboard application.
Changes target reproducible defects and measured costs, not a wholesale rewrite.
The initial source review was followed by an owner-authorized deployment and
validation cycle on Mac, Pi5, Windows CTPS, and WSL. Mac, Pi5, and WSL run
`b75f0fb77f50b72857fbc44440846f80d1236697`; Windows was subsequently updated to
`db1b7e6a4c409b2d0342c05e0d98023cd4de43b9`, all with clean build provenance.
Production configuration, enrollment, tunnels, and owner credentials were preserved.

## Corrections

- OAuth token issuance cannot upgrade a revoked grant to the new generation;
  code and refresh-token expiry is enforced at the deadline. Concurrent state
  saves use unique temporary files and process-wide reader/writer ordering. Native
  Windows tests exposed replacement failures across different Core instances;
  serialization fixes the cause without retries hiding the error.
- IPC replay protection retains future-dated nonces for their entire acceptance
  period. DCR rejects extra JSON and over-limit trailing data before registration;
  MCP rejects invalid request-ID types before tool dispatch.
- Actual MCP connector testing exposed clients hiding execution errors behind
  HTTP 500. Execution failures now use standard MCP `isError` text results, so
  users see the reason without a fabricated successful structured result.
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
- Native Windows deployment tests exposed Node's `shell:true` command-shim
  warning interrupting PowerShell. Windows now invokes the selected npm JavaScript
  entrypoint directly through Node, without suppressing warnings.
- A final Mac check exposed false-offline status: a 500 ms liveness probe invoked
  a potentially slow desktop OS query. Authenticated `executor.health` now returns
  a strictly validated protocol marker without calling the application handler.
  CLI status, local Dashboard reachability, and lifecycle readiness use it;
  actual desktop availability and permissions are still queried separately.
  Timeouts were not increased and no retry or stale-success cache was introduced.

## Verification

Each defect correction includes a regression test run before the production fix.
Validation includes the complete Go race suite, Go vet, six platform/architecture
binary builds (Darwin builds include native CGO), Dashboard unit/component/Worker
tests, type checking, linting, client build and Worker dry-run, packaging tests,
shell syntax checks, and whitespace checks.

Final Dashboard results: 104 unit tests, 51 component tests, and 44 Worker tests
passed; one Windows-only unit test was skipped on macOS. The complete Go race
suite and all six binary builds passed after integration corrections.

Windows native Go packages, ConPTY state/resize/no-parent-console tests, the
opt-in desktop probe, and the complete PowerShell deployment fixture passed.
The latter includes two full source-validation cycles and the uninstall failure,
ACL, junction, and protected-credential cases. WSL native terminal tests include
background process-tree shutdown. Production Kill/Rotate were not used.

## Installed runtime evidence

| Target | Installed executor SHA-256 | Live result |
| --- | --- | --- |
| Mac arm64 | `a8361e0192bacf73a19b42583acd8ac94a7b1edfd25ad6a37d150ce8659099ba` | Full doctor healthy; owner/admin UID 501/0; signed helper retains granted permissions; 1512x982 screenshot and capture-only batch returned real images. A completed terminal returned all 64 KiB plus its final marker. |
| Pi5 arm64 | `7d0b3a7e14ee28c9319059b000b2f28e4beb6a0bcc502fc11e832042faac8d67` | Full doctor healthy; owner/admin UID 1000/0; services active; headless desktop rejection is readable. |
| WSL amd64 | `4d84c3ae04bccfe90606d77c07331dc429142777fb239327ca7f80ecdc2952dc` | Full doctor healthy; owner/admin UID 1000/0; services active; no active Linux desktop session is reported explicitly. |
| Windows amd64 | `2b853860280588036a631fdfde77509cfba8213b2025d748840bc82f5ccc0270` | Full doctor healthy; interactive-owner/SYSTEM calls work; services and Desktop task running; full physical 2560x1440 capture and actual input verified at 125% scaling. |

All targets exercised real authenticated filesystem operations and rejected
missing-content writes without changing the existing fixture. Public issuer
identity and unauthenticated MCP rejection were checked independently of a
listener merely being online. Test sessions and fixture files were cleaned up.
No Defender settings were changed; Windows retained its existing exact-binary
exclusion. Temporary Mac upgrade jobs were removed, and prior binaries remain
available in protected host-local rollback backups.

The final health-probe revision passed its new named-pipe regression on real
Windows and corresponding tests on WSL. Every installed host then passed 20
consecutive status checks with all four local components online. Tests also
reject wrong keys and invalid authenticated health markers rather than accepting
an unrelated listener as healthy.

The existing central Dashboard was updated to Worker version
`e220fc40-b5d0-4f3c-97ce-19a9f73923db`, deployment
`59960d8b-7d5e-4471-b402-f3dac66ffb41`, with UI source `315d8a4` and client
asset `index-Gm1QQDRt.js`. Its bindings, custom domain, and Access
boundary remained unchanged; enrollment remained disabled. All four signed
device relays reconnected with recent heartbeats. Same-source Chrome interactions
verified delayed output, failed-preview save protection, and stale-read cancellation.

After the owner logged in and unlocked all four devices, production testing in
the Codex in-app browser verified each host's terminal command and Files-panel
read through the actual Dashboard relay. Mac/Windows file saves, Windows readback,
WSL file save and administrator UID, Mac permissions, audit retrieval, and both
Mac/Windows displayed screen captures succeeded. An absent Mac file disabled Save
instead of offering an empty overwrite. Pi5 capture accurately reported its
headless boundary. Only random, isolated test directories were used; those files
and the five test sessions were removed afterward.

This production test exposed raw Windows terminal escape codes in the old plain
text display. A read-only `@xterm/headless` parser now interprets VT control and
incremental UTF-8 without a new input surface or terminal replies to the host.
Regression tests cover carriage-return overwrite, cursor redraw, split sequences,
truncation reset, dimension bounds, malformed metadata, and rapid session
reattachment. The renderer retains bounded per-session state and disposes it on
close/unmount. Upstream MIT notices ship in `third-party-notices.txt`.

The final Dashboard was tested and built with Node 24.19.0. After deployment,
reloading the same authenticated browser retained all four unlocked devices.
Reattaching to the same Windows session visibly rendered its original output
without raw control codes. WSL emitted a single UTF-8 character split by a
two-second delay; the published page correctly displayed the completed character
and subsequent completion marker. No host restart or credential change was needed
for this frontend correction.

### Computer Use follow-up

Actual input testing, rather than screenshot-only testing, exposed a Windows DPI
error: at 125% scaling, a requested `(291,148)` cursor location became physical
`(364,185)`, outside the intended button. The previous 2048x1152 capture was also
smaller than the 2560x1440 physical primary display. Screenshot and mouse helper
processes now opt into physical-pixel coordinates before any WinForms display
query. Cursor movement is checked instead of discarding native failures. Native
Windows capture-size and no-input bindings tests passed before installation.

The panel also had a deferred drag-origin read, lost selected drag buttons,
missing pointer cancellation/capture, overlapping refresh/control calls, and an
automatic error-refresh path that obscured uncertain outcomes. Ten new component
regressions cover the corrected gesture and request boundaries. No delay/retry
workaround or automatic input replay was added.

The deployed fixes were exercised through the public Dashboard against an
isolated WinForms window on Windows. Verification used both returned images and
the fixture's actual received events:

- Click incremented the fixture counter and typing populated its input.
- Right-button drag received exactly the queued `(189,363)` to `(784,482)` path;
  event locations, not the potentially newer global cursor position, were checked.
- A 600-unit downward scroll moved the test list from row 1 to row 16.
- Tab followed by Enter activated the fixture button; a separate native check
  verified CTRL+A arrived with the Control modifier. Application shortcut behavior
  is separate from delivery and is not inferred from an expected selection change.
- Releasing a drag outside the screenshot queued no host action.
- Pending requests disabled refresh/input; an unconfirmed execution attempt
  stayed visible without automatic replay. Later independent refreshed tests
  succeeded; the unconfirmed attempt is not claimed to have completed.

Only the Windows runtime and existing Dashboard were updated for this follow-up.
Windows retained exact config/secret fingerprints and passed 20 consecutive status
checks plus full doctor. The temporary fixture and its terminal sessions were
closed; no personal application was clicked or typed into.

Operational consequence: the initial Pi5 restart ended six pre-existing terminal
sessions before their workload state was checked. Their session state cannot be
recovered. Subsequent host restarts checked existing workloads first; this is not
a claim that the first Pi5 restart was impact-free.

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

- Windows-specific PowerShell and installed-runtime checks were performed on
  Windows, not inferred from cross-compilation. Platform-inapplicable tests remain
  skipped on other operating systems.
- Dashboard component interactions use real React components with controlled
  slow/out-of-order responses, and Worker tests use workerd. Authenticated
  production in-app-browser flows are verified as listed above. Desktop input was
  confined to the isolated Windows fixture. Arbitrary personal-app operations,
  production Kill/Rotate, uploads, and destructive lifecycle changes were not exercised.
- Concurrent OAuth-file tests do not establish cross-process transactional
  coordination between independent stale in-memory snapshots.
- Network reconnect backoff, platform capability checks, safe cleanup, recovery
  rollback, and the independent local rescue page are retained intentionally.
  Removing these would reduce reliability; failures should be diagnosed without
  exposing sensitive data or falsely claiming a completed host operation failed.
- Passing this review is not a guarantee that every possible defect has been
  eliminated. Future upgrades still require target-host validation.
