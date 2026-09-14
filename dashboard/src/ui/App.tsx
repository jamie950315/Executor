import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { callDevice, DeviceLockedError, DeviceOfflineError, DeviceResponseError, fetchDevices, fetchSession, type DeviceRecord, type SessionContext } from "./api";
import { DeviceGrid, type DeviceView } from "./DeviceGrid";
import { UnlockDialog } from "./UnlockDialog";
import { WorkspaceTabs, type WorkspaceTab } from "./WorkspaceTabs";
import { OneTimeSecretDialog } from "./OneTimeSecretDialog";
import type { SensitiveResult } from "./sensitive-result";
import type { DeviceCall } from "./panels/types";
import { OverviewPanel } from "./panels/OverviewPanel";
import { TerminalPanel } from "./panels/TerminalPanel";
import { FilesPanel } from "./panels/FilesPanel";
import { LiveRemoteDesktopPanel } from "./panels/LiveRemoteDesktopPanel";
import { PermissionsPanel } from "./panels/PermissionsPanel";
import { ControlPanel } from "./panels/ControlPanel";
import { AuditPanel } from "./panels/AuditPanel";

export const FLEET_REVALIDATION_INTERVAL_MS = 30_000;

export function App() {
  const [session, setSession] = useState<SessionContext | null>(null);
  const [devices, setDevices] = useState<DeviceView[]>([]);
  const workspaceTab = useRef<WorkspaceTab>("overview");
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [unlocking, setUnlocking] = useState<DeviceView | null>(null);
  const [oneTimeResult, setOneTimeResult] = useState<SensitiveResult | null>(null);
  const [status, setStatus] = useState("Establishing Access-protected browser context…");
  const [loading, setLoading] = useState(true);
  const [operationNotice, setOperationNotice] = useState<string | null>(null);
  const reportFailure = useCallback((error: unknown) => {
    if (!(error instanceof DeviceResponseError) && !(error instanceof DeviceOfflineError)) return;
    const code = error instanceof DeviceResponseError && /^[a-z_]{1,64}$/u.test(error.code) ? error.code : "relay_unavailable";
    const requestID = error instanceof DeviceResponseError && error.requestID !== null && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu.test(error.requestID) ? error.requestID : null;
    setOperationNotice(`Operation outcome unconfirmed (${code}${requestID === null ? "" : `; request ${requestID}`}). Nothing was retried. Check host state before retrying.`);
  }, []);
  const activeRefresh = useRef<AbortController | null>(null);
  const refreshSequence = useRef(0);

  const markState = useCallback((deviceID: string, uiState: DeviceView["uiState"], state?: DeviceRecord["state"], generation?: number) => {
    setDevices((current) => current.map((device) => device.device_id === deviceID ? { ...device, uiState, ...(state ? { state } : {}), ...(generation !== undefined ? { generation } : {}) } : device));
  }, []);

  const probe = useCallback(async (device: DeviceRecord, signal: AbortSignal): Promise<DeviceView> => {
    throwIfAborted(signal);
    if (device.state === "offline") return { ...device, uiState: "offline" };
    try {
      await callDevice(device.device_id, "device_status", { action: "summary" }, signal);
      throwIfAborted(signal);
      return { ...device, uiState: "unlocked" };
    } catch (error) {
      throwIfAborted(signal);
      if (error instanceof DeviceLockedError) return { ...device, uiState: "locked" };
      if (error instanceof DeviceOfflineError) return { ...device, state: "offline", uiState: "offline" };
      return { ...device, uiState: "needs_attention" };
    }
  }, []);

  const loadFleet = useCallback(async (showLoading = false) => {
    const sequence = ++refreshSequence.current;
    activeRefresh.current?.abort();
    const controller = new AbortController();
    activeRefresh.current = controller;
    if (showLoading) setLoading(true);
    try {
      const nextSession = await fetchSession(controller.signal);
      const registry = await fetchDevices(controller.signal);
      const views = await Promise.all(registry.map((device) => probe(device, controller.signal)));
      if (controller.signal.aborted || sequence !== refreshSequence.current) return;
      setSession(nextSession);
      setDevices((current) => reconcileKilledDevices(current, views));
      setSelectedID((current) => {
        if (current === null) return null;
        const selected = views.find((device) => device.device_id === current);
        return selected && (selected.uiState === "unlocked" || selected.uiState === "needs_attention" || (selected.uiState === "offline" && workspaceTab.current === "files")) ? current : null;
      });
      setStatus(`${views.length} enrolled device${views.length === 1 ? "" : "s"} · browser-bound grants`);
    } catch {
      if (controller.signal.aborted || sequence !== refreshSequence.current) return;
      setStatus("Dashboard session or registry is unavailable");
    } finally {
      if (sequence === refreshSequence.current) {
        if (activeRefresh.current === controller) activeRefresh.current = null;
        setLoading(false);
      }
    }
  }, [probe]);

  useEffect(() => {
    const refreshVisibleFleet = () => { if (document.visibilityState === "visible") void loadFleet(); };
    const visibilityChanged = () => {
      if (document.visibilityState === "visible") void loadFleet();
      else activeRefresh.current?.abort();
    };
    const interval = window.setInterval(refreshVisibleFleet, FLEET_REVALIDATION_INTERVAL_MS);
    window.addEventListener("online", refreshVisibleFleet);
    document.addEventListener("visibilitychange", visibilityChanged);
    void loadFleet(true);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener("online", refreshVisibleFleet);
      document.removeEventListener("visibilitychange", visibilityChanged);
      refreshSequence.current += 1;
      activeRefresh.current?.abort();
      activeRefresh.current = null;
    };
  }, [loadFleet]);
  useEffect(() => { window.scrollTo(0, 0); }, [selectedID]);
  const selected = useMemo(() => devices.find((device) => device.device_id === selectedID) ?? null, [devices, selectedID]);
  const open = (device: DeviceView) => { if (device.uiState === "unlocked" || device.uiState === "needs_attention") { workspaceTab.current = "overview"; setSelectedID(device.device_id); } };

  return <div className="app-frame"><a className="skip-link" href="#main-content">Skip to controls</a><header className="global-header"><div className="wordmark"><span>EX</span><strong>Executor</strong></div><div className="system-legend"><span className="live-dot" />Sovereign host control</div><div className="identity-block"><span>Access subject</span><strong>{session?.access_subject ?? "verifying"}</strong></div></header>
    <main id="main-content" className={selected ? "workspace-main" : "fleet-main"}>
      {operationNotice !== null && <section role="alert" className="panel-shell"><p>{operationNotice}</p><button onClick={() => setOperationNotice(null)}>Dismiss operation notice</button></section>}
      {selected && session ? <DeviceWorkspace device={selected} session={session} onFailure={reportFailure} onTabChange={(tab) => { workspaceTab.current = tab; }} onBack={() => setSelectedID(null)} onLocked={() => { markState(selected.device_id, "locked"); setStatus(`${selected.name} must be unlocked again in this browser.`); setSelectedID(null); }} onOffline={() => { markState(selected.device_id, "offline", "offline"); setStatus(`${selected.name} relay is offline.`); if (workspaceTab.current !== "files") setSelectedID(null); }} onKilled={() => { markState(selected.device_id, "killed", "offline", selected.generation + 1); setStatus(`${selected.name} was killed at generation ${selected.generation + 1}. Resume requires local rescue or executor CLI.`); setSelectedID(null); }} onRotated={() => { markState(selected.device_id, "locked", "online", selected.generation + 1); setStatus(`${selected.name} rotated to generation ${selected.generation + 1}. Unlock this browser again.`); setSelectedID(null); }} onRemoved={() => { setDevices((current) => current.filter((device) => device.device_id !== selected.device_id)); setStatus(`${selected.name} was removed. Re-enroll it locally from the host CLI with a new one-time token.`); setSelectedID(null); }} onSensitive={setOneTimeResult} /> : <><section className="fleet-hero"><div><p className="eyebrow">Unified Dashboard / control plane</p><h1>Owner<br />Command</h1></div><div className="hero-copy"><p>Authenticated AI control across macOS, Windows, Linux, and WSL. Every device unlock belongs to one Access identity and one HttpOnly browser identity.</p><button onClick={() => void loadFleet(true)} disabled={loading}>{loading ? "Synchronizing…" : "Refresh fleet"}</button></div></section><p className="fleet-status" aria-live="polite">{status}</p><DeviceGrid devices={devices} onOpen={open} onUnlock={setUnlocking} /></>}
    </main><footer className="global-footer"><span>Executor Unified Dashboard</span><span>Payload-free audit · local Kill Switch</span><span>{new Date().getFullYear()}</span></footer>
    {unlocking && session && <UnlockDialog device={unlocking} session={session} onDismiss={() => setUnlocking(null)} onUnlocked={() => { markState(unlocking.device_id, "unlocked"); setUnlocking(null); workspaceTab.current = "overview"; setSelectedID(unlocking.device_id); }} />}
    {oneTimeResult && <OneTimeSecretDialog value={oneTimeResult} onDismiss={() => setOneTimeResult(null)} />}
  </div>;
}

