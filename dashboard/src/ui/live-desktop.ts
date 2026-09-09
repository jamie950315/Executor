import type { DeviceCall } from "./panels/types";

export interface LiveStatus { supported: boolean; available: boolean; active: boolean; reason?: string; iceServers: string[]; relayConfigured?: boolean }
interface LiveSession { sessionId: string; answer: string; width: number; height: number; leaseSeconds: number }
export interface Point { x: number; y: number }
type Input = { type: string; x?: number; y?: number; button?: number; code?: string; down?: boolean; text?: string; scrollX?: number; scrollY?: number; enabled?: boolean };

export function liveErrorMessage(code: unknown): string {
 switch(code) {
  case "video_start_failed": return "The device could not start video. Check FFmpeg and screen recording permission.";
  case "video_start_timeout": case "video_failed": case "invalid_video_sample": return "The device stopped producing valid video. The session has stopped.";
  case "session_ended": return "The device was locked, revoked, resized, or its session expired. Start a new session when ready.";
  case "input_release_failed": return "The device could not confirm that every key was released. Control has stopped; check the host.";
  case "input_backlog": return "Input could not keep up. The session stopped instead of replaying delayed actions.";
  case "read_only": return "The session is viewing only. Enable control again before sending input.";
  default: return "The device could not confirm an operation. The session has stopped; nothing was retried.";
 }
}

// Input authority depends on recently decoded video, not merely a connected
// peer or a past loadeddata event. The fallback also counts decoded frames.
export class LiveVideoWatch {
 private lastFrame = performance.now(); private frameID: number | undefined; private closed = false; private decoded = 0;private seenFrame=false;
 private timer: ReturnType<typeof setInterval>;
 constructor(private video: HTMLVideoElement, private fresh: (value: boolean) => void, private stalled: () => void) {
  if (typeof video.requestVideoFrameCallback === "function") this.requestFrame();
  else this.decoded = video.getVideoPlaybackQuality?.().totalVideoFrames ?? 0;
  this.timer = setInterval(() => {
   if (this.closed) return;
   if (typeof video.requestVideoFrameCallback !== "function") { const count = video.getVideoPlaybackQuality?.().totalVideoFrames ?? 0; if (count > this.decoded) { this.decoded = count;this.seenFrame=true; this.lastFrame = performance.now(); this.fresh(true); } }
   if (performance.now() - this.lastFrame > (this.seenFrame ? 3000 : 15000)) { this.close(); this.fresh(false); this.stalled(); }
  }, 250);
 }
 private requestFrame() { this.frameID = this.video.requestVideoFrameCallback(() => { if (this.closed) return; this.seenFrame=true;this.lastFrame = performance.now(); this.fresh(true); this.requestFrame(); }); }
 isFresh(){return !this.closed&&this.seenFrame&&performance.now()-this.lastFrame<=3000;}
 close() { if (this.closed) return; this.closed = true; clearInterval(this.timer); if (this.frameID !== undefined) this.video.cancelVideoFrameCallback?.(this.frameID); }
}

export function containedPoint(rect: Pick<DOMRect, "left" | "top" | "width" | "height">, width: number, height: number, clientX: number, clientY: number): Point | null {
 if (width <= 0 || height <= 0 || rect.width <= 0 || rect.height <= 0) return null;
 const scale = Math.min(rect.width / width, rect.height / height); const w = width * scale; const h = height * scale;
 const x = (clientX - rect.left - (rect.width - w) / 2) / w; const y = (clientY - rect.top - (rect.height - h) / 2) / h;
 return Number.isFinite(x) && Number.isFinite(y) && x >= 0 && y >= 0 && x < 1 && y < 1 ? { x, y } : null;
}

export class LiveInput {
 private seq = 0; private timer: ReturnType<typeof setTimeout> | undefined; private pending: Point | undefined; private closed = false;
 constructor(private dc: RTCDataChannel, private fail: () => void) {}
 send(event: Input): boolean {
  if (this.closed) return false;
  if (this.dc.readyState !== "open" || this.dc.bufferedAmount > 128 * 1024) { this.closed = true; this.discardMove(); this.fail(); return false; }
  try { this.dc.send(JSON.stringify({ ...event, seq: ++this.seq })); return true; } catch { this.closed = true; this.discardMove(); this.fail(); return false; }
 }
 move(point: Point) { if (this.closed) return; this.pending = point; if (!this.timer) this.timer = setTimeout(() => this.flushMove(), 34); }
 flushMove() { const point = this.pending; this.discardMove(); if (point) this.send({ type: "move", ...point }); }
 private discardMove() { clearTimeout(this.timer); this.timer = undefined; this.pending = undefined; }
 release() { this.discardMove(); return this.send({ type: "release" }); }
 close() { this.discardMove(); this.closed = true; }
}

