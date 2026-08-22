import { useEffect, useState } from "react";
import type { DeviceRecord } from "../api";
import type { PanelProps } from "./types";

export function OverviewPanel({ call, device }: PanelProps & { device: DeviceRecord }) {
  const [control, setControl] = useState<Record<string, unknown> | null>(null);
  const [host, setHost] = useState<Record<string, unknown> | null>(null);
  const [status, setStatus] = useState("Collecting live host status…");
  useEffect(() => {
    const controller = new AbortController();
    void Promise.allSettled([
      call("control.status", {}, controller.signal),
      call("device_status", { action: "summary" }, controller.signal),
    ]).then(([controlResult, hostResult]) => {
      setControl(controlResult.status === "fulfilled" ? record(controlResult.value.result) : null);
      setHost(hostResult.status === "fulfilled" ? record(hostResult.value.result) : null);
      setStatus(controlResult.status === "fulfilled" && hostResult.status === "fulfilled" ? "Live status verified" : "Some host status is unavailable");
    });
    return () => controller.abort();
  }, [call]);
  return <section className="panel-shell overview-panel" aria-labelledby="overview-title"><header className="panel-heading"><div><p className="eyebrow">Live system telemetry</p><h2 id="overview-title">Overview</h2></div><span className="state-chip">{String(control?.state ?? "checking")}</span></header><p className="status-line" aria-live="polite">{status}</p>
    <div className="overview-grid"><section><h3>Lifecycle</h3><dl className="status-matrix">{(["agent", "broker", "desktop", "tunnel"] as const).map((key) => <div key={key}><dt>{key}</dt><dd>{text(control?.[key])}</dd></div>)}</dl></section><section><h3>Host capability</h3><dl className="status-matrix"><div><dt>Broker</dt><dd>{componentState(host?.broker)}</dd></div><div><dt>Desktop</dt><dd>{componentState(host?.desktop)}</dd></div><div><dt>Platform</dt><dd>{device.platform} / {device.arch}</dd></div><div><dt>Version</dt><dd>{device.version}</dd></div></dl></section></div>
    <section className="platform-note"><p className="eyebrow">Platform boundary</p><h3>{platformLimit(device.platform).title}</h3><p>{platformLimit(device.platform).body}</p></section>
  </section>;
}
function platformLimit(platform: string): { title: string; body: string } { const value = platform.toLowerCase(); if (value.includes("darwin") || value.includes("mac")) return { title: "Active unlocked macOS desktop required", body: "Screen Recording, Accessibility, and Input Control approvals belong to the installed Executor Desktop helper. Terminal and administrator control remain available when the GUI is locked." }; if (value.includes("windows")) return { title: "Active input desktop required", body: "Computer Use targets the primary display on the unlocked Default desktop. The Desktop Scheduled Task must run as the active console user." }; if (value.includes("wsl")) return { title: "WSLg only", body: "Linux terminal and filesystem control are native. Windows desktop control requires the Windows companion; Linux GUI control requires a live WSLg session." }; return { title: "Display session dependent", body: "X11 supports the complete action surface. Advanced Wayland mouse actions may be unavailable and require an explicit supported active-user session." }; }
function record(value: unknown): Record<string, unknown> | null { return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function text(value: unknown): string { return typeof value === "string" ? value : "unavailable"; }
function componentState(value: unknown): string { const item = record(value); return typeof item?.component === "string" ? `${item.component}${item.available === false ? " unavailable" : " ready"}` : "unavailable"; }
