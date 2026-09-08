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
explicitly captures and discards three actual encoded samples, without recording a
video file. This is an opt-in live-host test, not a substitute for browser decoding
and actual interaction checks. Deployment verification is recorded in AGENTS.md.

### Installed verification, 2026-09-08

Mac and Windows run source revision `b449af6`, with matching installed hashes,
healthy services and preserved credentials. Both produced real encoded samples
in opt-in native capture checks. The deployed Dashboard preserves all four
device grants and relay connectivity; Pi5 explicitly displays the Linux live
video limitation.

The Codex in-app browser currently times out during browser ICE gathering,
before dispatching the device start request. The same Mac receives a valid
Cloudflare STUN UDP response, so a general host-network outage is not established.
Browser-tool isolated evaluation is not proof of page-global API availability.
Actual browser video decoding and live input are **not yet verified**. Compare
with a separately authenticated Chrome/Edge session before attributing the
failure to the host, changing firewall policy or adding a paid relay.

Startup failures distinguish browser initialization, network discovery, device
start, response validation and answer acceptance; raw SDP is never displayed.
