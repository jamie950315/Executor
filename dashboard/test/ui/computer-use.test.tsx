import { StrictMode } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ComputerUsePanel } from "../../src/ui/panels/ComputerUsePanel";
import type { DeviceCall } from "../../src/ui/panels/types";

const screenshot = (captureId = "first") => ({ requestID: captureId, result: {
  structuredContent: { captureId, width: 1000, height: 500, mimeType: "image/png" },
  content: [{ type: "image", data: btoa("png"), mimeType: "image/png" }],
} });

async function observe() {
  fireEvent.click(screen.getByRole("button", { name: "Refresh screen" }));
  const img = await screen.findByRole("img");
  vi.spyOn(img, "getBoundingClientRect").mockReturnValue({ left: 10, top: 20, width: 500, height: 250, right: 510, bottom: 270, x: 10, y: 20, toJSON() {} });
  Object.assign(img, { setPointerCapture: vi.fn(), releasePointerCapture: vi.fn(), hasPointerCapture: () => true });
  return img;
}

function pointer(img: HTMLElement, type: string, x: number, y: number, pointerId = 1) {
  const event = new MouseEvent(type, { bubbles: true, clientX: x, clientY: y, button: 0 });
  Object.defineProperty(event, "pointerId", { value: pointerId });
  fireEvent(img, event);
}

describe("Computer Use capture and gesture safety", () => {
  it("preserves a capture when the parent rerenders with a fresh call callback", async () => {
    const view = render(<ComputerUsePanel call={async () => screenshot()} />);
    await observe();
    fireEvent.click(screen.getByRole("button", { name: "Queue keypress" }));
    view.rerender(<ComputerUsePanel call={async () => screenshot()} />);
    expect(screen.getByRole("img")).toBeVisible();
    expect(screen.getByRole("button", { name: "Confirm 1 action" })).toBeEnabled();
  });

  it("snapshots drag coordinates and selected button before clearing the gesture", async () => {
    const call = vi.fn<DeviceCall>(async () => screenshot());
    render(<StrictMode><ComputerUsePanel call={call} /></StrictMode>);
    const img = await observe();
    fireEvent.click(screen.getByRole("button", { name: "drag" }));
    fireEvent.change(screen.getByLabelText("Button"), { target: { value: "right" } });
    pointer(img, "pointerdown", 20, 30);
    pointer(img, "pointerup", 110, 120);
    expect(screen.getByText(/20,20 → 200,200/u)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Confirm 1 action" }));
    expect(call).toHaveBeenLastCalledWith("desktop_control", expect.objectContaining({ actions: [{ type: "drag", button: "right", path: [{ x: 20, y: 20 }, { x: 200, y: 200 }] }] }), expect.any(AbortSignal));
    await screen.findByText("Actions completed — fresh capture accepted");
  });

  it.each(["pointercancel", "lostpointercapture", "outside", "other-pointer", "no-start"])("discards %s gestures without queuing an edge click", async (ending) => {
    render(<ComputerUsePanel call={async () => screenshot()} />);
    const img = await observe();
    if (ending !== "no-start") pointer(img, "pointerdown", 20, 30);
    if (ending === "pointercancel" || ending === "lostpointercapture") pointer(img, ending, 20, 30);
    pointer(img, "pointerup", ending === "outside" ? 600 : 30, 40, ending === "other-pointer" ? 2 : 1);
    expect(screen.getByText("No actions queued.")).toBeVisible();
    if (ending !== "no-start") expect(img.setPointerCapture).toHaveBeenCalledWith(1);
  });

  it("blocks overlapping calls and queue changes while capturing or controlling", async () => {
    let finish!: (value: ReturnType<typeof screenshot>) => void;
    const call = vi.fn<DeviceCall>().mockResolvedValueOnce(screenshot()).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
    render(<ComputerUsePanel call={call} />);
    const img = await observe();
    fireEvent.click(screen.getByRole("button", { name: "Queue keypress" }));
    const confirm = screen.getByRole("button", { name: "Confirm 1 action" });
    act(() => { fireEvent.click(confirm); fireEvent.click(confirm); });
    expect(call).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Refresh screen" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Queue keypress" })).toBeDisabled();
    pointer(img, "pointerdown", 20, 30); pointer(img, "pointerup", 20, 30);
    expect(screen.getByText("No actions queued.")).toBeVisible();
    await act(async () => finish(screenshot("second")));
    fireEvent.click(screen.getByRole("button", { name: "Queue keypress" }));
    fireEvent.click(screen.getByRole("button", { name: "Refresh screen" }));
    expect(screen.getByRole("button", { name: "Confirm 0 actions" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Queue typing" })).toBeDisabled();
    await act(async () => finish(screenshot("third")));
  });

  it("keeps an uncertain execution visible without automatic refresh or retry", async () => {
    const call = vi.fn<DeviceCall>().mockResolvedValueOnce(screenshot()).mockRejectedValueOnce(new Error("connection lost"));
    render(<ComputerUsePanel call={call} />);
    await observe();
    fireEvent.click(screen.getByRole("button", { name: "Queue keypress" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm 1 action" }));
    expect(await screen.findByText(/Some actions may already have completed/u)).toBeVisible();
    expect(call).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Queue keypress" })).toBeDisabled();
  });

  it("aborts on deactivation and ignores the response even if cancellation is ignored", async () => {
    let finish!: (value: ReturnType<typeof screenshot>) => void;
    let signal: AbortSignal | undefined;
    const call: DeviceCall = async (_method, _args, inputSignal) => { signal = inputSignal; return new Promise(resolve => { finish = resolve; }); };
    const view = render(<ComputerUsePanel call={call} />);
    fireEvent.click(screen.getByRole("button", { name: "Refresh screen" }));
    view.rerender(<ComputerUsePanel call={call} active={false} />);
    expect(signal?.aborted).toBe(true);
    view.rerender(<ComputerUsePanel call={call} active />);
    await act(async () => finish(screenshot("stale")));
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    view.unmount();
  });
});
