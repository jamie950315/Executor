import { useState } from "react";
import type { PanelProps } from "./types";

interface AuditEvent { time: string; actor?: string; method: string; outcome?: string }

export function AuditPanel({ call }: PanelProps) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [status, setStatus] = useState("Audit not loaded");
  const refresh = async () => {
    setStatus("Loading metadata-only audit…");
    const controller = new AbortController();
    try {
      const response = await call("control.audit", { limit: 100 }, controller.signal);
      const record = asRecord(response.result);
      const rawEvents = Array.isArray(record?.events) ? record.events : [];
      setEvents(rawEvents.flatMap(parseEvent));
      setStatus(`${rawEvents.length} audit events`);
    } catch {
      setEvents([]);
      setStatus("Audit unavailable");
    }
  };
  return (
    <section className="panel-shell" aria-labelledby="audit-title">
      <header className="panel-heading"><div><p className="eyebrow">Metadata only</p><h2 id="audit-title">Audit</h2></div><button onClick={() => void refresh()}>Refresh audit</button></header>
      <p className="status-line" aria-live="polite">{status}</p>
      <div className="table-scroll"><table><thead><tr><th>Time</th><th>Actor</th><th>Method</th><th>Outcome</th></tr></thead>
        <tbody>{events.map((event, index) => <tr key={`${event.time}-${index}`}><td>{event.time}</td><td>{event.actor ?? "—"}</td><td>{event.method}</td><td>{event.outcome ?? "—"}</td></tr>)}</tbody>
      </table></div>
    </section>
  );
}

function parseEvent(value: unknown): AuditEvent[] {
  const record = asRecord(value);
  if (record === null || typeof record.time !== "string" || typeof record.method !== "string") return [];
  return [{
    time: record.time,
    method: record.method,
    ...(typeof record.actor === "string" ? { actor: record.actor } : {}),
    ...(typeof record.outcome === "string" ? { outcome: record.outcome } : {}),
  }];
}
function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null;
}
