import { describe, expect, it, vi } from "vitest";

import { writeAudit } from "../../src/db";

describe("dashboard audit writes", () => {
  it("does not replace an action result when the secondary D1 audit is unavailable", async () => {
    const db = {
      prepare() {
        return {
          bind() {
            return {
              async run() {
                throw new Error("D1 unavailable");
              },
            };
          },
        };
      },
    } as unknown as D1Database;
    const error = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      await expect(writeAudit(db, "device-1", "subject-1", "device.call", "forwarded", 1)).resolves.toBeUndefined();
      expect(error).toHaveBeenCalledWith("dashboard audit write failed");
    } finally {
      error.mockRestore();
    }
  });
});