export function gatherICE(pc: RTCPeerConnection, signal: AbortSignal): Promise<boolean> {
 return new Promise((resolve, reject) => {
  const finish = (error?: Error) => { clearTimeout(timer); pc.removeEventListener("icegatheringstatechange", changed); signal.removeEventListener("abort", aborted); if (error) reject(error); else resolve(pc.iceGatheringState === "complete"); };
  const changed = () => { if (pc.iceGatheringState === "complete") finish(); }; const aborted = () => finish(new Error("Connection cancelled"));
  // A slow interface must not discard candidates already gathered on working
  // interfaces. ICE connectivity checks still verify whether these can connect.
  const timer = setTimeout(() => finish(/^a=candidate:/m.test(pc.localDescription?.sdp ?? "") ? undefined : new Error("Direct connection discovery timed out without candidates")), 8000);
  pc.addEventListener("icegatheringstatechange", changed); signal.addEventListener("abort", aborted); if (signal.aborted) aborted(); else changed();
 });
}

export function parseLiveStatus(value: unknown): LiveStatus {
 const s = value as Partial<LiveStatus> | null;
 if (!s || typeof s.supported !== "boolean" || typeof s.available !== "boolean" || !Array.isArray(s.iceServers) || s.iceServers.some((url) => typeof url !== "string" || !/^stuns?:[^@\s]+$/u.test(url))) throw new Error("Invalid live desktop status");
 if (s.relayConfigured !== undefined && typeof s.relayConfigured !== "boolean") throw new Error("Invalid relay status");
 return { supported: s.supported, available: s.available, active: s.active === true, iceServers: s.iceServers, reason: typeof s.reason === "string" ? s.reason : undefined, relayConfigured: s.relayConfigured === true };
}

