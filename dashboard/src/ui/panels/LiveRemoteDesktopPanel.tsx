import { useEffect, useRef, useState } from "react";
import { LiveDesktopConnection, LiveVideoWatch, containedPoint, parseLiveStatus, type LiveStatus } from "../live-desktop";
import { ComputerUsePanel } from "./ComputerUsePanel";
import type { DeviceCall, PanelProps } from "./types";

export function LiveRemoteDesktopPanel(props: PanelProps & { platform?: string }) {
 const [snapshot, setSnapshot] = useState(false);
 const supported = props.platform === undefined || ["darwin", "macos", "windows"].includes(props.platform);
 return <><div className="segmented live-mode-switch" aria-label="Computer Use mode"><button aria-pressed={!snapshot} onClick={() => setSnapshot(false)}>Live desktop</button><button aria-pressed={snapshot} onClick={() => setSnapshot(true)}>Snapshot tools</button></div>{snapshot ? <ComputerUsePanel {...props} /> : supported ? <LivePanel {...props} /> : <section className="panel-shell"><h2>Live desktop</h2><p>Live video currently supports macOS and Windows. Linux and WSL retain their existing Snapshot tools and terminal access.</p></section>}</>;
}

function LivePanel({ call, active = true }: PanelProps) {
 const [fps, setFPS] = useState<15 | 30>(30);
 const [relayOnly,setRelayOnly]=useState(false);
 const callRef=useRef(call);callRef.current=call;const latestCall=useRef<DeviceCall>((...args)=>callRef.current(...args));
 const [status, setStatus] = useState<LiveStatus | null>(null); const [message, setMessage] = useState("Checking live desktop availability…");
 const [running, setRunning] = useState(false); const [ready, setReady] = useState(false); const [control, setControl] = useState(false); const [videoReady, setVideoReady] = useState(false); const [text, setText] = useState("");
 const video = useRef<HTMLVideoElement>(null); const stage = useRef<HTMLDivElement>(null); const connection = useRef<LiveDesktopConnection | null>(null);
 const controlling = useRef(false); const wantedControl = useRef(false); const pointer = useRef<{ id: number; button: number; target: HTMLElement } | null>(null);
 const keys = useRef(new Set<string>()); const mounted = useRef(true);
 const videoWatch=useRef<LiveVideoWatch|null>(null);
 const clearPointer = () => { const p = pointer.current; pointer.current = null; if (p?.target.hasPointerCapture?.(p.id)) p.target.releasePointerCapture(p.id); };
 const release = () => { wantedControl.current = false; controlling.current = false; keys.current.clear(); clearPointer(); connection.current?.input?.release(); if (mounted.current) { setControl(false); setText(""); } };
 useEffect(() => {
  mounted.current = true; const abort = new AbortController();
  if (active) void latestCall.current("desktop_live", { action: "status" }, abort.signal).then((response) => { if (abort.signal.aborted) return; const s = parseLiveStatus(response.result); setStatus(s); setMessage(s.supported && s.available ? "Start a private, direct video session. Viewing only until you enable control." : s.reason ?? "Live desktop is unavailable on this device."); }).catch(() => { if (!abort.signal.aborted) setMessage("Live desktop availability could not be checked. Snapshot tools remain available separately."); });
  const blur = () => release(); const hidden = () => { if (document.hidden) release(); }; const escape = (event: KeyboardEvent) => { if (event.key === "Escape" && (controlling.current || wantedControl.current)) { event.preventDefault(); release(); } };
  const wheel = (event: WheelEvent) => {
   const v=video.current;if(!controlling.current||!v)return;const position=containedPoint(v.getBoundingClientRect(),v.videoWidth,v.videoHeight,event.clientX,event.clientY);if(!position)return;
   event.preventDefault();const unit=event.deltaMode===1?16:event.deltaMode===2?600:1;const bounded=(value:number)=>Math.max(-2000,Math.min(2000,Math.round(value*unit)));
   connection.current?.input?.send({type:"wheel",...position,scrollX:bounded(event.deltaX),scrollY:bounded(event.deltaY)});
  };
  const target=stage.current;target?.addEventListener("wheel",wheel,{passive:false});
  window.addEventListener("blur", blur); document.addEventListener("visibilitychange", hidden); window.addEventListener("keydown", escape, true);
  return () => { mounted.current = false; abort.abort(); release(); videoWatch.current?.close();videoWatch.current=null;connection.current?.stop(); connection.current = null; target?.removeEventListener("wheel",wheel);window.removeEventListener("blur", blur); document.removeEventListener("visibilitychange", hidden); window.removeEventListener("keydown", escape, true); };
  // These handlers use refs, not captured session state.
 }, [active]);

 const start = () => {
  if (!status?.available || !status.supported || connection.current || !active) return;
  setRunning(true); setReady(false); setVideoReady(false); setMessage(status.relayConfigured ? "Connecting to the device with private relay support…" : "Connecting directly to the device…");
  const session = new LiveDesktopConnection(latestCall.current, {
   stream: (stream) => { if (connection.current !== session || !mounted.current) return; videoWatch.current?.close();videoWatch.current=null;setVideoReady(false);if (video.current) { video.current.srcObject = stream; if (stream) {videoWatch.current=new LiveVideoWatch(video.current,(fresh)=>{if(mounted.current&&connection.current===session)setVideoReady(fresh);},()=>session.stop("Video stopped updating. Control has stopped to avoid acting on an old image."));void video.current.play().catch(() => { if (mounted.current && connection.current === session && video.current?.srcObject === stream) setMessage("Video is ready. Press Play video if your browser paused playback."); });} } },
   state: (enabled, connected) => { if (connection.current !== session || !mounted.current) return; setReady(connected); if (enabled && !wantedControl.current) { session.input?.release(); return; } if (!enabled) { if(controlling.current)wantedControl.current=false;keys.current.clear();clearPointer(); } controlling.current = enabled; setControl(enabled); if (connected) setMessage(enabled ? "Control enabled. Click the video to focus. Esc immediately returns to viewing only." : "Viewing only. No keyboard or pointer input is sent."); },
   stopped: (reason) => { if (connection.current !== session) return; connection.current = null; controlling.current = false; wantedControl.current = false; keys.current.clear(); clearPointer(); if (mounted.current) { setRunning(false); setControl(false); setReady(false); setVideoReady(false); setMessage(reason); } },
  });
  connection.current = session; void session.start(status, fps, relayOnly);
 };
 const toggleControl = () => { if (controlling.current || wantedControl.current) { release(); return; } if (!ready || !videoReady || !videoWatch.current?.isFresh()) return; wantedControl.current = true; connection.current?.input?.send({ type: "control", enabled: true }); stage.current?.focus(); };
 const point = (event: { clientX: number; clientY: number }) => { const v = video.current; return v ? containedPoint(v.getBoundingClientRect(), v.videoWidth, v.videoHeight, event.clientX, event.clientY) : null; };
 const pointerDown = (event: React.PointerEvent<HTMLDivElement>) => { if (!controlling.current || pointer.current || event.button > 2) return; const p = point(event); if (!p) return; event.preventDefault(); event.currentTarget.focus(); event.currentTarget.setPointerCapture(event.pointerId); pointer.current = { id: event.pointerId, button: event.button, target: event.currentTarget }; connection.current?.input?.flushMove(); connection.current?.input?.send({ type: "button", ...p, button: event.button, down: true }); };
 const pointerUp = (event: React.PointerEvent<HTMLDivElement>) => { const p = pointer.current; if (!p || p.id !== event.pointerId) return; const position = point(event); clearPointer(); if (!position || !controlling.current) { release(); return; } event.preventDefault(); connection.current?.input?.flushMove(); connection.current?.input?.send({ type: "button", ...position, button: p.button, down: false }); };
 const key = (event: React.KeyboardEvent<HTMLDivElement>, down: boolean) => {
  if (!controlling.current || event.key === "Escape" || event.nativeEvent.isComposing || event.code === "Unidentified" || !event.code) return;
  event.preventDefault(); event.stopPropagation(); if (!down && !keys.current.has(event.code)) return; if (down) keys.current.add(event.code); else keys.current.delete(event.code);
  connection.current?.input?.send({ type: "key", code: event.code, down });
 };
 const sendText = (value: string) => { if (!controlling.current || !value) return; if (new TextEncoder().encode(value).length > 4096 || value.includes("\0")) { setMessage("Text is too long. Send a smaller piece."); return; } connection.current?.input?.send({ type: "text", text: value }); setText(""); };

 const fullscreen = async () => { try { if(document.fullscreenElement === stage.current) await document.exitFullscreen(); else if(stage.current?.requestFullscreen) await stage.current.requestFullscreen(); else throw new Error(); } catch { setMessage("Fullscreen is unavailable in this browser. The embedded view remains available."); } };
 return <section className="panel-shell live-desktop-panel" aria-labelledby="live-desktop-title">
  <header className="panel-heading"><div><p className="eyebrow">Encrypted device connection · H264</p><h2 id="live-desktop-title">Live desktop</h2></div><div className="live-session-actions"><span className={`live-indicator ${running ? "is-live" : ""}`}>{running ? control ? "CONTROL ON" : "VIEW ONLY" : "DISCONNECTED"}</span>{running ? <button className="danger-button" onClick={() => connection.current?.stop()}>Stop session</button> : <button className="primary-button" disabled={!active || !status?.supported || !status.available} onClick={start}>Start live desktop</button>}</div></header>
  <p className="status-line" aria-live="polite">{message}</p>
  <label>Frame rate <select aria-label="Frame rate" value={fps} disabled={running} onChange={(event) => setFPS(event.target.value === "15" ? 15 : 30)}><option value="15">15 FPS · Lower load</option><option value="30">30 FPS · Smoother</option></select></label>
  {status?.relayConfigured && <label>Connection <select aria-label="Connection mode" disabled={running} value={relayOnly?"relay":"auto"} onChange={(event)=>setRelayOnly(event.target.value==="relay")}><option value="auto">Automatic · Direct preferred</option><option value="relay">Private relay only</option></select></label>}
  <button onClick={() => void fullscreen()}>Fullscreen</button>
  <div className={`live-video-stage ${control ? "has-control" : ""}`} ref={stage} tabIndex={control ? 0 : -1} role="application" aria-label="Remote desktop video. Escape releases control." onPointerDown={pointerDown} onPointerUp={pointerUp} onPointerMove={(event) => { if (controlling.current) { const p = point(event); if (p) connection.current?.input?.move(p); } }} onPointerCancel={() => release()} onLostPointerCapture={() => { if (pointer.current) release(); }} onContextMenu={(event) => { if (controlling.current) event.preventDefault(); }} onKeyDown={(event) => key(event, true)} onKeyUp={(event) => key(event, false)} onBlur={() => { if (pointer.current) release(); else { for (const code of keys.current) connection.current?.input?.send({ type: "key", code, down: false }); keys.current.clear(); } }}>
   <video ref={video} autoPlay muted playsInline aria-label="Live primary display" onEmptied={() => setVideoReady(false)} />
   {!videoReady && <div className="live-video-empty"><span>{running ? "CONNECTING" : "PRIMARY DISPLAY"}</span><p>{running ? "Negotiating a private video connection…" : "Continuous video. Direct input. Your explicit permission."}</p></div>}
   <div className="live-video-caption"><span>{videoReady ? `${video.current?.videoWidth} × ${video.current?.videoHeight}` : "NO VIDEO"}</span><span>{control ? "ESC TO RELEASE" : "INPUT DISABLED"}</span></div>
   <button className="live-fullscreen-exit" onPointerDown={event=>event.stopPropagation()} onKeyDown={event=>event.stopPropagation()} onClick={()=>void fullscreen()}>Exit fullscreen</button>
  </div>
  <div className="live-control-bar"><div><strong>{control ? "You are controlling this device" : "View first. Control when ready."}</strong><p>Leaving this window or pressing Esc releases every held key and button.</p></div><button className={control ? "danger-button" : "primary-button"} disabled={!ready || !videoReady} aria-pressed={control} onClick={toggleControl}>{control ? "Release control" : "Enable control"}</button>{running && <button onClick={() => void video.current?.play().catch(() => setMessage("Your browser could not play this video."))}>Play video</button>}</div>
  <div className="live-text-entry"><label>Text / IME<input type="password" aria-label="Text for remote device" disabled={!control} value={text} autoComplete="off" onChange={(event) => setText(event.target.value)} onCompositionEnd={(event) => setText(event.currentTarget.value)} /></label><button disabled={!control || !text} onClick={() => sendText(text)}>Send text</button><p>{status?.relayConfigured ? (relayOnly ? "Relay-only mode: direct connections are disabled. No microphone or clipboard sharing." : "Private relay configured; direct connections are preferred. No microphone or clipboard sharing.") : "No microphone, clipboard sharing, or relay service."}</p></div>
 </section>;
}
