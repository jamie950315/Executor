import { describe, expect, it, vi } from "vitest";

import { disconnectDeletedDevice } from "../../src/control";

describe("deleted device relay cleanup", () => {
  it("does not replace a completed registry deletion when relay cleanup fails", async () => {
    const relay = {
      async disconnect(): Promise<void> {
        throw new Error("Durable Object unavailable");
      },
    };
    const error = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      await expect(disconnectDeletedDevice(relay, 7)).resolves.toBeUndefined();
      expect(error).toHaveBeenCalledWith("deleted device relay disconnect failed");
    } finally {
      error.mockRestore();
    }
  });
});
