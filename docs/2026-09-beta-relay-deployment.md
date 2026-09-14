# Isolated Mac Beta relay-validation deployment — 2026-09-14

## Installed revision and isolation

The existing, independent Mac Beta installation now runs `fix/relay-result-validation` source `ec5f53d4e3ec08df564f59a29eba33ef82396f5e`, including the result-validation correction in `6ab4903`. It was built from a clean standalone local clone; build metadata reports `vcs.modified=false` and native darwin/arm64 CGO support.

- Suggested ChatGPT connection name: `BetaExecutorMac`.
- MCP URL: `https://beta-executor-mac.0ruka.dev/mcp`.
- Installation root: `/Users/jamie/Library/Application Support/Executor Beta`.
- Active bundle: `bundle-ec5f53d`; previous `bundle-8dadd7e` remains intact.
- Executable SHA-256: `54be589ee7d1b9b2f78734fdf21cdced7e7a585338dae184cc771680856e4242`.
- Signing identity retains team `N3JN8G9YWK` and bundle identifier `dev.0ruka.executor.desktop`; strict signature verification passed.
- Beta agent listens at `127.0.0.1:18787`. Its state directory, OAuth/recovery/IPC credentials and endpoint configuration were preserved byte-for-byte.

Only the three existing `com.executor.beta.agent`, `com.executor.beta.broker` and `com.executor.beta.desktop` services were unloaded and reloaded with the new bundle paths. The scoped stop helper was updated to validate those paths. There were no active Beta child processes before the switch, and readiness checks verified all three components after startup.

The production Agent, Broker, Desktop, Dashboard and cloudflared process IDs remained unchanged through deployment and acceptance. Production binaries, service plists, configuration and secret-store hashes also remained unchanged. Existing Cloudflare DNS and tunnel ingress were read and verified; no Cloudflare API mutations were needed. Fetch Proxy, the other devices, `main`, remote branches and releases were preserved.

## Acceptance performed on the installed Beta

The functional checks used the documented local `stdio --config <Beta config>` interface of the installed binary, dispatching to the running Beta owner/admin helpers. They did not use production helper endpoints.

| Check | Observed result |
| --- | --- |
| MCP tool discovery | Nine tools |
| Chinese multi-page file | 210,000 bytes, 100 complete reads, 400 page requests, every SHA-256 identical |
| Empty file | Zero returned bytes and a valid EOF result |
| Owner and admin file operations | Write byte counts and exact six-byte slices passed |
| Owner and admin pipe terminals | Separate stdout/stderr matched, exit code 7, created sessions closed |
| Desktop prerequisites | Screen Recording, Accessibility and Input Control remained granted |
| Beta and production public HTTPS metadata | HTTP 200, each with its correct distinct issuer/resource |
| Unauthenticated public MCP | HTTP 401 on both hosts |
| Beta-only stop helper | `--check` passed |
| Post-acceptance preservation | Production processes/files and Beta configuration/credentials still matched the pre-deployment snapshot |

Temporary acceptance files and the test-created Beta terminal sessions were cleaned up. A combined inspection command was blocked by the platform before the switch; it produced no execution result. The initial deployment preflight then discovered that production's Desktop LaunchAgent is under `/Library/LaunchAgents`; the guard was corrected to that observed path and passed before any service changes.

## Safe management and rollback

The existing production lifecycle entrypoints retain production service labels. Use the Beta-scoped helpers for this instance.

```sh
# Validate Beta-only stop targets without stopping services.
python3 '/Users/jamie/Library/Application Support/Executor Beta/stop.py' --check

# Stop only Beta when explicitly requested.
sudo python3 '/Users/jamie/Library/Application Support/Executor Beta/stop.py'
```

The one-time switch helper includes a narrowly scoped rollback to the retained previous Beta bundle. The backup contains the original Beta service plists and stop helper plus credential-free integrity metadata:

```text
/Users/jamie/Library/Application Support/Executor Beta/deployment-backups/relay-ec5f53d-20260914
```

```sh
# Explicit rollback command; it requires the upgraded Beta to be idle.
sudo python3 '/Users/jamie/Library/Application Support/Executor Beta/upgrade-relay-ec5f53d.py' --rollback
```

The rollback path was prepared but was not invoked during this successful update. The deployed Beta uses its existing recovery key; this deployment generated no new key and performed no credential rotation.

## Remaining coverage

Beta retains its existing unenrolled central-Dashboard state; its optional local Dashboard service is not running. The shared production tunnel supplies its HTTPS route, so the Beta CLI's own tunnel status remains `not-configured` while the public endpoint is reachable.

The 100-read result establishes installed local MCP/helper behavior. An authenticated public ChatGPT connection and central Dashboard -> relay -> Beta end-to-end acceptance remain separate follow-up coverage. The original intermittent empty HTTP body still lacks a production request trace proving its cause. The result-validation fix and its earlier native regression/race/Worker tests remain documented in `2026-09-relay-result-validation.md`.

## Owner-approved Beta recovery reset

At 2026-09-14T20:14:30+08:00, the owner explicitly approved a Beta-only reset after losing the prior Beta recovery key. Native secret-store rotation advanced the Beta credential generation to 2; persisted OAuth code and refresh grants were revoked while client registrations and the relay identity were preserved. Only the three exact Beta services were restarted. The new recovery key was delivered exclusively in the requesting private conversation; its plaintext is excluded from this document and repository.

Public HTTPS verification passed new-key consent, PKCE authorization-code exchange, refresh exchange, nine-tool discovery, and authenticated owner/admin helper capability checks. All three Beta components were online. Production process IDs and protected-file hashes, along with the Beta domain and configuration, matched their pre-reset snapshots. The earlier credential-preservation statements describe deployment history; the approved reset in this section supersedes them for the current Beta credential generation.
