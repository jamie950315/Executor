import { useEffect, useRef, useState } from "react";
import type { PanelProps } from "./types";

interface ScreenCapture { captureId: string; width: number; height: number; mimeType: string; imageURL: string }
type ComputerAction =
  | { type: "click" | "double_click" | "move"; x: number; y: number; button?: "left" | "right" | "middle" }
  | { type: "drag"; button: "left" | "right" | "middle"; path: Array<{ x: number; y: number }> }
  | { type: "scroll"; x: number; y: number; scrollX: number; scrollY: number }
  | { type: "type"; text: string }
  | { type: "keypress"; keys: string[] };

export function ComputerUsePanel({ call, active = true }: PanelProps) {
  const [capture, setCapture] = useState<ScreenCapture | null>(null);
  const [queue, setQueue] = useState<ComputerAction[]>([]);
  const [mode, setMode] = useState<"click" | "double_click" | "move" | "drag">("click");
  const [button, setButton] = useState<"left" | "right" | "middle">("left");
  const [typedText, setTypedText] = useState("");
  const [keypress, setKeypress] = useState("CTRL,L");
  const [scroll, setScroll] = useState({ x: 0, y: 0, scrollX: 0, scrollY: 600 });
  const [status, setStatus] = useState("Observe the current desktop before controlling it");
  const [busy, setBusy] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const gesture = useRef<{ point: { x: number; y: number }; pointerId: number; mode: typeof mode; button: typeof button; target: HTMLImageElement } | null>(null);
  const cancelGesture = () => {
    const started = gesture.current;
    gesture.current = null;
    if (started?.target.hasPointerCapture(started.pointerId)) started.target.releasePointerCapture(started.pointerId);
  };

  useEffect(() => {
    setCapture(null); setQueue([]); setBusy(false);
    setStatus("Observe the current desktop before controlling it");
    return () => { pending.current?.abort(); pending.current = null; cancelGesture(); };
  }, [active]);

  const beginRequest = () => {
    if (!active || pending.current) return null;
    const controller = new AbortController();
    pending.current = controller;
    cancelGesture(); setBusy(true); setQueue([]); setCapture(null);
    return controller;
  };
  const finishRequest = (controller: AbortController) => {
    if (pending.current === controller) { pending.current = null; setBusy(false); }
  };
  const canQueue = () => active && !pending.current && capture !== null;
  const controlsDisabled = !active || busy || !capture;

  const observe = async () => {
    const controller = beginRequest(); if (!controller) return;
    setStatus("Capturing the active primary display…");
    try {
      const response = await call("desktop_observe", { action: "screenshot", includeImage: true }, controller.signal);
      if (pending.current !== controller) return;
      setCapture(parseScreenshotResult(response.result)); setQueue([]); setStatus("Fresh capture ready");
    } catch { if (pending.current === controller) { setCapture(null); setStatus("Desktop unavailable — the session may be locked or unsupported on this platform"); } }
    finally { finishRequest(controller); }
  };
  const coordinate = (event: React.PointerEvent<HTMLImageElement>): { x: number; y: number } | null => {
    if (!capture || !canQueue()) return null;
    const rect = event.currentTarget.getBoundingClientRect();
    const x = event.clientX - rect.left; const y = event.clientY - rect.top;
    if (rect.width <= 0 || rect.height <= 0 || !Number.isFinite(x) || !Number.isFinite(y) || x < 0 || y < 0 || x >= rect.width || y >= rect.height) return null;
    return { x: Math.floor(x * capture.width / rect.width), y: Math.floor(y * capture.height / rect.height) };
  };
  const pointerDown = (event: React.PointerEvent<HTMLImageElement>) => {
    if (gesture.current || event.button !== 0) return;
    const point = coordinate(event); if (!point) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    gesture.current = { point, pointerId: event.pointerId, mode, button, target: event.currentTarget };
  };
  const pointerUp = (event: React.PointerEvent<HTMLImageElement>) => {
    const started = gesture.current;
    if (!started || started.pointerId !== event.pointerId) return;
    cancelGesture();
    const point = coordinate(event); if (!point) return;
    const action: ComputerAction = started.mode === "drag"
      ? { type: "drag", button: started.button, path: [started.point, point] }
      : { type: started.mode, ...point, ...(started.mode === "move" ? {} : { button: started.button }) };
    setQueue((items) => [...items, action]);
  };
  const pointerCancel = (event: React.PointerEvent<HTMLImageElement>) => {
    if (gesture.current?.pointerId === event.pointerId) cancelGesture();
  };
  const queueTyping = () => { if (!canQueue() || !typedText) return; setQueue((items) => [...items, { type: "type", text: typedText }]); setTypedText(""); };
  const queueKeypress = () => { if (!canQueue()) return; const keys = keypress.split(/[,+]/u).map((key) => key.trim()).filter(Boolean); if (keys.length > 0) setQueue((items) => [...items, { type: "keypress", keys }]); };
  const queueScroll = () => { if (canQueue()) setQueue((items) => [...items, { type: "scroll", ...scroll }]); };
  const confirm = async () => {
    if (!capture || queue.length === 0) return;
    const submittedCaptureID = capture.captureId; const actions = queue;
    const controller = beginRequest(); if (!controller) return;
    setStatus("Executing confirmed action queue…");
    try {
      const response = await call("desktop_control", { action: "batch", captureId: submittedCaptureID, actions }, controller.signal);
      if (pending.current !== controller) return;
      setCapture(parseScreenshotResult(response.result)); setStatus("Actions completed — fresh capture accepted");
    } catch {
      if (pending.current === controller) setStatus("Execution outcome unconfirmed. Some actions may already have completed. Nothing was retried. Refresh the screen and inspect it before deciding what to do next.");
    } finally { finishRequest(controller); }
  };
  return (
    <section className="panel-shell computer-panel" aria-labelledby="computer-title"><header className="panel-heading"><div><p className="eyebrow">Capture-bound action loop</p><h2 id="computer-title">Computer Use</h2></div><button className="primary-button" disabled={busy || !active} onClick={() => void observe()}>Refresh screen</button></header>
      <p className="status-line" aria-live="polite">{status}</p>
      <div className="computer-layout"><div className="capture-stage">{capture ? <img src={capture.imageURL} width={capture.width} height={capture.height} alt={`Current screen capture ${capture.captureId}`} onPointerDown={pointerDown} onPointerUp={pointerUp} onPointerCancel={pointerCancel} onLostPointerCapture={pointerCancel} style={{ touchAction: "none" }} draggable={false} /> : <div className="capture-empty"><span>NO CAPTURE</span><p>Desktop actions require a fresh single-use capture ID.</p></div>}<div className="capture-meta">{capture ? `${capture.width}×${capture.height} · ${capture.mimeType} · ${capture.captureId}` : "Coordinate plane unavailable"}</div></div>
        <aside className="action-console"><fieldset disabled={controlsDisabled}><legend>Pointer action</legend><div className="segmented wrap">{(["click", "double_click", "move", "drag"] as const).map((value) => <button type="button" key={value} aria-pressed={mode === value} onClick={() => setMode(value)}>{value.replace("_", " ")}</button>)}</div><label>Button<select value={button} onChange={(event) => setButton(event.target.value as typeof button)}><option>left</option><option>right</option><option>middle</option></select></label><p>Choose a mode, then point on the exact rendered screenshot.</p></fieldset>
          <fieldset disabled={controlsDisabled}><legend>Keyboard</legend><label>Text to type<input type="password" value={typedText} onChange={(event) => setTypedText(event.target.value)} autoComplete="off" /></label><button onClick={queueTyping}>Queue typing</button><label>Key chord<input value={keypress} onChange={(event) => setKeypress(event.target.value)} /></label><button onClick={queueKeypress}>Queue keypress</button></fieldset>
          <fieldset disabled={controlsDisabled}><legend>Scroll</legend><div className="coordinate-grid">{(["x", "y", "scrollX", "scrollY"] as const).map((key) => <label key={key}>{key}<input type="number" value={scroll[key]} onChange={(event) => setScroll((value) => ({ ...value, [key]: Number(event.target.value) }))} /></label>)}</div><button onClick={queueScroll}>Queue scroll</button></fieldset>
        </aside></div>
      <section className="action-queue" aria-labelledby="queue-title"><header><div><p className="eyebrow">Preview before control</p><h3 id="queue-title">Action queue</h3></div><button onClick={() => setQueue([])} disabled={controlsDisabled || queue.length === 0}>Clear</button></header>{queue.length === 0 ? <p>No actions queued.</p> : <ol>{queue.map((action, index) => <li key={index}>{describeAction(action)}</li>)}</ol>}<button className="primary-button" disabled={controlsDisabled || queue.length === 0} onClick={() => void confirm()}>Confirm {queue.length} action{queue.length === 1 ? "" : "s"}</button></section>
    </section>
  );
}

