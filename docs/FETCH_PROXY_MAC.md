# Independent Fetch Proxy Mac deployment

The `fetch-proxy` source `4d3e6e8` is installed independently at `https://fetch-proxy-executor-mac.0ruka.dev/mcp`. It exposes exactly `read` and explicitly mutating `fetch`; it does not replace the nine-tool Beta line.

Installation: `/Users/jamie/Library/Application Support/Executor Fetch Proxy/bundle-4d3e6e8`. Independent state: sibling `state/`. Agent port 19787; dashboard port 19788 is reserved but no dashboard runs. Services are `com.executor.fetchproxy.agent`, `.broker`, `.desktop`, with owner agent/desktop and root broker. Recovery material was generated separately and delivered privately; no Beta credentials were copied.

The shared Mac Tunnel has one additional route to `http://127.0.0.1:19787`; production and Beta routes are preserved. DNS record ID: `6074561ecbc5cb92448ec876dc912ea3`. Fetch Proxy DNS was confirmed via public resolver 1.1.1.1; this Mac initially cached a negative response, so acceptance checks optionally resolve the verified Cloudflare edge IP while retaining the real HTTPS hostname/certificate validation.

Use the installation's `stop.py --check` to validate exact service targets and `sudo python3 '/Users/jamie/Library/Application Support/Executor Fetch Proxy/stop.py'` to stop only Fetch Proxy. It preserves state and the shared Tunnel. Do not run the standard lifecycle kill/rotate/resume binaries: their service labels target production.

Beta remains source `8dadd7e`, nine tools, port 18787. Its installed path is `/Users/jamie/Library/Application Support/Executor Beta/bundle-8dadd7e`. Beta was rebuilt from a standalone source checkout after nested-worktree builds produced misleading Git revision metadata despite containing the correct Beta tool surface. Validate both source provenance and actual tools, rather than interpreting the embedded revision alone.

Both bundles are signed. Full Go tests pass for Fetch Proxy; Beta additionally passed the recorded native terminal, race, six-platform build and Dashboard checks. Public acceptance verification status is recorded in AGENTS.md. A transient shared Tunnel disconnect returned 1033 for all hostnames; restarting only the existing cloudflared service restored production connectivity without credential, DNS, or security-policy changes.
