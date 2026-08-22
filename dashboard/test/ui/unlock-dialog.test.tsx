import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

vi.mock("../../src/ui/device-actions", () => ({
  performUnlock: vi.fn(async () => Promise.reject(new Error("fixed failure"))),
}));

import { UnlockDialog } from "../../src/ui/UnlockDialog";

describe("Unlock dialog", () => {
  it("clears recovery input and reports only a fixed failure after every submission", async () => {
    const marker = "SENSITIVE-RECOVERY-INPUT";
    render(<UnlockDialog
      device={{
        device_id: "device-1", name: "Owner Mac", platform: "darwin", arch: "arm64", version: "dev",
        mcp_url: "https://device.example/mcp", public_jwk: { kty: "EC", crv: "P-256", x: vectors.device_public_key.x, y: vectors.device_public_key.y },
        generation: 7, state: "online", created_at: 1, updated_at: 1, last_seen_at: 1, uiState: "locked",
      }}
      session={{ access_subject: "access-1", browser_id: "browser-1" }}
      onUnlocked={vi.fn()}
      onDismiss={vi.fn()}
    />);
    const input = screen.getByLabelText("Recovery key");
    await userEvent.type(input, marker);
    await userEvent.click(screen.getByRole("button", { name: "Unlock device" }));
    await waitFor(() => expect(input).toHaveValue(""));
    expect(screen.getByRole("status")).not.toHaveTextContent(marker);
    expect(screen.getByText("Unlock failed. The key was not retained.")).toBeVisible();
  });
});