function DeviceWorkspace({ device, session, onBack, onLocked, onOffline, onKilled, onRotated, onRemoved, onSensitive, onFailure, onTabChange }: {
  device: DeviceView; session: SessionContext; onFailure: (error: unknown) => void; onTabChange: (tab: WorkspaceTab) => void; onBack: () => void; onLocked: () => void; onOffline: () => void; onKilled: () => void; onRotated: () => void; onRemoved: () => void; onSensitive: (value: SensitiveResult) => void;
}) {
  const [tab, setTab] = useState<WorkspaceTab>("overview");
  const handlers = useRef({ onLocked, onOffline });
  useEffect(() => { handlers.current = { onLocked, onOffline }; }, [onLocked, onOffline]);
  const call: DeviceCall = useCallback(async (method, argumentsValue, signal) => {
    try { return await callDevice(device.device_id, method, argumentsValue, signal); }
    catch (error) {
      if (!signal?.aborted) onFailure(error);
      if (error instanceof DeviceLockedError) handlers.current.onLocked();
      else if (error instanceof DeviceOfflineError) handlers.current.onOffline();
      throw error;
    }
  }, [device.device_id, onFailure]);
  const panel = tab === "overview" ? <OverviewPanel call={call} device={device} /> : tab === "terminal" ? <TerminalPanel call={call} active /> : tab === "files" ? <FilesPanel call={call} /> : tab === "computer" ? <LiveRemoteDesktopPanel call={call} platform={device.platform} /> : tab === "permissions" ? <PermissionsPanel call={call} /> : tab === "control" ? <ControlPanel call={call} device={device} session={session} onSensitive={onSensitive} onKilled={onKilled} onRotated={onRotated} onRemoved={onRemoved} onRemoveFailure={(error) => { onFailure(error); if (error instanceof DeviceLockedError) { onLocked(); return true; } if (error instanceof DeviceOfflineError) { onOffline(); return true; } return false; }} /> : <AuditPanel call={call} />;
  return <><section className="workspace-mast"><button className="back-button" onClick={onBack}>← Fleet</button><div><p className="eyebrow">{device.platform} / {device.arch} / generation {device.generation}</p><h1>{device.name}</h1></div><span className="state-chip" data-state={device.uiState}>{device.uiState.replace("_", " ")}</span></section><WorkspaceTabs active={tab} onChange={(nextTab) => { onTabChange(nextTab); setTab(nextTab); }} />{device.state === "offline" && <p className="status-line" role="status">Relay is offline. Controls are disabled; pending operations are not retried.</p>}<fieldset disabled={device.state === "offline"} style={{ border: 0, padding: 0, margin: 0, minWidth: 0 }}><section id={`panel-${tab}`} role="tabpanel" aria-labelledby={`tab-${tab}`} tabIndex={0}>{panel}</section></fieldset></>;
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw signal.reason instanceof Error ? signal.reason : new DOMException("Operation aborted", "AbortError");
}

function reconcileKilledDevices(current: DeviceView[], fresh: DeviceView[]): DeviceView[] {
  return fresh.map((device) => {
    const previous = current.find((candidate) => candidate.device_id === device.device_id);
    return previous?.uiState === "killed" && device.state === "offline" ? { ...device, uiState: "killed" } : device;
  });
}
