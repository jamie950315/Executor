import { useState } from "react";
import type { PanelProps } from "./types";

interface PermissionItem { id: string; label: string; state: string; required: boolean; detail?: string; settings_url?: string }
interface PermissionReport { platform: string; requested: boolean; ready: boolean; restart_required: boolean; permissions: PermissionItem[] }

export function PermissionsPanel({ call }: PanelProps) {
  const [report, setReport] = useState<PermissionReport | null>(null);
  const [status, setStatus] = useState("Permission status not checked");
  const load = async (requestAll: boolean) => {
    setStatus(requestAll ? "Requesting host permissions…" : "Checking host permissions…");
    const controller = new AbortController();
    try {
      const response = await call("control.permissions", { action: requestAll ? "request_all" : "status" }, controller.signal);
      const next = parseReport(response.result);
      setReport(next);
      setStatus(next.ready ? "Granted — host control ready" : next.requested && next.restart_required ? "Requested — restart required" : next.requested ? "Requested — owner approval pending" : "Action required");
    } catch {
      setReport(null);
      setStatus("Permission status unavailable");
    }
  };
  return (
    <section className="panel-shell" aria-labelledby="permissions-title">
      <header className="panel-heading"><div><p className="eyebrow">Active-user boundary</p><h2 id="permissions-title">Permissions</h2></div><div className="button-row"><button onClick={() => void load(false)}>Check permissions</button><button className="primary-button" onClick={() => void load(true)}>Request all</button></div></header>
      <p className="status-line" aria-live="polite">{status}</p>
      {report && <><div className="metric-strip"><span>Platform <b>{report.platform}</b></span><span>Requested <b>{report.requested ? "Yes" : "No"}</b></span><span>Ready <b>{report.ready ? "Yes" : "No"}</b></span><span>Restart <b>{report.restart_required ? "Required" : "No"}</b></span></div>
        <ul className="permission-list">{report.permissions.map((item) => <li key={item.id}><div><strong>{item.label}</strong><span>{item.required ? "Required" : "Optional"}{item.detail ? ` · ${item.detail}` : ""}</span>{item.settings_url && <a href={item.settings_url}>Open settings</a>}</div><span className="state-chip">{item.state}</span></li>)}</ul></>}
    </section>
  );
}

function parseReport(value: unknown): PermissionReport {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error();
  const record = value as Record<string, unknown>;
  if (typeof record.platform !== "string" || typeof record.requested !== "boolean" || typeof record.ready !== "boolean" || !Array.isArray(record.permissions)) throw new Error();
  const permissions = record.permissions.flatMap((item): PermissionItem[] => {
    if (typeof item !== "object" || item === null || Array.isArray(item)) return [];
    const entry = item as Record<string, unknown>;
    if (typeof entry.id !== "string" || typeof entry.label !== "string" || typeof entry.state !== "string" || typeof entry.required !== "boolean") return [];
    return [{ id: entry.id, label: entry.label, state: entry.state, required: entry.required, ...(typeof entry.detail === "string" ? { detail: entry.detail } : {}), ...(typeof entry.settings_url === "string" ? { settings_url: entry.settings_url } : {}) }];
  });
  return { platform: record.platform, requested: record.requested, ready: record.ready, restart_required: record.restart_required === true, permissions };
}
