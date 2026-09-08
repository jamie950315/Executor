# Live desktop

The Dashboard's **Computer Use → Live desktop** mode receives continuous H264
video over a direct, encrypted WebRTC connection. It does not poll screenshots.
The existing **Snapshot tools** mode and model-facing MCP tools remain separate.

## First supported targets

- macOS with a signed native Desktop helper and existing Screen Recording and
  Accessibility/Input Control grants. Capture uses AVFoundation and encoding uses
  VideoToolbox; no software-encoder fallback is selected silently.
- Windows 10/11 with an active unlocked Default desktop. Capture uses FFmpeg
  gdigrab with physical primary-display geometry, and input uses native Win32.
- Linux/WSL retain snapshot/terminal tools; live video is not enabled there yet.

FFmpeg must be separately installed and discoverable by the Desktop helper.
`EXECUTOR_FFMPEG_PATH` can specify its executable; standard macOS installation
paths are also checked. Windows requires a build with gdigrab and libx264. No
FFmpeg executable is bundled or installed automatically.

Dashboard video defaults to proportional 1280x720, 30 FPS and 4 Mbps, with no audio.
The Frame rate selector offers 15 FPS / 2.5 Mbps for lower load. Stop the current
session before changing quality; it never silently changes an active session.
The native backend is bounded to H264 baseline level 3.1 and at most 30 FPS.
60 FPS is not offered or verified. Actual frame rate and latency depend on the
host and network.

## Owner flow

1. Sign in and unlock the device as before.
2. Open Live desktop, select 15 or 30 FPS, and start a session. It starts in viewing-only mode.
3. After video has actually decoded, enable control and focus the video to send
   pointer and keyboard input. The Text/IME field sends only explicitly submitted
   text; clipboard contents are never read automatically.
4. Escape or losing window focus releases control. Stop session closes the video
   and peer. Fullscreen is available when permitted by the browser; browser/OS
   reserved shortcuts may remain local.

Only one live session is allowed per host. No automatic reconnection, input
replay, audio capture, clipboard sharing, or paid relay provisioning occurs.

## Connectivity and costs

The existing authenticated Dashboard relay carries only signaling and periodic
lease renewal. Video and input use the direct WebRTC peer connection. The default
Cloudflare STUN endpoint discovers possible direct paths; no TURN relay is
configured. If NAT or firewall rules prevent a direct path, the UI reports failure.
Adding a relay or changing firewall policy requires a separate owner decision.
Existing signaling requests and local network/video traffic still consume normal
service or network usage; this is not a promise of zero usage charges.

## Safety boundaries

- Peer signaling, renewals and stop requests use the existing Access identity and
  browser-bound device grant. Session IDs are random and scoped to that owner.
- The lease expires without renewal, independently of browser cleanup. Peer
  disconnect, host revocation/disable, lock and geometry changes stop the session.
- Control is off by default. An ordered data channel uses monotonic sequence IDs,
  bounded messages/queues, normalized coordinates, and validated input types.
  Idle control drops back to viewing-only; stalled video stops client control.
- Held keys/buttons are released on stopping. Snapshot captures carry a helper
  epoch, so live input invalidates previous snapshots across transports. Snapshot
  mutation is excluded while live control owns the desktop.
- Media, SDP and typed input are not persisted or logged. The normal relay audit
  records signaling-tool metadata, not media or individual input payloads.

## Verification

`go test -race ./internal/livedesktop ./internal/desktop ./internal/dispatch`
covers real loopback RTP/data channels, leases, ownership, revocation, held input,
native input layouts, cursor bounds and epochs. `npm test`, `npm run check` and
`npm run build` validate the browser control and Worker integration.

`EXECUTOR_TEST_LIVE_CAPTURE=1 go test ./internal/desktop -run '^TestLiveCaptureOptIn$'`
explicitly captures and discards 31 actual encoded samples and checks their pacing, without recording a
video file. This is an opt-in live-host test, not a substitute for browser decoding
and actual interaction checks. Deployment verification is recorded in AGENTS.md.

### Installed verification, 2026-09-09

Mac runs signed source revision `88d54d8`; Windows runs `44d3a09`, with matching
installed hashes and preserved credentials. Both native capture tests produce
31 samples in about two seconds. The output FPS filter prevents AVFoundation's
input time base from creating excessive output frames; x264's native repeated
headers/AUDs must not be duplicated with the macOS bitstream filters.

