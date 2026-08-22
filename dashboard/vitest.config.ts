import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [
    cloudflareTest({
      miniflare: {
        bindings: {
          ACCESS_AUD: "test-access-audience",
          ACCESS_TEAM_DOMAIN: "executor-test.cloudflareaccess.com",
          ENROLLMENT_TOKEN_HASH: "test-only-overridden-by-test-setup",
        },
      },
      wrangler: {
        configPath: "./wrangler.jsonc",
      },
    }),
  ],
  test: {
    include: ["test/worker/**/*.test.ts"],
  },
});
