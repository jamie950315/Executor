import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

vi.mock("../../src/ui/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../src/ui/api")>();
  return {
    ...actual,
    fetchSession: vi.fn(),
    fetchDevices: vi.fn(),
    callDevice: vi.fn(),
    removeDevice: vi.fn(),
  };
});

vi.mock("../../src/ui/lifecycle", () => ({
  runSensitiveLifecycle: vi.fn(),
}));

import { App, FLEET_REVALIDATION_INTERVAL_MS } from "../../src/ui/App";
import {
  callDevice,
  DeviceLockedError,
  DeviceOfflineError,
  fetchDevices,
  fetchSession,
  removeDevice,
  type DeviceRecord,
} from "../../src/ui/api";
import { runSensitiveLifecycle } from "../../src/ui/lifecycle";

const device: DeviceRecord = {
  device_id: "device-1",
  name: "Owner Mac",
  platform: "darwin",
  arch: "arm64",
  version: "dev",
  mcp_url: "https://device.example/mcp",
  public_jwk: { kty: "EC", crv: "P-256", x: vectors.device_public_key.x, y: vectors.device_public_key.y },
  generation: 7,
  state: "online",
  created_at: 1,
  updated_at: 1,
  last_seen_at: 1,
};

beforeEach(() => {
  vi.useFakeTimers();
  setVisibility("visible");
  vi.mocked(fetchSession).mockReset().mockResolvedValue({ access_subject: "access-1", browser_id: "browser-1" });
  vi.mocked(fetchDevices).mockReset().mockResolvedValue([device]);
  vi.mocked(callDevice).mockReset().mockResolvedValue({ requestID: "probe", result: { ready: true } });
  vi.mocked(removeDevice).mockReset().mockResolvedValue();
  vi.mocked(runSensitiveLifecycle).mockReset().mockResolvedValue({
    recovery_key: "SENSITIVE-NEW-RECOVERY",
    url_secret: "SENSITIVE-NEW-URL",
    dashboard: "SENSITIVE-NEW-DASHBOARD",
  });
});

afterEach(() => {
  vi.useRealTimers();
  setVisibility("visible");
});

