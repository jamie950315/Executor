import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("production Static Assets routing", () => {
  it("runs every asset request through the Worker security-header fallback", () => {
    const config = readFileSync(new URL("../../wrangler.jsonc", import.meta.url), "utf8");
    expect(config).toMatch(/"run_worker_first"\s*:\s*true/u);
    expect(config).not.toMatch(/"run_worker_first"\s*:\s*\[/u);
  });
});
