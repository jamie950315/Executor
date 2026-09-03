import { useEffect, useRef, useState } from "react";
import type { DeviceRecord, SessionContext } from "../api";
import { removeDevice } from "../api";
import { runSensitiveLifecycle } from "../lifecycle";
import type { SensitiveResult } from "../sensitive-result";
import type { PanelProps } from "./types";
import { useDialogFocus } from "../dialog-focus";

type ConfirmMode = "rotate" | "kill" | "remove" | null;

export function ControlPanel({ call, device, session, onSensitive, onKilled, onRotated, onRemoved, onRemoveFailure }: PanelProps & {
  device: DeviceRecord;
  session: SessionContext;
  onSensitive: (value: SensitiveResult) => void;
  onKilled: () => void;
  onRotated: () => void;
  onRemoved: () => void;
  onRemoveFailure: (error: unknown) => boolean;
}) {
  const [snapshot, setSnapshot] = useState<Record<string, unknown> | null>(null);
  const [status, setStatus] = useState("Lifecycle status not loaded");
  const [confirm, setConfirm] = useState<ConfirmMode>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [typedName, setTypedName] = useState("");
  const [working, setWorking] = useState(false);
  const activeLifecycle = useRef<AbortController | null>(null);
  const activeStatus = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const finishConfirm = () => { setConfirm(null); setAcknowledged(false); setTypedName(""); setWorking(false); };
  const closeConfirm = () => {
    const active = activeLifecycle.current;
    if (active !== null) {
      active.abort();
      activeLifecycle.current = null;
      if (mounted.current) setStatus("Stopped waiting and requested best-effort cancellation. The host action may already have completed and cannot be undone here.");
    }
    if (mounted.current) finishConfirm();
  };
  const dialog = useDialogFocus(confirm !== null, () => closeConfirm());
  const load = async () => {
    activeStatus.current?.abort();
    const controller = new AbortController();
    activeStatus.current = controller;
    try {
      const response = await call("control.status", {}, controller.signal);
      const next = record(response.result);
      if (!next) throw new Error();
      if (!controller.signal.aborted && activeStatus.current === controller && mounted.current) {
        setSnapshot(next); setStatus("Lifecycle status verified");
      }
    } catch {
      if (!controller.signal.aborted && activeStatus.current === controller && mounted.current) {
        setSnapshot(null); setStatus("Lifecycle status unavailable");
      }
    } finally {
      if (activeStatus.current === controller) activeStatus.current = null;
    }
  };
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
      activeStatus.current?.abort();
      activeStatus.current = null;
      activeLifecycle.current?.abort();
      activeLifecycle.current = null;
    };
  }, []);
  const run = async () => {
    if (!confirm || working) return;
    const mode = confirm;
    const controller = new AbortController();
    activeLifecycle.current = controller;
    setWorking(true);
    try {
      if (mode === "rotate" || mode === "kill") {
        const value = await runSensitiveLifecycle(mode === "rotate" ? "control.rotate" : "control.kill", device, session, call, controller.signal);
        if (controller.signal.aborted || activeLifecycle.current !== controller || !mounted.current) return;
        activeLifecycle.current = null;
        if (mode === "kill") setStatus("Killed. Remote Resume is unavailable; use local rescue or executor CLI.");
        else setStatus("Credentials rotated. This browser must unlock the new generation again.");
        finishConfirm();
        onSensitive(value);
        if (mode === "kill") onKilled();
        else onRotated();
      } else {
        await removeDevice(device.device_id, controller.signal);
        if (controller.signal.aborted || activeLifecycle.current !== controller || !mounted.current) return;
        activeLifecycle.current = null;
        setStatus("Device removed. Re-enroll from the host CLI with a new one-time enrollment token.");
        finishConfirm();
        onRemoved();
      }
    } catch (error) {
      if (!controller.signal.aborted && activeLifecycle.current === controller && mounted.current) {
        if (mode !== "remove" || !onRemoveFailure(error)) setStatus("Lifecycle action failed safely");
      }
    } finally {
      if (activeLifecycle.current === controller) {
        activeLifecycle.current = null;
        if (mounted.current) setWorking(false);
      }
    }
  };
  const lifecycleState = typeof snapshot?.state === "string" ? snapshot.state : "unknown";
  const healthy = lifecycleState === "armed" && ["agent", "broker", "desktop"].every((key) => snapshot?.[key] === "reachable");
  const canResume = device.state === "online" && snapshot !== null && !healthy;
  const resume = async () => { if (!canResume) return; try { await call("control.resume", {}, new AbortController().signal); await load(); } catch { setStatus("Remote Resume failed. Use local rescue if relay access is lost."); } };
  const confirmed = confirm === "rotate" ? acknowledged : confirm === "kill" ? acknowledged && typedName === device.name : confirm === "remove" ? typedName === device.name : false;
  return <section className="panel-shell control-panel" aria-labelledby="control-title"><header className="panel-heading"><div><p className="eyebrow alarm-text">Owner authority boundary</p><h2 id="control-title">Control</h2></div><button onClick={() => void load()}>Refresh status</button></header><p className="status-line" aria-live="polite">{status}</p>
    <div className="metric-strip"><span>Lifecycle <b>{lifecycleState}</b></span><span>Relay <b>{device.state}</b></span><span>Generation <b>{device.generation}</b></span></div>
    <div className="control-actions"><article><p className="eyebrow">Credential maintenance</p><h3>Rotate</h3><p>Stops, rotates every Executor-owned credential, resumes services, and returns replacement material encrypted to this browser.</p><button onClick={() => setConfirm("rotate")}>Rotate credentials</button></article><article className="danger-zone"><p className="eyebrow">Emergency stop</p><h3>Kill</h3><p>Stops the tunnel and sessions, rotates credentials, and leaves the host disabled. Remote Resume will not be possible after relay shutdown.</p><button className="alarm-button" onClick={() => setConfirm("kill")}>Kill Executor</button></article><article><p className="eyebrow">Degraded online only</p><h3>Resume</h3><p>Available only while the relay remains online and status is degraded. A fully killed host requires local rescue or CLI.</p><button disabled={!canResume} onClick={() => void resume()}>Resume services</button></article><article><p className="eyebrow">Registry</p><h3>Remove</h3><p>Revokes the Dashboard registry entry and browser grant. Re-enrollment must start locally from the host CLI.</p><button className="danger-ghost" onClick={() => setConfirm("remove")}>Remove device</button></article></div>
    {confirm && <div className="dialog-backdrop" role="presentation"><section ref={dialog.containerRef} onKeyDown={dialog.onKeyDown} className="sovereign-dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-title"><p className="eyebrow alarm-text">Double confirmation</p><h2 id="confirm-title">{confirm === "rotate" ? "Rotate every credential?" : confirm === "kill" ? `Kill ${device.name}?` : `Remove ${device.name}?`}</h2><p>{confirm === "kill" ? "The relay will go offline. Central Resume becomes unavailable; save the encrypted one-time recovery result and use local rescue when needed." : confirm === "rotate" ? "All grants become invalid immediately. Save the one-time result, then unlock the new generation." : "This removes the Dashboard record; it does not uninstall Executor from the host."}</p>{confirm !== "remove" && <p>Closing this dialog stops waiting and requests best-effort cancellation. It cannot undo a host action that already completed.</p>}{confirm !== "remove" && <label className="check-row"><input type="checkbox" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} />I understand the one-time credential and access consequences.</label>}{confirm !== "rotate" && <label>Type device name exactly<input value={typedName} onChange={(event) => setTypedName(event.target.value)} autoComplete="off" placeholder={device.name} /></label>}<div className="button-row"><button onClick={closeConfirm}>{working ? "Stop waiting" : "Cancel"}</button><button className={confirm === "kill" ? "alarm-button" : "primary-button"} disabled={!confirmed || working} onClick={() => void run()}>{working ? "Working…" : confirm === "rotate" ? "Confirm rotate" : confirm === "kill" ? "Confirm Kill" : "Confirm remove"}</button></div></section></div>}
  </section>;
}
function record(value: unknown): Record<string, unknown> | null { return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null; }
