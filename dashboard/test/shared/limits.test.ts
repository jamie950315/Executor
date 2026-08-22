import { describe, expect, it } from "vitest";

import { boundedResponseTotal } from "../../src/limits";

describe("relay response bounds", () => {
  it("accepts 16 MiB messages through 64 MiB total and rejects either next byte", () => {
    expect(boundedResponseTotal(48 * 1024 * 1024, 16 * 1024 * 1024)).toBe(64 * 1024 * 1024);
    expect(boundedResponseTotal(48 * 1024 * 1024, 16 * 1024 * 1024 + 1)).toBeNull();
    expect(boundedResponseTotal(64 * 1024 * 1024, 1)).toBeNull();
  });
});
