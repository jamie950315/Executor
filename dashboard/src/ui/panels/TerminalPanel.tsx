import { useCallback, useEffect, useRef, useState } from "react";
import type { PanelProps } from "./types";

interface TerminalSession { id: string; dir: string; running: boolean }

export function TerminalPanel({ call, active = true }: PanelProps) {
  const [privilege, setPrivilege] = useState<"owner" | "admin">("owner");
  const [sessions, setSessions] = useState<TerminalSession[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [outputs, setOutputs] = useState<Record<string, string>>({});
  const [command, setCommand] = useState("");
  const [cwd, setCwd] = useState("");
  const [input, setInput] = useState("");
  const [dimensions, setDimensions] = useState({ columns: 120, rows: 36 });
  const [status, setStatus] = useState("Loading persistent sessions…");
  const cursors = useRef<Record<string, number>>({});
  const sessionRequest = useRef<AbortController | null>(null);

  const refreshSessions = useCallback(async () => {
    sessionRequest.current?.abort();
    const controller = new AbortController();
    sessionRequest.current = controller;
    try {
      const response = await call("terminal_sessions", { action: "list", privilege }, controller.signal);
      if (controller.signal.aborted) return;
      const next = parseSessions(response.result);
      setSessions(next);
      setStatus(`${next.length} persistent ${privilege} session${next.length === 1 ? "" : "s"}`);
    } catch {
      if (controller.signal.aborted) return;
      setStatus("Terminal sessions unavailable");
    }
  }, [call, privilege]);

  useEffect(() => {
    setSessions([]);
    if (active) void refreshSessions();
    return () => sessionRequest.current?.abort();
  }, [active, refreshSessions]);

  useEffect(() => {
    if (!active || selected === null) return;
    let stopped = false;
    let inFlight: AbortController | null = null;
    const poll = async () => {
      if (stopped || inFlight !== null || document.visibilityState !== "visible") return;
      inFlight = new AbortController();
      try {
        const response = await call("terminal_output", {
          sessionId: selected,
          cursor: cursors.current[selected] ?? 0,
          privilege,
        }, inFlight.signal);
        if (stopped || inFlight.signal.aborted) return;
        const chunk = parseOutput(response.result);
        cursors.current[selected] = chunk.nextCursor;
        if (chunk.text.length > 0) {
          setOutputs((current) => ({ ...current, [selected]: `${current[selected] ?? ""}${chunk.text}`.slice(-2_000_000) }));
        }
        if (!chunk.running) setStatus("Session exited — output retained in this page only");
      } catch (error) {
        if (!stopped && !(error instanceof DOMException && error.name === "AbortError")) setStatus("Terminal output unavailable");
      } finally {
        inFlight = null;
      }
    };
    void poll();
    const interval = window.setInterval(() => void poll(), 1000);
    const visibility = () => { if (document.visibilityState === "visible") void poll(); };
    document.addEventListener("visibilitychange", visibility);
    return () => {
      stopped = true;
      inFlight?.abort();
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [active, call, privilege, selected]);

  const create = async () => {
    const controller = new AbortController();
    try {
      const response = await call("terminal", {
        action: "create", privilege, command, cwd, columns: dimensions.columns, rows: dimensions.rows,
      }, controller.signal);
      const record = asRecord(response.result);
      const id = textField(record, "ID", "id");
      if (!id) throw new Error();
      cursors.current[id] = 0;
      setSelected(id);
      setOutputs((current) => ({ ...current, [id]: "" }));
      setCommand("");
      await refreshSessions();
      setStatus(`Attached to ${id}`);
    } catch { setStatus("Session creation failed"); }
  };

  const sendInput = async () => {
    if (!selected || input.length === 0) return;
    const value = input.endsWith("\n") ? input : `${input}\n`;
    setInput("");
    try { await call("terminal", { action: "write", privilege, sessionId: selected, input: value }, new AbortController().signal); }
    catch { setStatus("Terminal input failed"); }
  };

  const signal = async (signalName: "interrupt" | "terminate" | "kill") => {
    if (!selected) return;
    try { await call("terminal", { action: "signal", privilege, sessionId: selected, signal: signalName }, new AbortController().signal); }
    catch { setStatus("Terminal signal failed"); }
  };

  const resize = async () => {
    if (!selected) return;
    try { await call("terminal", { action: "resize", privilege, sessionId: selected, ...dimensions }, new AbortController().signal); setStatus(`Resized to ${dimensions.columns}×${dimensions.rows}`); }
    catch { setStatus("Terminal resize failed"); }
  };

  const close = async () => {
    if (!selected) return;
    const id = selected;
    try { await call("terminal", { action: "close", privilege, sessionId: id }, new AbortController().signal); setSelected(null); await refreshSessions(); }
    catch { setStatus("Terminal close failed"); }
  };

  return (
    <section className="panel-shell terminal-panel" aria-labelledby="terminal-title">
      <header className="panel-heading"><div><p className="eyebrow">PTY / persistent / volatile display</p><h2 id="terminal-title">Terminal</h2></div>
        <div className="segmented" aria-label="Terminal privilege"><button aria-pressed={privilege === "owner"} onClick={() => { setPrivilege("owner"); setSelected(null); }}>Owner</button><button aria-pressed={privilege === "admin"} onClick={() => { setPrivilege("admin"); setSelected(null); }}>Admin</button></div></header>
      <p className="status-line" aria-live="polite">{status}</p>
      <div className="terminal-layout">
        <aside className="session-rail" aria-label="Persistent terminal sessions">
          <button onClick={() => void refreshSessions()}>Refresh sessions</button>
          {sessions.map((session) => <button className={selected === session.id ? "selected" : ""} key={session.id} onClick={() => setSelected(session.id)}><strong>{session.id}</strong><span>{session.running ? "running" : "exited"} · {session.dir || "default cwd"}</span></button>)}
        </aside>
        <div className="terminal-workbench">
          <div className="terminal-create"><label>Initial command<input value={command} onChange={(event) => setCommand(event.target.value)} placeholder="optional" /></label><label>Working directory<input value={cwd} onChange={(event) => setCwd(event.target.value)} placeholder="host default" /></label><button className="primary-button" onClick={() => void create()}>New session</button></div>
          <pre className="terminal-screen" role="log" aria-live="polite" tabIndex={0}>{selected ? outputs[selected] || "Attached. Waiting for output…" : "Select or create a persistent session."}</pre>
          <label className="terminal-input">Terminal input<textarea value={input} disabled={!selected} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); void sendInput(); } }} /></label>
          <div className="terminal-controls"><button disabled={!selected} onClick={() => void sendInput()}>Send</button><button disabled={!selected} onClick={() => void signal("interrupt")}>Ctrl+C</button><button disabled={!selected} onClick={() => void signal("terminate")}>Terminate</button><button className="danger-ghost" disabled={!selected} onClick={() => void signal("kill")}>Kill process</button><label>Columns<input type="number" min="1" max="32767" value={dimensions.columns} onChange={(event) => setDimensions((value) => ({ ...value, columns: Number(event.target.value) }))} /></label><label>Rows<input type="number" min="1" max="32767" value={dimensions.rows} onChange={(event) => setDimensions((value) => ({ ...value, rows: Number(event.target.value) }))} /></label><button disabled={!selected} onClick={() => void resize()}>Resize</button><button className="danger-ghost" disabled={!selected} onClick={() => void close()}>Close</button></div>
        </div>
      </div>
    </section>
  );
}

