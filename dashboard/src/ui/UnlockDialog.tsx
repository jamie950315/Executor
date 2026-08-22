import { useEffect, useRef, useState } from "react";
import type { DeviceView } from "./DeviceGrid";
import type { SessionContext } from "./api";
import { performUnlock } from "./device-actions";

export function UnlockDialog({ device, session, onUnlocked, onDismiss }: {
  device: DeviceView;
  session: SessionContext;
  onUnlocked: () => void;
  onDismiss: () => void;
}) {
  const [recoveryKey, setRecoveryKey] = useState("");
  const [status, setStatus] = useState("Recovery material is encrypted directly to this device.");
  const [working, setWorking] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => { inputRef.current?.focus(); }, []);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); if (working || recoveryKey.length === 0) return;
    const controller = new AbortController(); setWorking(true); setStatus("Encrypting and verifying on the device…");
    try { await performUnlock(device, session, recoveryKey, controller.signal); onUnlocked(); }
    catch { setStatus("Unlock failed. The key was not retained."); }
    finally { setRecoveryKey(""); if (inputRef.current) inputRef.current.value = ""; setWorking(false); }
  };
  return <div className="dialog-backdrop" role="presentation"><section className="sovereign-dialog" role="dialog" aria-modal="true" aria-labelledby="unlock-title"><p className="eyebrow">Browser-memory unlock</p><h2 id="unlock-title">Unlock {device.name}</h2><p>No recovery key is sent to the Dashboard Worker. It is sealed in this browser to device generation {device.generation}.</p><form onSubmit={(event) => void submit(event)}><label>Recovery key<input ref={inputRef} type="password" autoComplete="off" value={recoveryKey} onChange={(event) => setRecoveryKey(event.target.value)} /></label><p className="status-line" role="status" aria-live="polite">{status}</p><div className="button-row"><button type="button" onClick={onDismiss}>Cancel</button><button className="primary-button" type="submit" disabled={working || recoveryKey.length === 0}>{working ? "Unlocking…" : "Unlock device"}</button></div></form></section></div>;
}
