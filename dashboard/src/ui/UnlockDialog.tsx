import { useEffect, useRef, useState } from "react";
import type { DeviceView } from "./DeviceGrid";
import type { SessionContext } from "./api";
import { performUnlock } from "./device-actions";
import { useDialogFocus } from "./dialog-focus";

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
  const activeController = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      activeController.current?.abort();
      activeController.current = null;
    };
  }, []);
  const dismiss = () => {
    activeController.current?.abort();
    activeController.current = null;
    setRecoveryKey("");
    if (inputRef.current) inputRef.current.value = "";
    setWorking(false);
    onDismiss();
  };
  const dialog = useDialogFocus(true, dismiss);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); if (working || recoveryKey.length === 0) return;
    const submittedRecoveryKey = recoveryKey;
    const controller = new AbortController();
    activeController.current = controller;
    setRecoveryKey("");
    if (inputRef.current) inputRef.current.value = "";
    setWorking(true); setStatus("Encrypting and verifying on the device…");
    try {
      await performUnlock(device, session, submittedRecoveryKey, controller.signal);
      if (!controller.signal.aborted && activeController.current === controller && mounted.current) onUnlocked();
    } catch {
      if (!controller.signal.aborted && activeController.current === controller && mounted.current) setStatus("Unlock failed. The key was not retained.");
    } finally {
      if (activeController.current === controller) {
        activeController.current = null;
        if (mounted.current) setWorking(false);
      }
    }
  };
  return <div className="dialog-backdrop" role="presentation"><section ref={dialog.containerRef} onKeyDown={dialog.onKeyDown} className="sovereign-dialog" role="dialog" aria-modal="true" aria-labelledby="unlock-title"><p className="eyebrow">Browser-memory unlock</p><h2 id="unlock-title">Unlock {device.name}</h2><p>No recovery key is sent to the Dashboard Worker. It is sealed in this browser to device generation {device.generation}.</p><form onSubmit={(event) => void submit(event)}><label>Recovery key<input ref={inputRef} type="password" autoComplete="off" value={recoveryKey} onChange={(event) => setRecoveryKey(event.target.value)} /></label><p className="status-line" role="status" aria-live="polite">{status}</p><div className="button-row"><button type="button" onClick={dismiss}>Cancel</button><button className="primary-button" type="submit" disabled={working || recoveryKey.length === 0}>{working ? "Unlocking…" : "Unlock device"}</button></div></form></section></div>;
}
