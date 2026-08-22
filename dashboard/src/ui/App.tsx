import { useCallback, useEffect, useMemo, useState } from "react";
import { callDevice, DeviceLockedError, DeviceOfflineError, fetchDevices, fetchSession, type DeviceRecord, type SessionContext } from "./api";
import { DeviceGrid, type DeviceView } from "./DeviceGrid";
import { UnlockDialog } from "./UnlockDialog";
import { WorkspaceTabs, type WorkspaceTab } from "./WorkspaceTabs";
import { OneTimeSecretDialog } from "./OneTimeSecretDialog";
import type { SensitiveResult } from "./sensitive-result";
import type { DeviceCall } from "./panels/types";
import { OverviewPanel } from "./panels/OverviewPanel";
import { TerminalPanel } from "./panels/TerminalPanel";
import { FilesPanel } from "./panels/FilesPanel";
import { ComputerUsePanel } from "./panels/ComputerUsePanel";
import { PermissionsPanel } from "./panels/PermissionsPanel";
import { ControlPanel } from "./panels/ControlPanel";
import { AuditPanel } from "./panels/AuditPanel";

export function App() {
  const [session, setSession] = useState<SessionContext | null>(null);
  const [devices, setDevices] = useState<DeviceView[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [unlocking, setUnlocking] = useState<DeviceView | null>(null);
  const [oneTimeResult, setOneTimeResult] = useState<SensitiveResult | null>(null);
  const [status, setStatus] = useState("Establishing Access-protected browser context…");
  const [loading, setLoading] = useState(true);

  const markState = useCallback((deviceID: string, uiState: DeviceView["uiState"], state?: DeviceRecord["state"], generation?: number) => {
    setDevices((current) => current.map((device) => device.device_id === deviceID ? { ...device, uiState, ...(state ? { state } : {}), ...(generation ? { generation } : {}) } : device));
  }, []);

  const probe = useCallback(async (device: DeviceRecord): Promise<DeviceView> => {
    if (device.state === "offline") return { ...device, uiState: "offline" };
    try {
      await callDevice(device.device_id, "device_status", { action: "summary" }, new AbortController().signal);
      return { ...device, uiState: "unlocked" };
    } catch (error) {
      if (error instanceof DeviceLockedError) return { ...device, uiState: "locked" };
      if (error instanceof DeviceOfflineError) return { ...device, state: "offline", uiState: "offline" };
      return { ...device, uiState: "needs_attention" };
    }
  }, []);

  const loadFleet = useCallback(async () => {
    setLoading(true);
    try {
      const nextSession = await fetchSession();
      setSession(nextSession);
      const registry = await fetchDevices();
      const views = await Promise.all(registry.map(probe));
      setDevices(views);
      setStatus(`${views.length} enrolled device${views.length === 1 ? "" : "s"} · browser-bound grants`);
    } catch {
      setStatus("Dashboard session or registry is unavailable");
    } finally { setLoading(false); }
  }, [probe]);

  useEffect(() => { void loadFleet(); }, [loadFleet]);
  useEffect(() => { window.scrollTo(0, 0); }, [selectedID]);
  const selected = useMemo(() => devices.find((device) => device.device_id === selectedID) ?? null, [devices, selectedID]);
  const open = (device: DeviceView) => { if (device.uiState === "unlocked" || device.uiState === "needs_attention") setSelectedID(device.device_id); };

  return <div className="app-frame"><a className="skip-link" href="#main-content">Skip to controls</a><header className="global-header"><div className="wordmark"><span>EX</span><strong>Executor</strong></div><div className="system-legend"><span className="live-dot" />Sovereign host control</div><div className="identity-block"><span>Access subject</span><strong>{session?.access_subject ?? "verifying"}</strong></div></header>
    <main id="main-content" className={selected ? "workspace-main" : "fleet-main"}>
      {selected && session ? <DeviceWorkspace device={selected} session={session} onBack={() => setSelectedID(null)} onLocked={() => { markState(selected.device_id, "locked"); setSelectedID(null); }} onKilled={() => { markState(selected.device_id, "killed", "offline", selected.generation + 1); setStatus(`${selected.name} was killed at generation ${selected.generation + 1}. Resume requires local rescue or executor CLI.`); }} onRotated={() => { markState(selected.device_id, "locked", "online", selected.generation + 1); setStatus(`${selected.name} rotated to generation ${selected.generation + 1}. Unlock this browser again.`); setSelectedID(null); }} onRemoved={() => { setDevices((current) => current.filter((device) => device.device_id !== selected.device_id)); setStatus(`${selected.name} was removed. Re-enroll it locally from the host CLI with a new one-time token.`); setSelectedID(null); }} onSensitive={setOneTimeResult} /> : <><section className="fleet-hero"><div><p className="eyebrow">Unified Dashboard / control plane</p><h1>Owner<br />Command</h1></div><div className="hero-copy"><p>Authenticated AI control across macOS, Windows, Linux, and WSL. Every device unlock belongs to one Access identity and one HttpOnly browser identity.</p><button onClick={() => void loadFleet()} disabled={loading}>{loading ? "Synchronizing…" : "Refresh fleet"}</button></div></section><p className="fleet-status" aria-live="polite">{status}</p><DeviceGrid devices={devices} onOpen={open} onUnlock={setUnlocking} /></>}
    </main><footer className="global-footer"><span>Executor Unified Dashboard</span><span>Payload-free audit · local Kill Switch</span><span>{new Date().getFullYear()}</span></footer>
    {unlocking && session && <UnlockDialog device={unlocking} session={session} onDismiss={() => setUnlocking(null)} onUnlocked={() => { markState(unlocking.device_id, "unlocked"); setUnlocking(null); setSelectedID(unlocking.device_id); }} />}
    {oneTimeResult && <OneTimeSecretDialog value={oneTimeResult} onDismiss={() => setOneTimeResult(null)} />}
  </div>;
}

function DeviceWorkspace({ device, session, onBack, onLocked, onKilled, onRotated, onRemoved, onSensitive }: {
  device: DeviceView; session: SessionContext; onBack: () => void; onLocked: () => void; onKilled: () => void; onRotated: () => void; onRemoved: () => void; onSensitive: (value: SensitiveResult) => void;
}) {
  const [tab, setTab] = useState<WorkspaceTab>("overview");
  const call: DeviceCall = useCallback(async (method, argumentsValue, signal) => {
    try { return await callDevice(device.device_id, method, argumentsValue, signal); }
    catch (error) { if (error instanceof DeviceLockedError) onLocked(); throw error; }
  }, [device.device_id, onLocked]);
  const panel = tab === "overview" ? <OverviewPanel call={call} device={device} /> : tab === "terminal" ? <TerminalPanel call={call} active /> : tab === "files" ? <FilesPanel call={call} /> : tab === "computer" ? <ComputerUsePanel call={call} /> : tab === "permissions" ? <PermissionsPanel call={call} /> : tab === "control" ? <ControlPanel call={call} device={device} session={session} onSensitive={onSensitive} onKilled={onKilled} onRotated={onRotated} onRemoved={onRemoved} /> : <AuditPanel call={call} />;
  return <><section className="workspace-mast"><button className="back-button" onClick={onBack}>← Fleet</button><div><p className="eyebrow">{device.platform} / {device.arch} / generation {device.generation}</p><h1>{device.name}</h1></div><span className="state-chip" data-state={device.uiState}>{device.uiState.replace("_", " ")}</span></section><WorkspaceTabs active={tab} onChange={setTab} /><section id={`panel-${tab}`} role="tabpanel" aria-labelledby={`tab-${tab}`} tabIndex={0}>{panel}</section></>;
}
