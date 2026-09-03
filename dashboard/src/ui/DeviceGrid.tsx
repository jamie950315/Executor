import type { DeviceRecord } from "./api";

export type DeviceUIState = "locked" | "unlocked" | "offline" | "killed" | "needs_attention";
export interface DeviceView extends DeviceRecord {
  uiState: DeviceUIState;
}

const stateLabels: Record<DeviceUIState, string> = {
  locked: "Locked",
  unlocked: "Unlocked",
  offline: "Offline",
  killed: "Killed",
  needs_attention: "Needs attention",
};

export function DeviceGrid({
  devices,
  onOpen,
  onUnlock,
}: {
  devices: DeviceView[];
  onOpen: (device: DeviceView) => void;
  onUnlock: (device: DeviceView) => void;
}) {
  if (devices.length === 0) {
    return (
      <section className="empty-deck" aria-labelledby="empty-title">
        <p className="eyebrow">Fleet registry / 000</p>
        <h2 id="empty-title">No enrolled devices</h2>
        <p>Enroll Executor from a host CLI. Devices appear here after their signed relay handshake.</p>
      </section>
    );
  }
  return (
    <section className="device-grid" aria-label="Enrolled devices">
      {devices.map((device, index) => {
        const canOpen = device.uiState === "unlocked" || device.uiState === "needs_attention";
        const canUnlock = device.uiState === "locked";
        return (
          <article className={`device-card state-${device.uiState}`} key={device.device_id}>
            <div className="card-index" aria-hidden="true">{String(index + 1).padStart(2, "0")}</div>
            <header>
              <div>
                <p className="eyebrow">{device.platform} / {device.arch}</p>
                <h2>{device.name}</h2>
              </div>
              <span className="state-chip" data-state={device.uiState}>{stateLabels[device.uiState]}</span>
            </header>
            <dl className="device-facts">
              <div><dt>Executor</dt><dd>{device.version}</dd></div>
              <div><dt>Generation</dt><dd>{device.generation}</dd></div>
              <div><dt>Last seen</dt><dd>{formatLastSeen(device.last_seen_at)}</dd></div>
              <div className="wide"><dt>MCP URL</dt><dd>{device.mcp_url}</dd></div>
            </dl>
            <footer>
              {canUnlock ? (
                <button className="primary-button" onClick={() => onUnlock(device)}>Unlock {device.name}</button>
              ) : (
                <button className="primary-button" disabled={!canOpen} onClick={() => onOpen(device)}>
                  Open {device.name}
                </button>
              )}
              <span className="relay-indicator" data-online={device.state === "online"}>
                {device.state === "online" ? "Relay live" : "Relay down"}
              </span>
            </footer>
          </article>
        );
      })}
    </section>
  );
}

function formatLastSeen(value: number | null): string {
  if (value === null) return "Never";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "Unknown" : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}
