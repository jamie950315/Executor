# MCP result and pagination contracts

These changes belong to the development branch `pro-fix`. Installed services,
OAuth credentials, recovery keys, Tunnels, and the production Dashboard remain
unchanged. Authenticated owner/admin capabilities retain their existing scope.

## File reads and writes

`filesystem_read` / `read_file` interprets `offset` and `limit` as raw bytes.
The default page is 65,536 bytes; the maximum is 1,048,576 bytes. Offsets are
non-negative integers and `offset + limit` must remain at or below
9,007,199,254,740,991 for exact JSON pagination. Invalid types, fractions, and
out-of-range values fail before helper execution.

Every file page returns `content`, `encoding`, `size`, `offsetBytes`,
`returnedBytes`, `nextOffsetBytes`, `truncated`, and `eof`. `size` is the number
of raw bytes in this page. Continue at `nextOffsetBytes` while `truncated` is
true. EOF is observed with at most one lookahead byte. File size metadata is
not used to infer EOF, allowing reads of seekable virtual files such as procfs.
Each page observes the current file; pagination provides no immutable snapshot
or protection against concurrent external modification.

Both omitted encoding and `encoding=utf8` require valid UTF-8. A page that cuts
a multibyte character returns an explicit error with base64 guidance. Use
`encoding=base64` to preserve arbitrary byte ranges and decode after collecting
the required bytes. Legacy internal `filesystem.read` retains its full-read
behavior; public bounded reads use `filesystem.read-range`. A helper missing
the range interface returns an upgrade error rather than allocating a whole
file as a fallback.

Successful `write_file` and `append_file` calls consistently return
`{ok, action, encoding, size, writtenBytes}`. Byte counts describe the supplied
decoded content. `mkdir`, `move`, and `delete` return `{ok, action}`. Errors
remain errors. Base64 is validated strictly before writes.

For `mkdir` and `delete`, explicit `recursive=false` affects only the named
item: missing parents and nonempty directories respectively produce errors.
Omitted `recursive` and explicit `true` retain the legacy recursive behavior.
Non-recursive operations use dedicated helper methods and never fall back to
recursive operations. Invalid privilege or recursive arguments fail before IPC.

## Terminal input, output, and completion

Follow-up: macOS/Linux now start the executable on a native PTY, so initial dimensions and resize are applied directly and SIGTERM reaches the foreground job without terminating a `script` wrapper. `tty` defaults to true for compatibility. For batch jobs, use `terminal.create` with `argv` and `tty:false`; stdout/stderr are pipes and can be read independently with `terminal_output.stream`. Each stream has its own cursor; combined output preserves observed arrival order, not a total order across streams. Separate retained streams each have a 4 MiB buffer; combined output retains 8 MiB. Use `close_stdin` to deliver EOF without deleting the session. Query `terminal_sessions action=capabilities` for support. Windows retains ConPTY and rejects pipe mode explicitly. Invalid cwd is checked under the selected helper identity and reports `CWD_NOT_FOUND`, `CWD_NOT_DIRECTORY`, or `CWD_ACCESS_DENIED`.

`terminal` / `create` retains the interactive `command` string behavior.
An optional mutually exclusive `argv` array starts the exact executable and
arguments in the existing session mechanism. Shell syntax requires an explicit
shell in that array. No additional execution tool is introduced.

Successful terminal input returns
`{ok:true, inputAccepted:true, completion:"unknown"}`. Acceptance confirms the
write to the session, with command outcome determined separately. Short writes
produce an error; failed writes are not retried automatically.

`terminal_output` uses the same 65,536-byte default and 1,048,576-byte maximum.
The bound is applied in the output buffer through both authenticated helper
paths, before the requested data is copied. `Data` remains base64. Legacy
`StartCursor`, `NextCursor`, `Running`, and `Truncated` remain present alongside
`encoding`, `returnedBytes`, `hasMore`, `mode`, `sessionRunning`,
`commandRunning`, and `exitCode`.

Read subsequent buffered pages at `NextCursor` while `hasMore` is true.
`Truncated` means older requested bytes were evicted from the retained buffer;
ordinary pagination uses `hasMore`. `Running` and `sessionRunning` describe the
session process and final output capture. Interactive sessions report null
`commandRunning` and `exitCode`. Command-mode sessions expose an observed exit
code after final output capture; unknown or unavailable outcomes remain null.
Completed sessions retain their results until closed. The Windows ConPTY
adapter preserves actual nonzero process exits.

## Diagnostic scope and tool side effects

`device_permissions.ready` describes desktop permission prerequisites.
The response adds `scope:"desktop"` and a `capabilities` breakdown. Filesystem
and terminal status are `not_checked`; their actual operations determine
availability for the selected privilege and target. Desktop capture/input
values describe permission prerequisites, including pending restart or missing
checks, rather than guaranteeing the next operation will succeed.

`device_status` / `summary` and `terminals` probe the two helpers independently
within a shared two-second window. Existing component values remain at their
top-level keys. `partial` and `components.<name>.state` retain healthy evidence
alongside `unavailable`, `error`, `timeout`, `cancelled`, or `invalid_response`
states. Diagnostic metadata uses fixed state codes and excludes raw helper
error strings. An already canceled request makes no helper calls.

Mutating tools explicitly serialize `readOnlyHint:false`. `desktop_observe`
is annotated as mutating because screenshot export with a path and
`includeImage=false` can create or overwrite a file. Its observation and
screenshot capabilities remain available. Actual tool failures retain readable
MCP `isError` results over HTTP and stdio, without fabricated success output.

## Dashboard compatibility

The file editor and binary download collect complete base64 pages before
decoding. This preserves characters split across page boundaries and the UTF-8
BOM. Pagination metadata, byte counts, canonical base64, progress, and
cancellation are validated. A failed later page yields no partial editable or
downloadable result. Legacy whole-file responses remain supported.

Browser-side aggregation has a 32 MiB memory budget; larger reads fail visibly.
This browser limit does not restrict host paths or the MCP byte-range API.
Upgrade the Dashboard together with the helpers before relying on public
bounded reads; older editors can mistake one page for the complete file.

## Verification and limits

Confirmed locally on macOS: the full Go suite, race tests for terminal,
dispatch, desktop, broker, and MCP packages, `go vet ./...`, and `go build ./...`.
Authenticated isolated owner/admin helper tests cover file slicing, EOF,
default limits, write byte counts, non-recursive fixture preservation, and
reassembly of 4,096 terminal bytes from 128 pages with an observed exit code 7.
HTTP and stdio tests preserve partial diagnostic results and readable errors.

The focused Dashboard file-reader and panel checks passed 32 tests; TypeScript
and ESLint checks passed. A subsequent full Dashboard run reported 123 unit
tests passed and one skipped before final-result retrieval was blocked by the
platform. Completion of that run's remaining UI/Worker tests and build is
unconfirmed. Both `executor` and `executor-kill` compiled successfully for
darwin/amd64, darwin/arm64, linux/amd64, linux/arm64, windows/amd64, and
windows/arm64. Build outputs remain in an isolated temporary directory. Native
Windows/Linux execution and production deployment are outside this verification
session.

These changes improve truthful contracts and bounded I/O. Calls rejected by a
client platform before reaching Executor remain outside the server's
visibility. No claim is made about changing platform refusal rates or
Business/Pro safety thresholds; tool side effects stay accurately disclosed.
