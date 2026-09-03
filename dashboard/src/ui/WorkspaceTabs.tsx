import { useRef } from "react";

export const workspaceTabs = [
  ["overview", "Overview"],
  ["terminal", "Terminal"],
  ["files", "Files"],
  ["computer", "Computer Use"],
  ["permissions", "Permissions"],
  ["control", "Control"],
  ["audit", "Audit"],
] as const;

export type WorkspaceTab = (typeof workspaceTabs)[number][0];

export function WorkspaceTabs({ active, onChange }: { active: string; onChange: (tab: WorkspaceTab) => void }) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  const move = (index: number, key: string) => {
    let target: number;
    if (key === "ArrowRight") target = (index + 1) % workspaceTabs.length;
    else if (key === "ArrowLeft") target = (index - 1 + workspaceTabs.length) % workspaceTabs.length;
    else if (key === "Home") target = 0;
    else if (key === "End") target = workspaceTabs.length - 1;
    else return;
    const tab = workspaceTabs[target];
    if (tab === undefined) return;
    refs.current[target]?.focus();
    onChange(tab[0]);
  };
  return (
    <nav className="workspace-tabs" aria-label="Device workspace">
      <div role="tablist" aria-label="Device tools">
        {workspaceTabs.map(([id, label], index) => (
          <button
            key={id}
            ref={(element) => { refs.current[index] = element; }}
            id={`tab-${id}`}
            aria-label={label}
            role="tab"
            type="button"
            aria-selected={active === id}
            aria-controls={`panel-${id}`}
            tabIndex={active === id ? 0 : -1}
            onClick={() => onChange(id)}
            onKeyDown={(event) => move(index, event.key)}
          >
            <span>{String(index + 1).padStart(2, "0")}</span>{label}
          </button>
        ))}
      </div>
    </nav>
  );
}
