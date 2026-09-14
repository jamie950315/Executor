# Main relay-reliability rollout — 2026-09-15

## Delivered version and scope

`codex/beta-finalization` was merged into `main` at
`86e01dfff559b40de986c282da13ba71f7864b91`. All four existing production
installations now run binaries built from that clean merge commit
(`vcs.modified=false`). The only merge conflicts were documentation;
Dashboard, MCP and relay-client source matches the verified Beta branch.
Later documentation-only commits do not change the installed program.

This rollout updated production Mac, Pi5, Windows CTPS and Debian WSL, plus
the central Dashboard. The separate Mac Beta and Fetch Proxy installations,
shared Cloudflare tunnels, hostnames, enrollment and TURN configuration were
not replaced. No Setup, Kill, Resume or credential rotation was performed.
No Git push or release publication was requested or performed.

| Target | Installed executor SHA-256 |
| --- | --- |
| Mac arm64 | `690af2fee2081641d98bcb3ed11bcc6e98131149c1c98d206a347dc03c86b06d` |
| Pi5 arm64 | `d92fa5f71320fdd28ed2a4c20f98a6f183c5cbc0a2006e73e203b1f0c791113f` |
| WSL amd64 | `b1024ccad1832234131fca17bb6862c3bf4f6be03fb0a8c394b075a7515b6745` |
| Windows amd64 | `1296bc300feb4d9710644fe94a76887db3f3b8bc8ca41ab7673e227ef219bc57` |

Mac Desktop retains identifier `dev.0ruka.executor.desktop`, signing team
`N3JN8G9YWK` and the prior Apple Development signing identity. Its installed
executable hash is `0bd1ecf9b24325c2f159484d91e894f501ac560438aaf6fb4afbf3ba35a4236c`.
Windows service identities, Desktop task, firewall and existing exact-file
Defender exception remain unchanged; no new exclusion was created.

## Central Dashboard

- Hostname: `https://executor-dashboard.0ruka.dev`.
- Worker version: `9aa485e0-b1de-404c-afc2-76fbb3240fb3`.
- Deployment: `64b13afc-c379-4ee7-8b10-c1a95ebdb824`.
- Client asset: `index-D6IIi73N.js`.
- D1 remains `c7e4899e-f88b-4667-a4ec-4915fe4c35cc`; DO namespace remains
  `6585258fdd684aaa94a5ce8b0bda166f`.
- Access variables, secret bindings, observability settings and disabled
  enrollment were verified unchanged. No database migration was needed.

The protected deployment record at
`/Users/jamie/.local/state/executor/dashboard/deployment.json` was updated only
for current deployment/version identity and owned history. Its prior copy is
`deployment.before-main-86e01df-20260915.json` in the same private directory.

## Verification

- Full Go suite, focused relay/daemon/MCP race tests and `go vet ./...` passed.
- Both executables cross-built for Darwin/Linux/Windows on amd64 and arm64
  with Go 1.24.3. Darwin builds include native CGO; the Mac Desktop app passed
  strict signature verification.
- Dashboard passed 303 tests with one existing skip: 163 unit, 76 UI, 64
  Worker. TypeScript, ESLint, client build and Worker dry-run passed.
- All 28 Beta-maintenance Python tests passed after integration.
- Six compiled test packages ran on each real Pi5, WSL and Windows target:
  relayclient, relay, IPC, filesystem, config and terminal.
- Each installed host passed `doctor --full`, owner/admin file pagination and
  exact Unicode reassembly, missing-cwd errors and real terminal completion.
  Mac/Linux verified pipe EOF, separate streams, exit 7, SIGTERM exit 42 and
  PTY resize; Windows verified ConPTY exit 7 and explicit pipe-mode rejection.
- Existing authenticated public MCP connections on all four devices completed
  owner writes and exact administrator reads of a 44-byte disposable fixture.
  All fixtures were deleted through their original connections.
- Public OAuth metadata retained four distinct expected issuers and each
  unauthenticated `/mcp` endpoint returned 401.
- Public Mac and Windows screenshot calls returned actual PNG images at
  1512×982 and 2560×1440 respectively. Image bytes were not saved.
- The real Access-authenticated production Dashboard loaded the new asset,
  showed all four devices unlocked with live relays, reassembled a 212,000-byte
  Unicode file exactly and successfully read a zero-byte file. These fixtures
  were then removed. No production fault injection was added to this rollout;
  the unchanged failure boundary already passed the isolated Beta public test.

The initial build attempt overlapped the packaging test's own npm installation
with a second installation. Re-running those steps serially resolved the build
environment failure without a source change. Target test runners needed the
checked-in wire vectors, executable file modes and literal PowerShell test
arguments; final target runs passed. The pinned npm dependency tree still
reports six existing audit advisories; dependency upgrades were not part of
this deployment.

## Existing ChatGPT connections can remain

Before and after upgrading, all four real MCP `tools/list` results contained
nine tools with identical canonical metadata SHA-256:
`be083487359add300f1c2e9656e19afaa272907702ab04a484d0b9d7c29ac489`.
Tool names, descriptions, input/output schemas and annotations did not change;
neither did MCP authentication or MCP UI resources. The central Dashboard's
external web assets are not MCP UI resources.

ChatGPT.com continued to list Executor Mac, Pi5, Windows and WSL as installed.
Their existing authenticated connections were exercised successfully after
the upgrade. No Refresh, reconnect, approval-setting change or new OAuth grant
is needed for this rollout. Reloading an already-open Dashboard page loads its
new frontend. This follows the scope of the official
[metadata refresh workflow](https://developers.openai.com/plugins/deploy/connect-chatgpt).
No new ChatGPT conversation was created for this check.

Production config, secret-store, OAuth-state and TURN hashes remained unchanged
in the before/after checks. The separate Beta OAuth state changed during the
Mac administrator upgrade channel's normal token refresh: audit metadata shows
the refresh attempt and success at `2026-09-14T16:51:46Z`; Beta generation 2,
config and secret-store hashes remained unchanged. This was not a Recovery Key
or IPC credential rotation.

## Rollback and cleanup

Each host retains previous binaries under
`deployment-backups/main-86e01df-20260915` within its state directory:
`/var/lib/executor` on Mac/Linux/WSL and `C:\ProgramData\Executor` on Windows.
Mac also retains its previous signed Desktop app. Upgrade runners included
failure rollback; no rollback was needed in the successful rollout. A future
rollback must stop only the validated production components, restore those
backups, preserve credentials and verify readiness before reconnecting traffic.
The previous central Worker version is
`eb25ffbb-7c88-4c04-b72f-1d9a3556658e`; use only the production Worker and its
recorded ownership when reverting it, never the separate Beta resources.

No Executor terminal jobs were running at upgrade time. Disposable acceptance
sessions and files were cleaned up, the Mac Beta administrator session was
closed, and the one-time Windows upgrade task unregistered itself. Shared
cloudflared PIDs remained unchanged (Pi5 1065, WSL 196, Windows 5348; Mac's
inventory also preserved its shared tunnel). Build/staging bundles and
metadata-only evidence remain available for recovery; they are not running
services and were not committed.

While final documentation was being prepared, unrelated work switched the
primary checkout to `tunnel` and added uncommitted tunnel scripts. That checkout
was preserved. Main's rollout documentation was committed from the separate
`/Users/jamie/Executor/.worktrees/main-rollout-record` worktree.
