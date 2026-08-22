import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/shared/**/*.test.ts"],
  },
});