describe("fleet revalidation", () => {
  it("maps expired grants, offline relays, other failures, and proven reconnects without inventing a grant", async () => {
    const view = render(<App />);
    await flushAsyncWork();
    expect(screen.getByText("Unlocked")).toBeVisible();

    vi.mocked(callDevice).mockRejectedValueOnce(new DeviceLockedError());
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(screen.getByRole("button", { name: "Unlock Owner Mac" })).toBeVisible();

    vi.mocked(callDevice).mockRejectedValueOnce(new DeviceOfflineError());
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(screen.getByText("Offline")).toBeVisible();

    vi.mocked(callDevice).mockRejectedValueOnce(new Error("opaque health failure"));
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(screen.getByText("Needs attention")).toBeVisible();

    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(screen.getByText("Unlocked")).toBeVisible();
    view.unmount();
  });

  it("polls only at a bounded visible interval, refreshes on visibility/online, and cleans up", async () => {
    const view = render(<App />);
    await flushAsyncWork();
    expect(fetchDevices).toHaveBeenCalledTimes(1);
    expect(fetchSession).toHaveBeenCalledWith(expect.any(AbortSignal));

    await act(async () => { await vi.advanceTimersByTimeAsync(FLEET_REVALIDATION_INTERVAL_MS - 1); });
    expect(fetchDevices).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    await flushAsyncWork();
    expect(fetchDevices).toHaveBeenCalledTimes(2);

    setVisibility("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await act(async () => { await vi.advanceTimersByTimeAsync(FLEET_REVALIDATION_INTERVAL_MS * 3); });
    expect(fetchDevices).toHaveBeenCalledTimes(2);

    setVisibility("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await flushAsyncWork();
    expect(fetchDevices).toHaveBeenCalledTimes(3);
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(fetchDevices).toHaveBeenCalledTimes(4);

    let inFlightSignal: AbortSignal | undefined;
    vi.mocked(fetchDevices).mockImplementationOnce(async (signal) => {
      inFlightSignal = signal;
      return new Promise(() => undefined);
    });
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(fetchDevices).toHaveBeenCalledTimes(5);
    view.unmount();
    expect(inFlightSignal?.aborted).toBe(true);

    window.dispatchEvent(new Event("online"));
    document.dispatchEvent(new Event("visibilitychange"));
    await act(async () => { await vi.advanceTimersByTimeAsync(FLEET_REVALIDATION_INTERVAL_MS); });
    expect(fetchDevices).toHaveBeenCalledTimes(5);
  });

  it("aborts and ignores a stale fleet refresh that finishes after a newer refresh", async () => {
    const view = render(<App />);
    await flushAsyncWork();

    let staleSignal: AbortSignal | undefined;
    let finishStale: ((devices: DeviceRecord[]) => void) | undefined;
    vi.mocked(fetchDevices).mockImplementationOnce(async (signal) => {
      staleSignal = signal;
      return new Promise<DeviceRecord[]>((resolve) => { finishStale = resolve; });
    });
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();

    vi.mocked(fetchDevices).mockResolvedValueOnce([{ ...device, name: "Current Mac" }]);
    window.dispatchEvent(new Event("online"));
    await flushAsyncWork();
    expect(staleSignal?.aborted).toBe(true);
    expect(screen.getByText("Current Mac")).toBeVisible();

    await act(async () => { finishStale?.([{ ...device, name: "Stale Mac" }]); });
    await flushAsyncWork();
    expect(screen.queryByText("Stale Mac")).not.toBeInTheDocument();
    expect(screen.getByText("Current Mac")).toBeVisible();
    view.unmount();
  });

  it.each([
    ["locked", new DeviceLockedError(), "Unlock Owner Mac"],
    ["offline", new DeviceOfflineError(), "Offline"],
  ])("returns the workspace to the fleet when a call reports the device %s", async (_state, error, expected) => {
    const view = render(<App />);
    await flushAsyncWork();
    vi.mocked(callDevice).mockRejectedValue(error);

    fireEvent.click(screen.getByRole("button", { name: "Open Owner Mac" }));
    await flushAsyncWork();
    expect(screen.queryByRole("heading", { name: "Overview" })).not.toBeInTheDocument();
    if (_state === "locked") expect(screen.getByRole("button", { name: expected })).toBeVisible();
    else expect(screen.getByText(expected)).toBeVisible();
    view.unmount();
  });

  it.each([
    ["locked", new DeviceLockedError(), "Unlock Owner Mac"],
    ["offline", new DeviceOfflineError(), "Offline"],
  ])("returns the real Control Remove flow to the fleet when deletion reports %s", async (_state, error, expected) => {
    vi.mocked(removeDevice).mockRejectedValueOnce(error);
    const view = render(<App />);
    await flushAsyncWork();
    await openControlPanel();

    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    fireEvent.change(screen.getByLabelText("Type device name exactly"), { target: { value: device.name } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
    await flushAsyncWork();

    expect(screen.queryByRole("tab", { name: "Control" })).not.toBeInTheDocument();
    if (_state === "locked") expect(screen.getByRole("button", { name: expected })).toBeVisible();
    else expect(screen.getByText(expected)).toBeVisible();
    view.unmount();
  });

  it("keeps the workspace and shows only the fixed failure when Remove has an unrelated error", async () => {
    vi.mocked(removeDevice).mockRejectedValueOnce(new Error("sensitive upstream detail"));
    const view = render(<App />);
    await flushAsyncWork();
    await openControlPanel();

    fireEvent.click(screen.getByRole("button", { name: "Remove device" }));
    fireEvent.change(screen.getByLabelText("Type device name exactly"), { target: { value: device.name } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
    await flushAsyncWork();

    expect(screen.getByRole("tab", { name: "Control" })).toBeVisible();
    expect(screen.getByText("Lifecycle action failed safely")).toBeVisible();
    expect(screen.queryByText("sensitive upstream detail")).not.toBeInTheDocument();
    view.unmount();
  });

  it("returns to the killed fleet and retains the one-time secret after a successful Kill", async () => {
    const view = render(<App />);
    await flushAsyncWork();
    await openControlPanel();

    fireEvent.click(screen.getByRole("button", { name: "Kill Executor" }));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.change(screen.getByLabelText("Type device name exactly"), { target: { value: device.name } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm Kill" }));
    await flushAsyncWork();

    expect(screen.queryByRole("tab", { name: "Control" })).not.toBeInTheDocument();
    expect(screen.getByText("Killed")).toBeVisible();
    expect(screen.getByRole("dialog", { name: "Save these credentials now" })).toBeVisible();
    view.unmount();
  });
});

async function openControlPanel(): Promise<void> {
  fireEvent.click(screen.getByRole("button", { name: "Open Owner Mac" }));
  await flushAsyncWork();
  fireEvent.click(screen.getByRole("tab", { name: "Control" }));
  await flushAsyncWork();
}

async function flushAsyncWork(): Promise<void> {
  await act(async () => {
    for (let index = 0; index < 10; index += 1) await Promise.resolve();
  });
}

function setVisibility(state: DocumentVisibilityState): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, value: state });
}