Chrome gathered usable host/reflexive ICE candidates in 85ms while other probes
remained pending beyond ten seconds. The eight-second collection budget now
uses already-gathered candidates rather than discarding them; no-candidate
timeout still fails. Actual ICE checks determine connectivity, not gathering
completion. Browser-tool isolated evaluation is not proof of page-global APIs.

Mac Chrome decoded 1108x720 video: 614 frames over 40.9 seconds with one dropped
frame. The Codex in-app browser also displays freshly decoded Mac video.
Explicit control enable, Escape release and Stop were verified; the encoder
exits after Stop. Actual live pointer/typing on Mac has not yet been verified.

Windows connectivity was blocked by existing inbound UDP/TCP Block rules for
the installed Executor. The owner subsequently approved Dashboard-based access
from other devices rather than a Mac/IP allowlist. The installed program now has
an inbound UDP Allow rule on all profiles/interfaces with no local/remote address
restriction. The old UDP Block and Mac-only Allow rules are disabled and retained
for rollback; TCP blocking and system-wide firewall policy are unchanged.
This permits network packets to reach Executor, not unauthenticated desktop
access. Every browser must sign in and unlock the target device; grant validation
binds device, Access subject, browser identity, generation and expiry. Live peer
signaling and renewal are authorized, sessions are leased and encrypted, and
unknown peers cannot turn network reachability into desktop control. Worker
tests reject missing or another browser's grant for all four live session actions.
Connectivity still depends on the networks involved; firewall permission alone
does not guarantee a direct path across NATs.

After that change, Chrome decoded 1280x720 Windows video at ~15 FPS (387 frames
over 25.7 seconds, no drops). Actual live click, ASCII keys, Control+A key events,
explicit Chinese text, drag and wheel were verified in an isolated Windows
fixture, with visible results and fixture event output. The in-app browser also
decoded Windows video and delivered Chinese text. Escape releases input; leaving
the tab releases control, and stalled background video stops the session.
Stopping and starting a new session works. No paid relay was added. Temporary
fixtures and capture sessions were stopped; no encoder process remained.

Startup failures distinguish browser initialization, network discovery, device
start, response validation and answer acceptance; raw SDP is never displayed.

### 30 FPS validation

Dashboard source `798c066` is deployed as Worker
`3009a7ba-2788-4e54-a167-9ca03c5d90c4`; host binaries did not need replacement.
Mac Chrome decoded 3,003 frames in 100.1 seconds with zero dropped frames at
1108x720. Windows decoded 2,621 frames in 87.4 seconds at 1280x720, with 78 dropped
frames (~3%); explicit text input remained correct. Windows encoder CPU used
2.91 CPU-seconds over a 5.01-second sample on 16 logical processors (~3.6% total),
with a 106 MB working set. These are desktop-fixture measurements, not gaming
or arbitrary-network performance guarantees. Mac's encoder sampled 44.7% of one
CPU core and 137 MB RSS. The lower-load 15 FPS option remains available.

### Existing A1 relay feasibility

The owner approved checking A1-JP/A1-US for self-hosted relay use without adding
paid services or disrupting existing workloads. Both ARM hosts have ample spare
CPU/RAM. Their current DERP service owns TCP 80/443 and UDP 3478; DERP is not a
browser WebRTC TURN server, and those listeners were not replaced or moved.

Bounded tests temporarily allowed a separate TCP/UDP 5349 listener through each
host's INPUT rules, then automatically removed the rules. Neither public 5349
listener received the Mac's probe; public STUN 3478 also timed out, while existing
TCP 443 was reachable. Public network/cloud ingress needs investigation before
deployment: local listener startup alone is not proof of Internet reachability.
No standard OCI CLI/auth configuration was available on the Mac or A1-JP to
inspect cloud policy. No coturn service, paid relay, TLS-port replacement or
permanent A1 firewall change was installed. Existing DERP, DNS and app services
were rechecked as active; test listeners were stopped. A future implementation
must use authenticated short-lived TURN credentials, restricted peer ranges and
resource limits, and prove real browser relay traffic before claiming support.
