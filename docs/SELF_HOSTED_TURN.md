# Self-hosted desktop relay

Executor supports an optional user-owned TURN relay for live desktop sessions.
The Dashboard's existing Access login and per-browser device unlock remain the
only user authentication steps. No relay password is entered in the browser.

## Credential boundary

The Desktop helper reads `live-turn.json` from its installed state directory:
`/var/lib/executor` on Unix, or `%ProgramData%\Executor` on Windows. This file is
outside the checkout and release payload. Never commit it or a real coturn
configuration. Both filenames are ignored by Git as an additional safeguard.

Protected-file shape (the value below is a placeholder, not a working secret):

```json
{
  "urls": [
    "turn:relay.example.com:5349?transport=udp",
    "turn:relay.example.com:5349?transport=tcp"
  ],
  "secret": "REPLACE_WITH_A_SECURELY_GENERATED_SHARED_SECRET"
}
```

The secret must match the private coturn `static-auth-secret`. Use a random
256-bit or stronger value. Transfer it only over an authenticated encrypted
channel; do not pass the plaintext in command arguments, browser scripts, tool
transcripts, source files or logs. Unix requires a regular non-symlink file with
mode 0600 (or stricter), owned by the Desktop user. Windows requires an explicit
ACL granting only SYSTEM/Administrators full control and the Desktop task user
read access. Provision that ACL before writing the secret.

Missing configuration leaves direct mode unchanged. Invalid or publicly readable
Unix configuration fails helper startup with a credential-free error. Restart
the Desktop helper after intentionally changing this configuration. Existing
recovery, OAuth, IPC and device keys do not need rotation.

`desktop_live` action `status` exposes only whether a relay is configured. On
explicit Start, action `ice` issues fresh HMAC-SHA1 TURN REST credentials with a
random username suffix and a one-hour expiry. The browser never receives the
long-term key. The native peer independently obtains temporary credentials.
Every action, including `ice`, passes the existing browser-bound device grant
checks. The configuration type excludes the long-term key from ordinary JSON
and formatted diagnostics.

The relay checks temporary credentials independently. An already-issued relay
ticket can remain usable for its remaining lifetime after Dashboard revocation;
that does **not** preserve access to the desktop, whose lease and revocation
checks remain enforced by Executor. A long-running relayed session may need to
be restarted when its temporary relay authorization expires; restarting obtains
fresh credentials without an additional login while the unlock remains valid.

## Server requirements

Use coturn with `use-auth-secret`, an explicit realm, no anonymous allocation,
bounded relay ports, per-allocation bandwidth and total allocation limits.
Disable its administrative CLI, TCP peer relaying, unnecessary STUN responses,
and persistent traffic logs. Deny loopback, private, link-local/metadata, CGNAT
and multicast peer addresses so a ticket cannot reach internal host services.

The deployed A1-US and A1-JP services are isolated from their existing DERP/DNS/app services:

- Dedicated OCI network security groups `executor-turn-a1-us` and
  `executor-turn-a1-jp`, each attached only to its own instance's primary VNIC.
  Stateful ingress permits TCP/UDP 5349 and UDP
  49160–49200. Existing TCP 443 and UDP 3478 are not replaced or moved.
- Dedicated `executor-turn.service`, using a pinned coturn container with host
  networking, unprivileged UID, read-only filesystem, no-new-privileges, a small
  tmpfs and only the binary-required NET_BIND_SERVICE capability.
- Container limits: 0.5 CPU, 128 MiB memory, 64 processes. Coturn limits:
  `user-quota=4`, `total-quota=16`, `max-bps=1000000`,
  `bps-capacity=8000000`, allocation lifetime 600 seconds. Capacity accounts
  for candidate allocations, not just the ultimately selected video path;
  configuring capacity for only two allocations rejected normal negotiation.
- Private state under `/etc/executor-turn`. The systemd service adds/removes
  only its three comment-tagged host INPUT rules. No Docker traffic logs or
  media/credential logs are retained.

Current deployment uses TURN over UDP/TCP, not TURN-over-TLS. Desktop media and
input remain end-to-end encrypted by WebRTC. Networks that allow only TLS on
port 443 may still need a separately planned TURN-over-TLS listener; do not
take over an existing service's port or enable a paid product automatically.

No cloud instance, subscription, or paid relay was purchased. The service uses
the existing host's CPU and traffic allowance; rate limits are not a monetary
spending cap and operators must retain their normal provider usage controls.

## Verification and recovery

A1-JP (Osaka) uses the same pinned service version and limits as A1-US. The two
owned relay nodes share the protected relay-cluster secret; it was transferred
only through encrypted SSH streams and is not present in source or logs. Mac
and Windows now list JP UDP/TCP and US UDP/TCP endpoints in their private
configuration, with the existing secret preserved. No binary or Dashboard code
change was required for the second region. Endpoint order does not guarantee a
specific selected region; ICE selects a working path.
The private configuration supports at most four TURN URLs: the current setup
uses one UDP and one TCP URL for each region.

JP verification includes external UDP/TCP synthetic video, allocation capacity,
expired-ticket rejection, and a real browser session restricted to the JP relay.
The browser confirmed the JP relay candidate, decoded 1280x720 video in 30 FPS
mode without lost packets in the sample, and delivered Chinese text to an
isolated Windows fixture. Both nodes' existing services remained active.

When reloading Windows Desktop configuration, wait for the old managed Desktop
process to exit before starting its Scheduled Task again; Stop is asynchronous.
Verify the pipe/doctor status afterwards. Protected pre-JP client configuration
backups are kept under each state directory's `deployment-backups/turn-before-jp`.

Deployment is verified on native revision `102a7d1` (Mac/Windows) and Dashboard
Worker `9efdc9ee-0f53-442f-a830-a02a5b2e9ef6` (UI `4d128a6`). Existing config,
recovery, OAuth and IPC credentials are unchanged. The in-app browser verified
Windows relay-only video, click, ASCII/Chinese input, drag and scroll. A separate
TCP-only check reported `relay` candidates on both ends, local relay protocol
`tcp`, 30 FPS, 2,034 decoded frames and no lost RTP packets in the sample.
Mac relay-only video delivered 4,229 frames in 141.2 seconds; automatic mode
selected host-to-host direct connectivity. Test sessions and diagnostic hooks
were removed after verification. Background-tab presentation drops must not be
confused with network packet loss.

The UI defaults to Automatic (direct preferred), with an explicit Private relay
only mode for checking a relay path. Successful negotiation alone is not proof
of usable desktop video: verify decoded frames, input delivery, and Stop cleanup.

Run ordinary Go/UI/Worker tests first. With owner approval to use the deployed
relay, the following opt-in tests use synthetic media, not screen capture:

```sh
EXECUTOR_TEST_TURN_CONFIG=/var/lib/executor/live-turn.json \
  go test ./internal/livedesktop -run '^TestTURNExternal' -count=1 -v
```

Run these tests with live sessions stopped: they reserve multiple allocations.
They verify temporary credentials, expired-ticket rejection, candidate allocation
capacity, and real RTP through forced UDP/TCP relay paths. Do not print the file
or enable Pion/coturn verbose logs to diagnose failures.

To disable this optional feature, stop live sessions, move the private config
to a protected backup location, and restart only the affected helpers. Stop the
dedicated relay service when unused; its host rules are removed by ExecStopPost.
Keep existing desktop credentials, DERP, DNS and unrelated firewall rules intact.
