import { useEffect, useRef } from "react";
import type { SensitiveResult } from "./sensitive-result";

export function OneTimeSecretDialog({ value, onDismiss }: { value: SensitiveResult; onDismiss: () => void }) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    buttonRef.current?.focus();
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onDismiss();
    };
    document.addEventListener("keydown", escape);
    return () => document.removeEventListener("keydown", escape);
  }, [onDismiss]);
  return (
    <div className="dialog-backdrop" role="presentation">
      <section className="sovereign-dialog sensitive-dialog" role="dialog" aria-modal="true" aria-labelledby="secret-title">
        <p className="eyebrow alarm-text">Sensitive / shown once</p>
        <h2 id="secret-title">Save these credentials now</h2>
        <p>They are held only in this open page. Dismissing this window clears them from the interface.</p>
        {value.partial && <p className="alert-strip">The device reported a partial lifecycle result. Save the credentials before local rescue.</p>}
        <dl className="secret-list">
          <div><dt>Recovery key</dt><dd>{value.recovery_key}</dd></div>
          <div><dt>URL secret</dt><dd>{value.url_secret}</dd></div>
          <div><dt>Dashboard</dt><dd>{value.dashboard}</dd></div>
        </dl>
        <button ref={buttonRef} className="primary-button alarm-button" onClick={onDismiss}>
          I saved these credentials
        </button>
      </section>
    </div>
  );
}
