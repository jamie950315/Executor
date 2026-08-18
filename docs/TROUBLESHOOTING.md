# Executor Troubleshooting

This page records the verified causes of the OAuth and rotation failures encountered during the first ChatGPT deployments. Diagnose by boundary: Cloudflare edge, Executor OAuth, local state ownership, and browser consent are separate systems.

## Recommended validation order

1. Run `executor status` and confirm the host is `armed`.
2. Run `executor doctor --full`. All local checks and `remote OAuth DCR` should pass.
3. Confirm `GET /.well-known/oauth-protected-resource` and `GET /.well-known/oauth-authorization-server` return JSON through the public hostname.
4. Confirm unauthenticated `/mcp` returns HTTP 401 with `WWW-Authenticate` rather than a Cloudflare HTML challenge.
5. Link the exact hostname in ChatGPT and enter that host's newest recovery key.

## Public DCR returns HTTP 403

**Observed symptom:** ChatGPT could not register an OAuth client. Direct DCR requests returned HTTP 403, and no matching OAuth registration reached Executor.

**Verified cause:** Cloudflare Bot Fight Mode challenged non-browser API traffic before it reached the tunnel origin. This was not an Executor OAuth rejection.

**Correct fix:** First inspect Cloudflare Security > Analytics > Events and identify the service that produced the 403. If the event identifies Bot Fight Mode, disable it for the zone, use Super Bot Fight Mode with a narrowly scoped Skip rule for the Executor hostname and OAuth/MCP paths, or move Executor to a dedicated zone. Free-plan Bot Fight Mode cannot be skipped by WAF custom rules or Page Rules. If the event identifies Access or WAF instead, adjust that matching policy rather than changing Bot Fight Mode. Validate with `executor doctor --full`; the check does not create a client.

## `authorization denied`

**Observed symptom:** The Executor consent page loaded, but submitting it returned `authorization denied`.

**Verified cause:** Executor returns this response only when the submitted recovery key does not match the current verifier. Kill and rotation generate a new key and invalidate the old key immediately. With several hosts, a valid key for one hostname is invalid for every other hostname.

**Correct fix:** Use the newest recovery key produced for that exact hostname. An AI-managed setup or rotation must return the complete new key in the same private owner chat. Store keys in a password manager labeled by hostname; never store them in the repository, logs, issues, or public channels.

## Consent button does not submit on mobile Safari

**Observed symptom:** Pressing Return in the recovery-key field submitted the form, but tapping the styled authorization button did not.

**Verified boundary:** The recovery-key form and server endpoint were working because keyboard submission reached the server. The failed interaction was the button activation path in the observed mobile Safari flow.

**Applied fix:** Executor renders a native `<input type="submit">` control with mobile touch behavior instead of relying on the previous styled `<button>`. The form also preserves the OAuth `resource` parameter.

## Agent logs show permission denied after root rotation

**Observed symptom:** On macOS, a root-initiated Kill or rotation was followed by repeated failures to read `oauth-state.json`; the same ownership model applies to Linux and WSL.

**Verified cause:** Atomic state replacement created the new Unix file with the rotating process's ownership. The normal-user Agent then lost read access.

**Applied fix:** Unix OAuth state replacement preserves the existing owner and group before the atomic rename. macOS, Linux, and WSL use this common path. Windows has no Unix uid/gid transition and keeps state under `%ProgramData%\Executor` with the installed service permissions. Resume verifies that the Agent, Broker, and Desktop loaded the rotated credentials before restarting the tunnel.

## macOS screenshot fails only under LaunchAgent

**Observed symptom:** `desktop_observe` reports `screenshot unavailable`, while `screencapture` works from an interactive Terminal. The Desktop helper log may report `could not create image from display`.

**Verified cause:** Screen Recording is granted by macOS TCC to a stable signed application identity. A standalone ad-hoc helper binary launched from changing build paths does not provide the stable bundle identifier and signing requirement needed for persistent LaunchAgent permission.

**Correct fix:** Install the release's signed `/Library/Application Support/Executor/Executor Desktop.app`, then grant Screen Recording and Accessibility to that app in System Settings > Privacy & Security. Restart the `com.executor.desktop` LaunchAgent after permission changes. Production archives should be signed with a stable `Developer ID Application` identity through `EXECUTOR_MACOS_SIGN_IDENTITY`; ad-hoc builds may require permission to be granted again after upgrades. Do not grant these permissions only to Terminal or to `/usr/local/bin/executor`.

## ChatGPT receives the first screenshot but does not call `desktop_control`

**Observed symptom:** ChatGPT shows the first capture ID or image, then stops without executing the requested batch. Executor's metadata-only audit contains a successful `desktop_observe` but no `desktop_control` attempt.

**Boundary:** No request reached Executor, so this is not a Desktop helper, capture validation, or input failure. `desktop_control` is intentionally declared destructive because the same tool can click and type; Executor must not mislabel it to suppress a client-side approval boundary.

**Correct diagnosis:** Give ChatGPT an explicit instruction naming the exact allowed batch and approve the tool call if the client presents a confirmation. Use the audit log to distinguish a missing client call from an Executor rejection. If there is no `desktop_control` attempt and the client gives a truncated response, retry in a new chat or report the ChatGPT client behavior; do not weaken the tool annotation.

## CIMD flow stops before token exchange

**Observed symptom:** Real ChatGPT web attempts using advertised CIMD completed consent but did not reach a successful token exchange.

**Known boundary:** The exact external callback failure was not proven to be an Executor protocol defect. During diagnosis, Executor added standards-compatible `none` and `private_key_jwt` handling, trusted ChatGPT JWKS verification, and `resource` preservation. Repeated real tests still did not complete through the advertised CIMD path.

**Current compatibility mode:** Executor does not advertise CIMD and directs ChatGPT through DCR, which completed a real authorization-code + PKCE exchange. The CIMD resolver remains implemented for future interoperability testing. Do not re-advertise CIMD based only on unit tests; require a real ChatGPT callback and token-exchange success first.

## Cross-platform build boundary

OAuth, consent, recovery-key validation, state persistence, and remote DCR diagnostics are shared Go code. They are not separate macOS, Windows, Linux, or WSL implementations. Platform-specific service installers only determine how the shared binary is started and where state is stored.

Before publishing a change, run the complete Go tests, race tests, vet, shell syntax checks, and `scripts/build-release-artifacts.sh`. The release script must produce `executor` and `executor-kill` archives for Darwin, Linux, and Windows on both amd64 and arm64. Real host validation remains required for launchd, systemd, Windows Services, ConPTY, WSL, and each public Cloudflare hostname.
