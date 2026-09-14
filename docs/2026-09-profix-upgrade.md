# Production pro-fix rollout — 2026-09-14

## Delivered scope

The four existing production installations now run `pro-fix` commit
`b6f4b64de3aec0c716b6895ef40d5e2ddff22bc6`. A clean standalone checkout was
used to avoid nested-worktree version stamping. The binaries report this exact
revision with `vcs.modified=false`.

Production rebuilds should select `pro-fix`. This rollout does not merge its
implementation into `main`; the main checkout only receives the deployment
handoff documentation.

Existing per-device URLs, configuration, OAuth/recovery/IPC credentials,
enrollment, Cloudflare Tunnels and TURN settings were preserved. No Setup,
Kill or credential rotation was performed. The separate temporary Mac Beta
and Fetch Proxy installations were not replaced or removed.

The existing authenticated Beta administrator channel ran the narrowly scoped
Mac production replacement.

## Installed evidence

| Target | Installed executor SHA-256 | Verification |
| --- | --- | --- |
| Mac arm64 | `b527cbd9952f8af0e3ce54ac97ab13478b16628f2e38be0957aae433d999416f` | Full doctor, owner/admin file and process acceptance, signed 1512×982 screenshot |
| Pi5 arm64 | `00d71a54d39a4b683d628ba6cea329c231006a712ba4aa21f14d7b69376dbee8` | Full doctor, owner/admin file and process acceptance, connected Dashboard relay |
| WSL amd64 | `229ebbd3668657795cacfcec8774eafbff8632d23fec01d52e32f02b41849b62` | Full doctor, owner/admin file and process acceptance, connected Dashboard relay |
| Windows amd64 | `9487b1f984cc3a79ed35a52059aa94de85aa082ad5f1c51aaf937d4782cea74f` | Full doctor, owner/admin file and process acceptance, 2560×1440 screenshot |

Mac keeps the existing `dev.0ruka.executor.desktop` identifier and
`N3JN8G9YWK` signing team; Screen Recording, Accessibility and Input Control
remain granted. Windows retains its existing firewall and Defender settings.
The one-time upgrade task used process-scoped `RemoteSigned`, was removed
after completion, and did not alter the machine or account execution policies.

Host-local rollback copies are retained in
`/var/lib/executor/deployment-backups/profix-b6f4b64-20260914` on Unix and
`C:\ProgramData\Executor\deployment-backups\profix-b6f4b64-20260914` on
Windows. Mac also retains the original signed Desktop app. Before stopping
components, process-tree inspection confirmed that the pre-existing terminal
sessions contained only idle shells. Pi5 weather services and their existing
standalone process remained active across the upgrade.

## Tests and actual use

- Full Go suite, `go vet ./...`, `go build ./...`, and race tests for terminal,
  dispatch, desktop, broker and MCP passed on Mac.
- Both executable artifacts built for Darwin, Linux and Windows on amd64/arm64.
- Dashboard: 123 unit, 74 UI and 50 Worker tests passed; one platform-specific
  test was skipped. Type checking, lint, client build and Worker dry-run passed.
- Six compiled core-package test suites ran successfully on each of Pi5,
  Debian WSL and Windows, in addition to the local Mac tests.
- Installed-service acceptance exercised both owner and administrator helpers:
  nine-tool discovery and truthful annotations, complete multi-page UTF-8
  reassembly using base64 pages, stable write byte counts and missing-cwd errors.
- Mac/Linux additionally verified pipes, stdin EOF, separated stdout/stderr,
  exit 7, graceful SIGTERM with exit 42, and initial/resized PTY dimensions.
  Windows verified ConPTY exit 7 and explicit rejection of unsupported pipe mode.
- All four existing authenticated public MCP connections passed owner writes
  and exact owner/admin byte-range reads. Public metadata returned each device's
  distinct expected issuer; unauthenticated MCP returned HTTP 401 on all four.
- The owner's logged-in ChatGPT in-app-browser session completed normal
  file-write, six-byte slice, terminal-output and session-close workflows on
  all four production connectors with no observed refusal or operation failure.
  The four retained 28-byte fixtures were independently read back and matched.
  This is functional acceptance, not a claim about future platform refusal rates.

## ChatGPT metadata refresh

The initial fresh ChatGPT test still exposed cached, pre-upgrade tool schemas.
Each of the four existing connections was refreshed through its Settings >
Plugins management page, without changing the approval setting or OAuth link.
The visible schemas then included `argv`, `tty`, `close_stdin`, stream selection
and `terminal_sessions action=capabilities`.

A new ChatGPT conversation directly queried capabilities on all four devices.
It then successfully ran `tty=false` command sessions on Mac, Pi5 and WSL,
read separate stdout and stderr, observed exit code 7 and closed its sessions.
Windows correctly reported non-interactive pipes and graceful termination as
unsupported while retaining resize support. Actual UI tool details corroborated
the arguments and returned output, rather than relying only on the model's
summary. Old conversations may keep the definitions loaded before refresh.

This follows the [official MCP metadata refresh workflow](https://developers.openai.com/plugins/deploy/connect-chatgpt).

## Dashboard and remaining boundaries

Dashboard Worker version `eb25ffbb-7c88-4c04-b72f-1d9a3556658e`, deployment
`60d78258-69e1-47ce-aeac-e97452fedba6`, publishes client asset
`index-C4V5kihk.js`. The update includes the bounded-file reassembly required
by the new helpers. Remote version metadata confirms unchanged Access values,
D1 binding, Durable Object namespace, Worker code etag and secret-binding set.
The protected deployment record was backed up and only version/deployment
bookkeeping changed; enrollment remains disabled. All four relays reconnected
and reported fresh heartbeats.

After the owner signed in, authenticated Dashboard follow-up verified:

- All four devices remained unlocked and relay-connected after a full reload.
- Each device's Terminal panel created a new session, displayed the intended
  marker, and its Files panel read back the correct isolated test content.
- The Mac Files editor reassembled a 180,026-byte Chinese fixture exactly;
  five consecutive repeat reads passed. Saving the appended marker produced
  exactly 180,032 bytes, independently verified on the host.
- Mac automatic-mode live video and Windows private-relay-only live video
  displayed their actual primary desktops with the 30 FPS setting selected.
- Real live pointer and keyboard actions focused isolated text editors.
  Chinese text reached the Mac editor exactly; Windows remote Ctrl+S saved
  the exact 31-byte test string, independently verified through the host API.
- Both live sessions were stopped and all four newly created terminal
  sessions were closed. Test documents were retained; existing work and
  credentials were not changed.
- The matching Dashboard suite passed again: 247 tests, one platform-specific
  skip, successful client build and Worker dry-run. No application code,
  deployed artifacts, or service configuration changed in this follow-up.

Initial reads intermittently received zero-byte relay bodies despite HTTP 200
and successful host-side read events. The UI rejected these responses and
disabled Save rather than exposing partial/empty editable content. No payload
corruption was found; subsequent complete reads and saved content were exact.
The transient cause could not be confirmed, and this observation is not
recorded as a fixed bug or a guarantee of uninterrupted transport.

Windows local stdio acceptance needs an administrator runner because the
installed audit file is protected. The test still exercised both owner and
administrator helper identities; public owner MCP operations passed without
changing those permissions. Windows pipe mode and graceful termination remain
explicitly unsupported. Pi5/WSL headless desktop limitations are unchanged.