function parseSessions(value: unknown): TerminalSession[] {
  if (!Array.isArray(value)) throw new Error("Invalid terminal session list");
  return value.map((item) => {
    const entry = asRecord(item); const session = asRecord(entry?.Session ?? entry?.session);
    const id = textField(session, "ID", "id");
    const running = entry?.Running ?? entry?.running;
    if (!id || typeof running !== "boolean") throw new Error("Invalid terminal session");
    return { id, dir: textField(session, "Dir", "dir") ?? "", running };
  });
}
function parseOutput(value: unknown): { text: string; nextCursor: number; running: boolean } {
  const record = asRecord(value); if (!record) throw new Error();
  const data = textField(record, "Data", "data");
  const next = numberField(record, "NextCursor", "nextCursor", "next_cursor");
  const running = record.Running ?? record.running;
  if (data === null || next === null || next < 0 || typeof running !== "boolean") throw new Error("Invalid terminal output");
  return { text: decodeStandardBase64(data), nextCursor: next, running };
}
function decodeStandardBase64(value: string): string {
  if (value === "") return "";
  if (value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) throw new Error();
  const binary = atob(value); if (btoa(binary) !== value) throw new Error();
  return new TextDecoder().decode(Uint8Array.from(binary, (character) => character.charCodeAt(0)));
}
function asRecord(value: unknown): Record<string, unknown> | null { return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function textField(record: Record<string, unknown> | null, ...keys: string[]): string | null { for (const key of keys) if (typeof record?.[key] === "string") return record[key] as string; return null; }
function numberField(record: Record<string, unknown>, ...keys: string[]): number | null { for (const key of keys) if (typeof record[key] === "number" && Number.isSafeInteger(record[key])) return record[key] as number; return null; }