export function parseConnectivity(value: unknown): RTCIceServer[] {
 const c=value as {iceServers?: unknown;expiresAt?: unknown}|null;
 const now=Date.now()/1000;
 if(!c || typeof c.expiresAt!=="number" || !Number.isSafeInteger(c.expiresAt) || c.expiresAt<now+30 || c.expiresAt>now+3700 || !Array.isArray(c.iceServers) || c.iceServers.length<1 || c.iceServers.length>8) throw new Error("Invalid relay authorization");
 return c.iceServers.map((value:unknown)=>{
  const s=value as {urls?:unknown;username?:unknown;credential?:unknown}|null;
  if(!s||!Array.isArray(s.urls)||s.urls.length<1||s.urls.length>4||s.urls.some(u=>typeof u!=="string"||u.length>512||!/^(?:stun|stuns|turn|turns):[^@\s#]+$/u.test(u)))throw new Error("Invalid relay server");
  const turn=s.urls.some(u=>/^turns?:/u.test(u as string));
  if(turn&&(typeof s.username!=="string"||s.username.length<1||s.username.length>256||typeof s.credential!=="string"||s.credential.length<1||s.credential.length>512))throw new Error("Missing relay authorization");
  return {urls:s.urls as string[],...(turn?{username:s.username as string,credential:s.credential as string}:{})};
 });
}

export interface LiveCallbacks { stream: (stream: MediaStream | null) => void; state: (control: boolean, ready: boolean) => void; stopped: (message: string) => void }
export class LiveDesktopConnection {
 private pc: RTCPeerConnection | undefined; private dc: RTCDataChannel | undefined; input: LiveInput | undefined;
 private abort = new AbortController(); private closed = false; private session: LiveSession | undefined; private lease: ReturnType<typeof setTimeout> | undefined; private startup: ReturnType<typeof setTimeout> | undefined; private ping: ReturnType<typeof setInterval> | undefined;
 constructor(private call: DeviceCall, private callbacks: LiveCallbacks) {}
 async start(status: LiveStatus, fps: 15 | 30 = 15, relayOnly = false) {
  if (this.closed) return;
  if (relayOnly && !status.relayConfigured) { this.stop("No private relay is configured for this device."); return; }
  if (typeof RTCPeerConnection !== "function") {
   this.stop("This browser does not provide WebRTC video connections. Open this Dashboard in a WebRTC-enabled browser such as Chrome or Edge; device permissions cannot fix this browser limitation.");
   return;
  }
  let failure = "The browser could not initialize a video connection. Check browser WebRTC support.";
  try {
   let iceServers:RTCIceServer[]=status.iceServers.map((urls)=>({urls}));
   if(status.relayConfigured){
    failure="Relay authorization could not be obtained. Check the device relay configuration; no connection was retried.";
    const response=await this.call("desktop_live",{action:"ice"},this.abort.signal);
    if(this.closed)return;
    iceServers=parseConnectivity(response.result);
   }
   failure="The browser could not initialize a video connection. Check browser WebRTC support.";
   const pc = new RTCPeerConnection({ iceServers, iceTransportPolicy: relayOnly ? "relay" : "all" }); this.pc = pc;
   pc.addTransceiver("video", { direction: "recvonly" });
   pc.ontrack = (event) => { if (!this.closed) this.callbacks.stream(event.streams[0] ?? new MediaStream([event.track])); };
   pc.onconnectionstatechange = () => { if (["failed", "closed", "disconnected"].includes(pc.connectionState)) this.stop("Desktop connection ended. Start again when the device is reachable."); };
   const dc = pc.createDataChannel("executor-input", { ordered: true }); this.dc = dc;
   this.input = new LiveInput(dc, () => this.stop("Input connection is congested or unavailable. Control has stopped; nothing was retried."));
   dc.onclose = () => this.stop("Control connection ended. The session has stopped."); dc.onerror = () => this.stop("Control connection failed. The session has stopped.");
   dc.onmessage = (event) => {
    if (this.closed) return;
    try {
     if (typeof event.data !== "string" || event.data.length > 8192) throw new Error();
     const message = JSON.parse(event.data) as { type?: string; control?: boolean; code?: unknown };
     if (message.type === "ready" || message.type === "state") { if (typeof message.control !== "boolean") throw new Error(); clearTimeout(this.startup); if (!this.ping) this.ping = setInterval(() => { this.input?.send({ type: "ping" }); }, 1000); this.callbacks.state(message.control, true); }
     else if (message.type === "error") this.stop(liveErrorMessage(message.code));
     else throw new Error();
    } catch { this.stop("Invalid control response. The session has stopped."); }
   };
   await pc.setLocalDescription(await pc.createOffer());
   failure = status.relayConfigured ? "Browser network discovery failed. Check direct and private relay reachability." : "Browser network discovery failed or timed out before contacting the device. Check this browser's network access; no relay is configured.";
   await gatherICE(pc, this.abort.signal); if (this.closed) return;
   const offer = pc.localDescription?.sdp; if (!offer) throw new Error();
   // Do not abort a dispatched start: its late result is needed to stop the exact remote lease.
   failure = "The device could not start the live session. Check its video capture prerequisites and connection status.";
   const response = await this.call("desktop_live", { action: "start", offer, maxWidth: 1280, fps, bitrate: fps === 30 ? 4000000 : 2500000 });
   failure = "The device returned an invalid live-session response. Update the device and Dashboard to matching versions.";
   const s = response.result as Partial<LiveSession> | null;
   if (!s || typeof s.sessionId !== "string" || !s.sessionId || typeof s.answer !== "string" || typeof s.width !== "number" || s.width <= 0 || typeof s.height !== "number" || s.height <= 0 || typeof s.leaseSeconds !== "number" || s.leaseSeconds < 5) throw new Error();
   this.session = s as LiveSession;
   if (this.closed) { this.remoteStop(); return; }
   this.renewLater(); this.startup = setTimeout(() => this.stop(status.relayConfigured ? "A connection could not be established using the available direct or private relay paths." : "A direct connection could not be established. This network may require a relay, which is not configured."), 15000);
   failure = "The browser rejected the device's video connection answer. Check browser H264/WebRTC support.";
   await pc.setRemoteDescription({ type: "answer", sdp: s.answer });
  } catch { if (!this.closed) this.stop(failure); }
 }
 private renewLater() {
  this.lease = setTimeout(() => { void this.renew(); }, 5000);
 }
 private async renew() {
   if (this.closed || !this.session) return;
   const timeout = setTimeout(() => this.stop("Session renewal timed out. Control has stopped."), 5000);
   try { await this.call("desktop_live", { action: "renew", sessionId: this.session.sessionId }); if (!this.closed) this.renewLater(); }
   catch { this.stop("Session renewal failed. Control has stopped."); } finally { clearTimeout(timeout); }
 }
 private remoteStop() { const id = this.session?.sessionId; this.session = undefined; if (id) void this.call("desktop_live", { action: "stop", sessionId: id }).catch(() => { /* Remote lease independently expires. */ }); }
 stop(message = "Session stopped. No desktop input is being sent.") {
  if (this.closed) return; this.closed = true; this.abort.abort(); clearTimeout(this.lease); clearTimeout(this.startup); clearInterval(this.ping);
  if (this.dc?.readyState === "open") this.input?.release(); this.input?.close();
  if (this.dc) { this.dc.onclose = null; this.dc.onerror = null; this.dc.onmessage = null; this.dc.close(); }
  if (this.pc) { this.pc.ontrack = null; this.pc.onconnectionstatechange = null; this.pc.close(); }
  this.callbacks.stream(null); this.callbacks.state(false, false); this.callbacks.stopped(message); this.remoteStop();
 }
}
