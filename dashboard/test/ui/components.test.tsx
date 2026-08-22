import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { AuditPanel } from "../../src/ui/panels/AuditPanel";
import { ComputerUsePanel, parseScreenshotResult } from "../../src/ui/panels/ComputerUsePanel";
import { FilesPanel } from "../../src/ui/panels/FilesPanel";
import { PermissionsPanel } from "../../src/ui/panels/PermissionsPanel";
import { TerminalPanel } from "../../src/ui/panels/TerminalPanel";
import { DeviceGrid, type DeviceView } from "../../src/ui/DeviceGrid";
import { OneTimeSecretDialog } from "../../src/ui/OneTimeSecretDialog";
import { WorkspaceTabs } from "../../src/ui/WorkspaceTabs";
import type { DeviceCall } from "../../src/ui/panels/types";

const deviceBase = {
  device_id: "device-vector-1",
  name: "Owner Mac",
  platform: "darwin",
  arch: "arm64",
  version: "0.2.0",
  mcp_url: "https://device.example/mcp",
  public_jwk: { kty: "EC" as const, crv: "P-256" as const, x: vectors.device_public_key.x, y: vectors.device_public_key.y },
  generation: 7,
  state: "online" as const,
  created_at: 1,
  updated_at: 2,
  last_seen_at: 2,
};

