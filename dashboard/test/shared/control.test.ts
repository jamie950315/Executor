import { describe, expect, it, vi } from "vitest";

import { disconnectDeletedDevice, isAllowedControlMethod } from "../../src/control";

it("allows authorized live desktop signaling without widening arbitrary methods", () => {
  expect(isAllowedControlMethod("desktop_live")).toBe(true);
  expect(isAllowedControlMethod("desktop_live_arbitrary")).toBe(false);
});

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