export function parseScreenshotResult(value: unknown): ScreenCapture {
  const outer = record(value); if (!outer) throw new Error(); const structured = record(outer.StructuredContent ?? outer.structuredContent ?? outer.structured_content); const content = outer.Content ?? outer.content;
  const captureId = structured?.captureId ?? structured?.capture_id; const width = structured?.width; const height = structured?.height; const mimeType = structured?.mimeType ?? structured?.mime_type;
  if (typeof captureId !== "string" || captureId.length === 0 || typeof width !== "number" || !Number.isSafeInteger(width) || width < 1 || typeof height !== "number" || !Number.isSafeInteger(height) || height < 1 || typeof mimeType !== "string" || !mimeType.startsWith("image/") || !Array.isArray(content)) throw new Error();
  const image = content.map(record).find((item) => item?.type === "image"); const data = image?.data; const imageMime = image?.mimeType ?? image?.mime_type;
  if (typeof data !== "string" || typeof imageMime !== "string" || imageMime !== mimeType || !strictStandardBase64(data)) throw new Error();
  return { captureId, width, height, mimeType, imageURL: `data:${mimeType};base64,${data}` };
}
function describeAction(action: ComputerAction): string { switch (action.type) { case "type": return `${"•".repeat(action.text.length)} · ${action.text.length} characters`; case "keypress": return `keypress · ${action.keys.join(" + ")}`; case "drag": return `drag · ${action.path[0]?.x},${action.path[0]?.y} → ${action.path.at(-1)?.x},${action.path.at(-1)?.y}`; case "scroll": return `scroll · ${action.x},${action.y} / ${action.scrollX},${action.scrollY}`; default: return `${action.type.replace("_", " ")} · ${action.x},${action.y}`; } }
function strictStandardBase64(value: string): boolean { if (value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) return false; try { return btoa(atob(value)) === value; } catch { return false; } }
function record(value: unknown): Record<string, unknown> | null { return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null; }
