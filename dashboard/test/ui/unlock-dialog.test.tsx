import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

vi.mock("../../src/ui/device-actions", () => ({
  performUnlock: vi.fn(),
}));

import { UnlockDialog } from "../../src/ui/UnlockDialog";
import { performUnlock } from "../../src/ui/device-actions";

const device = {
  device_id: "device-1", name: "Owner Mac", platform: "darwin", arch: "arm64", version: "dev",
  mcp_url: "https://device.example/mcp", public_jwk: { kty: "EC" as const, crv: "P-256" as const, x: vectors.device_public_key.x, y: vectors.device_public_key.y },
  generation: 7, state: "online" as const, created_at: 1, updated_at: 1, last_seen_at: 1, uiState: "locked" as const,
};

beforeEach(() => {
  vi.mocked(performUnlock).mockReset().mockRejectedValue(new Error("fixed failure"));
});

describe("Unlock dialog", () => {
  it("traps keyboard focus and closes on Escape", async () => {
    const onDismiss = vi.fn();
    render(<UnlockDialog
      device={device}
      session={{ access_subject: "access-1", browser_id: "browser-1" }}
      onUnlocked={vi.fn()}
      onDismiss={onDismiss}
    />);
    const input = screen.getByLabelText("Recovery key");
    expect(input).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("clears recovery input and reports only a fixed failure after every submission", async () => {
    const marker = "SENSITIVE-RECOVERY-INPUT";
    render(<UnlockDialog
      device={device}
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

  it("aborts the caller-owned unlock request on dismiss and ignores its stale completion", async () => {
    let signal: AbortSignal | undefined;
    let finish: (() => void) | undefined;
    vi.mocked(performUnlock).mockImplementation(async (_device, _session, _recoveryKey, nextSignal) => {
      signal = nextSignal;
      await new Promise<void>((resolve) => { finish = resolve; });
    });
    const onDismiss = vi.fn();
    const onUnlocked = vi.fn();
    render(<UnlockDialog device={device} session={{ access_subject: "access-1", browser_id: "browser-1" }} onUnlocked={onUnlocked} onDismiss={onDismiss} />);

    await userEvent.type(screen.getByLabelText("Recovery key"), "SENSITIVE-RECOVERY");
    await userEvent.click(screen.getByRole("button", { name: "Unlock device" }));
    await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(signal?.aborted).toBe(true);
    expect(onDismiss).toHaveBeenCalledOnce();
    await act(async () => { finish?.(); });
    expect(onUnlocked).not.toHaveBeenCalled();
  });

  it("aborts an active unlock request on unmount", async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(performUnlock).mockImplementation(async (_device, _session, _recoveryKey, nextSignal) => {
      signal = nextSignal;
      await new Promise<void>(() => undefined);
    });
    const onUnlocked = vi.fn();
    const view = render(<UnlockDialog device={device} session={{ access_subject: "access-1", browser_id: "browser-1" }} onUnlocked={onUnlocked} onDismiss={vi.fn()} />);

    await userEvent.type(screen.getByLabelText("Recovery key"), "SENSITIVE-RECOVERY");
    await userEvent.click(screen.getByRole("button", { name: "Unlock device" }));
    await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));
    view.unmount();

    expect(signal?.aborted).toBe(true);
    expect(onUnlocked).not.toHaveBeenCalled();
  });

  it("completes unlock after StrictMode replays the mount effect", async () => {
    vi.mocked(performUnlock).mockResolvedValue();
    const onUnlocked = vi.fn();
    render(<StrictMode><UnlockDialog device={device} session={{ access_subject: "access-1", browser_id: "browser-1" }} onUnlocked={onUnlocked} onDismiss={vi.fn()} /></StrictMode>);

    await userEvent.type(screen.getByLabelText("Recovery key"), "SENSITIVE-RECOVERY");
    await userEvent.click(screen.getByRole("button", { name: "Unlock device" }));

    await waitFor(() => expect(onUnlocked).toHaveBeenCalledOnce());
  });
});
