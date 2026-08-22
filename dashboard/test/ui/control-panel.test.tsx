import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

vi.mock("../../src/ui/lifecycle", () => ({
  runSensitiveLifecycle: vi.fn(async () => ({
    recovery_key: "SENSITIVE-NEW-RECOVERY",
    url_secret: "SENSITIVE-NEW-URL",
    dashboard: "SENSITIVE-NEW-DASHBOARD",
  })),
}));

import { ControlPanel } from "../../src/ui/panels/ControlPanel";

describe("Lifecycle controls", () => {
  it("requires acknowledgement plus the exact device name for Kill and keeps Resume disabled while healthy", async () => {
    const onSensitive = vi.fn(); const onKilled = vi.fn();
    const call = vi.fn(async () => ({ requestID: "status", result: { state: "armed", agent: "reachable", broker: "reachable", desktop: "reachable", tunnel: "not monitored" } }));
    render(<ControlPanel
      call={call}
      device={{ device_id: "device-1", name: "Owner Mac", platform: "darwin", arch: "arm64", version: "dev", mcp_url: "https://device.example/mcp", public_jwk: { kty: "EC", crv: "P-256", x: vectors.device_public_key.x, y: vectors.device_public_key.y }, generation: 7, state: "online", created_at: 1, updated_at: 1, last_seen_at: 1 }}
      session={{ access_subject: "access-1", browser_id: "browser-1" }}
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
});