describe("Unified Dashboard components", () => {
  it("renders distinct device states and keeps offline devices non-operable", async () => {
    const onOpen = vi.fn();
    const devices: DeviceView[] = [
      { ...deviceBase, uiState: "unlocked" },
      { ...deviceBase, device_id: "locked", name: "Locked Pi", uiState: "locked" },
      { ...deviceBase, device_id: "offline", name: "Offline WSL", state: "offline", uiState: "offline" },
      { ...deviceBase, device_id: "killed", name: "Killed PC", state: "offline", uiState: "killed" },
      { ...deviceBase, device_id: "attention", name: "Attention Linux", uiState: "needs_attention" },
    ];
    render(<DeviceGrid devices={devices} onOpen={onOpen} onUnlock={vi.fn()} />);
    expect(screen.getByText("Unlocked")).toBeVisible();
    expect(screen.getByText("Locked")).toBeVisible();
    expect(screen.getByText("Offline")).toBeVisible();
    expect(screen.getByText("Killed")).toBeVisible();
    expect(screen.getByText("Needs attention")).toBeVisible();
    expect(screen.getByRole("button", { name: /Open Owner Mac/u })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Open Offline WSL/u })).toBeDisabled();
  });

  it("supports arrow-key tab navigation with semantic selected state", async () => {
    function Harness() {
      const [active, setActive] = useState("overview");
      return <WorkspaceTabs active={active} onChange={setActive} />;
    }
    render(<Harness />);
    const overview = screen.getByRole("tab", { name: "Overview" });
    overview.focus();
    fireEvent.keyDown(overview, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Terminal" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Terminal" })).toHaveFocus();
  });

  it("reattaches to a persistent terminal and polls output only while active", async () => {
    const call = vi.fn<DeviceCall>(async (method) => {
      if (method === "terminal_sessions") {
        return { requestID: "list", result: [{ Session: { ID: "session-1", Dir: "/tmp" }, Running: true }] };
      }
      if (method === "terminal_output") {
        return { requestID: "read", result: { Data: btoa("READY\n"), NextCursor: 6, Running: true } };
      }
      return { requestID: "other", result: {} };
    });
    render(<TerminalPanel call={call} active />);
    await waitFor(() => expect(screen.getByRole("button", { name: /session-1/u })).toBeVisible());
    await userEvent.click(screen.getByRole("button", { name: /session-1/u }));
    await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("READY"));
    expect(call).toHaveBeenCalledWith("terminal_output", expect.objectContaining({ sessionId: "session-1" }), expect.any(AbortSignal));
  });

  it("uploads binary files as bounded base64 write/append chunks", async () => {
    const calls: Array<{ method: string; args: unknown }> = [];
    const call: DeviceCall = async (method, args) => {
      calls.push({ method, args });
      if (method === "filesystem_read") return { requestID: "list", result: [] };
      return { requestID: "write", result: { size: 4, encoding: "base64" } };
    };
    render(<FilesPanel call={call} />);
    const file = new File([new Uint8Array([0, 255, 65, 66])], "blob.bin", { type: "application/octet-stream" });
    await userEvent.upload(screen.getByLabelText("Choose file to upload"), file);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(calls.some((entry) => entry.method === "filesystem_write")).toBe(true));
    expect(calls.find((entry) => entry.method === "filesystem_write")?.args).toEqual(
      expect.objectContaining({ action: "write_file", encoding: "base64", content: "AP9BQg==" }),
    );
  });

  it("parses screenshots and carries the latest capture ID into confirmed actions", async () => {
    const screenshot = (captureId: string) => ({
      StructuredContent: { captureId, width: 2, height: 1, mimeType: "image/png" },
      Content: [{ type: "image", data: btoa("png"), mimeType: "image/png" }],
    });
    expect(parseScreenshotResult(screenshot("capture-1"))).toMatchObject({ captureId: "capture-1", width: 2 });
    const call = vi.fn<DeviceCall>(async (method) => ({
      requestID: method,
      result: method === "desktop_control" ? screenshot("capture-2") : screenshot("capture-1"),
    }));
    render(<ComputerUsePanel call={call} />);
    await userEvent.click(screen.getByRole("button", { name: "Refresh screen" }));
    await screen.findByAltText("Current screen capture capture-1");
    await userEvent.type(screen.getByLabelText("Text to type"), "secret text");
    await userEvent.click(screen.getByRole("button", { name: "Queue typing" }));
    expect(screen.getByText("••••••••••• · 11 characters")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Confirm 1 action" }));
    await waitFor(() => expect(screen.getByAltText("Current screen capture capture-2")).toBeVisible());
    expect(call).toHaveBeenCalledWith(
      "desktop_control",
      expect.objectContaining({ action: "batch", captureId: "capture-1" }),
      expect.any(AbortSignal),
    );
  });

  it("distinguishes requested, ready, and restart-required permission states", async () => {
    const call: DeviceCall = async () => ({
      requestID: "permissions",
      result: {
        platform: "darwin",
        requested: true,
        ready: false,
        restart_required: true,
        permissions: [{ id: "screen", label: "Screen Recording", state: "pending", required: true, detail: "Approve in Settings" }],
      },
    });
    render(<PermissionsPanel call={call} />);
    await userEvent.click(screen.getByRole("button", { name: "Check permissions" }));
    expect(await screen.findByText("Requested — restart required")).toBeVisible();
    expect(screen.getByText("pending")).toBeVisible();
    expect(screen.queryByText("Granted")).not.toBeInTheDocument();
  });

  it("dismisses one-time credentials from rendered state", async () => {
    function Harness() {
      const [value, setValue] = useState({
        recovery_key: "SENSITIVE-RECOVERY",
        url_secret: "SENSITIVE-URL",
        dashboard: "SENSITIVE-DASHBOARD",
      });
      return value ? <OneTimeSecretDialog value={value} onDismiss={() => setValue(null as never)} /> : <p>Cleared</p>;
    }
    render(<Harness />);
    expect(screen.getByText("SENSITIVE-RECOVERY")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "I saved these credentials" }));
    expect(screen.queryByText("SENSITIVE-RECOVERY")).not.toBeInTheDocument();
    expect(screen.getByText("Cleared")).toBeVisible();
  });

  it("renders audit allowlisted fields and ignores untrusted detail", async () => {
    const call: DeviceCall = async () => ({
      requestID: "audit",
      result: {
        events: [{ time: "2026-08-23T00:00:00Z", actor: "relay:abc", method: "device_status", outcome: "succeeded", detail: "SENSITIVE_DETAIL" }],
      },
    });
    render(<AuditPanel call={call} />);
    await userEvent.click(screen.getByRole("button", { name: "Refresh audit" }));
    expect(await screen.findByText("device_status")).toBeVisible();
    expect(screen.getByText("relay:abc")).toBeVisible();
    expect(screen.queryByText("SENSITIVE_DETAIL")).not.toBeInTheDocument();
  });
});
