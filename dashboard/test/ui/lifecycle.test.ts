import { describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { runSensitiveLifecycle } from "../../src/ui/lifecycle";
import type { DeviceCall } from "../../src/ui/panels/types";

describe("sensitive lifecycle cancellation", () => {
  it("passes the caller-owned signal to the streaming device call boundary", async () => {
    const controller = new AbortController();
    const call = vi.fn<DeviceCall>(async () => { throw new Error("stop after signal capture"); });
    const pending = runSensitiveLifecycle(
      "control.rotate",
      {
        device_id: "device-1", name: "Owner Mac", platform: "darwin", arch: "arm64", version: "dev",
        mcp_url: "https://device.example/mcp", public_jwk: { kty: "EC", crv: "P-256", x: vectors.device_public_key.x, y: vectors.device_public_key.y },
        generation: 7, state: "online", created_at: 1, updated_at: 1, last_seen_at: 1,
      },
      { access_subject: "access-1", browser_id: "browser-1" },
      call,
      controller.signal,
    );

    await expect(pending).rejects.toThrow("stop after signal capture");
    expect(call).toHaveBeenCalledWith(
      "control.rotate",
      expect.objectContaining({ response_public_key: expect.any(Object) }),
      controller.signal,
    );
  });
});
