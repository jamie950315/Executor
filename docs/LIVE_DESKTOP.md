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

Default video is capped at proportional 1280x720, 15 FPS, 2.5 Mbps, with no audio.
The native backend is bounded to H264 baseline level 3.1 and at most 30 FPS.
Actual frame rate and latency depend on the host and network.

## Owner flow

1. Sign in and unlock the device as before.
2. Open Live desktop and start a session. It starts in viewing-only mode.
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

Windows signaling exchanges valid LAN/reflexive candidates, but peer connectivity
never advances beyond checking. Existing enabled inbound Block rules target the
installed Executor for both UDP and TCP on Private/Public profiles. They were
not modified. Windows browser decoding and live pointer/keyboard verification
remain blocked pending an owner-approved firewall change. No paid relay was added.
All temporary fixtures, capture sessions and file-transfer listeners were stopped.

Startup failures distinguish browser initialization, network discovery, device
start, response validation and answer acceptance; raw SDP is never displayed.
