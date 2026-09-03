import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent } from "react";

const focusableSelector = [
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "a[href]",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

export function useDialogFocus(active: boolean, onDismiss: () => void) {
  const containerRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!active) return;
    focusable(containerRef.current)[0]?.focus();
  }, [active]);
  const onKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      onDismiss();
      return;
    }
    if (event.key !== "Tab") return;
    const items = focusable(event.currentTarget);
    const first = items[0]; const last = items.at(-1);
    if (!first || !last) { event.preventDefault(); return; }
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  };
  return { containerRef, onKeyDown };
}

function focusable(container: HTMLElement | null): HTMLElement[] {
  return container ? Array.from(container.querySelectorAll<HTMLElement>(focusableSelector)) : [];
}
