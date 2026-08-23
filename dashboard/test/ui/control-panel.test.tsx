import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

vi.mock("../../src/ui/lifecycle", () => ({
  runSensitiveLifecycle: vi.fn(),
}));

import { ControlPanel } from "../../src/ui/panels/ControlPanel";
import { runSensitiveLifecycle } from "../../src/ui/lifecycle";
import type { SensitiveResult } from "../../src/ui/sensitive-result";

const device = { device_id: "device-1", name: "Owner Mac", platform: "darwin", arch: "arm64", version: "dev", mcp_url: "https://device.example/mcp", public_jwk: { kty: "EC" as const, crv: "P-256" as const, x: vectors.device_public_key.x, y: vectors.device_public_key.y }, generation: 7, state: "online" as const, created_at: 1, updated_at: 1, last_seen_at: 1 };
const session = { access_subject: "access-1", browser_id: "browser-1" };

beforeEach(() => {
  vi.mocked(runSensitiveLifecycle).mockReset().mockResolvedValue({
    recovery_key: "SENSITIVE-NEW-RECOVERY",
    url_secret: "SENSITIVE-NEW-URL",
    dashboard: "SENSITIVE-NEW-DASHBOARD",
  });
});

function renderControl(overrides: { onSensitive?: (value: SensitiveResult) => void; onKilled?: () => void; onRotated?: () => void } = {}) {
  const call = vi.fn(async () => ({ requestID: "status", result: { state: "armed", agent: "reachable", broker: "reachable", desktop: "reachable", tunnel: "not monitored" } }));
  const callbacks = {
    onSensitive: overrides.onSensitive ?? vi.fn(),
    onKilled: overrides.onKilled ?? vi.fn(),
    onRotated: overrides.onRotated ?? vi.fn(),
  };
  const view = render(<ControlPanel call={call} device={device} session={session} {...callbacks} onRemoved={vi.fn()} />);
  return { ...view, call };
}

describe("Lifecycle controls", () => {
  it("requires acknowledgement plus the exact device name for Kill and keeps Resume disabled while healthy", async () => {
    const onSensitive = vi.fn(); const onKilled = vi.fn();
    const call = vi.fn(async () => ({ requestID: "status", result: { state: "armed", agent: "reachable", broker: "reachable", desktop: "reachable", tunnel: "not monitored" } }));
    render(<ControlPanel
      call={call}
      device={device}
      session={session}
      onSensitive={onSensitive} onKilled={onKilled} onRotated={vi.fn()} onRemoved={vi.fn()}
    />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Resume services" })).toBeDisabled());
    await userEvent.click(screen.getByRole("button", { name: "Kill Executor" }));
    const confirm = screen.getByRole("button", { name: "Confirm Kill" });
    expect(confirm).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.type(screen.getByLabelText("Type device name exactly"), "Owner Ma");
    expect(confirm).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Type device name exactly"), "c");
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    await waitFor(() => expect(onKilled).toHaveBeenCalledOnce());
    expect(onSensitive).toHaveBeenCalledWith(expect.objectContaining({ recovery_key: "SENSITIVE-NEW-RECOVERY" }));
    expect(screen.getByText(/Remote Resume is unavailable/u)).toBeVisible();
  });

  it("aborts Rotate on explicit dismiss and does not publish a stale sensitive result", async () => {
    let signal: AbortSignal | undefined;
    let finish: ((value: SensitiveResult) => void) | undefined;
    vi.mocked(runSensitiveLifecycle).mockImplementation(async (_method, _device, _session, _call, nextSignal) => {
      signal = nextSignal;
      return new Promise((resolve) => { finish = resolve; });
    });
    const onSensitive = vi.fn();
    const onRotated = vi.fn();
    renderControl({ onSensitive, onRotated });

    await userEvent.click(screen.getByRole("button", { name: "Rotate credentials" }));
    expect(screen.getByText(/stops waiting and requests best-effort cancellation/u)).toBeVisible();
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm rotate" }));
    await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));
    await userEvent.click(screen.getByRole("button", { name: "Stop waiting" }));

    expect(signal?.aborted).toBe(true);
    await act(async () => { finish?.({ recovery_key: "STALE-RECOVERY", url_secret: "STALE-URL", dashboard: "STALE-DASHBOARD" }); });
    expect(onSensitive).not.toHaveBeenCalled();
    expect(onRotated).not.toHaveBeenCalled();
  });

  it("aborts Kill when workspace navigation unmounts the control surface", async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(runSensitiveLifecycle).mockImplementation(async (_method, _device, _session, _call, nextSignal) => {
      signal = nextSignal;
      return new Promise(() => undefined);
    });
    const onKilled = vi.fn();
    const view = renderControl({ onKilled });

    await userEvent.click(screen.getByRole("button", { name: "Kill Executor" }));
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.type(screen.getByLabelText("Type device name exactly"), device.name);
    await userEvent.click(screen.getByRole("button", { name: "Confirm Kill" }));
    await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));
    view.unmount();

    expect(signal?.aborted).toBe(true);
    expect(onKilled).not.toHaveBeenCalled();
  });

  it("completes Rotate after StrictMode replays the mount effect", async () => {
    const onRotated = vi.fn();
    const onSensitive = vi.fn();
    const call = vi.fn(async () => ({ requestID: "status", result: { state: "armed", agent: "reachable", broker: "reachable", desktop: "reachable" } }));
    render(<StrictMode><ControlPanel call={call} device={device} session={session} onSensitive={onSensitive} onKilled={vi.fn()} onRotated={onRotated} onRemoved={vi.fn()} /></StrictMode>);

    await userEvent.click(screen.getByRole("button", { name: "Rotate credentials" }));
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm rotate" }));

    await waitFor(() => expect(onRotated).toHaveBeenCalledOnce());
    expect(onSensitive).toHaveBeenCalledOnce();
  });
});
