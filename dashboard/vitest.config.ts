import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-pool-workers";
import type { D1Migration } from "@cloudflare/vitest-pool-workers";
import { exportJWK, generateKeyPair, type JWK } from "jose";
import { defineConfig } from "vitest/config";

const migrations = await readD1Migrations("./migrations");
const accessKeyPair = await generateKeyPair("RS256", { extractable: true });
const accessPrivateJWK = await exportJWK(accessKeyPair.privateKey);
const accessPublicJWK = {
  ...(await exportJWK(accessKeyPair.publicKey)),
  alg: "RS256",
  kid: "access-test-key",
};

declare module "vitest" {
  export interface ProvidedContext {
    migrations: D1Migration[];
    accessPrivateJWK: JWK;
  }
}

export default defineConfig({
  plugins: [
    cloudflareTest({
      miniflare: {
        bindings: {
          ACCESS_AUD: "test-access-audience",
          ACCESS_TEAM_DOMAIN: "executor-test.cloudflareaccess.com",
          ENROLLMENT_TOKEN_HASH: "7fb382418ab510954968bb4e53ba167ce3417b70d5b0ada2db2dd70fe0c0a6c3",
        },
        outboundService: async (request) => {
          const url = new URL(request.url);
          if (
            url.origin === "https://executor-test.cloudflareaccess.com" &&
            url.pathname === "/cdn-cgi/access/certs"
          ) {
            return new Response(JSON.stringify({ keys: [accessPublicJWK] }), {
              headers: { "content-type": "application/json" },
            });
          }
          return new Response("outbound request rejected", { status: 502 });
        },
      },
      wrangler: {
        configPath: "./wrangler.test.jsonc",
      },
    }),
  ],
  test: {
    include: ["test/worker/**/*.test.ts"],
    // Windows hosted runners can spend >5s in local workerd/D1 round trips.
    // Bound the integration budget without changing application deadlines or
    // weakening assertions; avoid competing workerd instances on those runners.
    fileParallelism: process.platform !== "win32",
    testTimeout: process.platform === "win32" ? 20_000 : 5_000,
    hookTimeout: process.platform === "win32" ? 20_000 : 10_000,
    provide: {
      accessPrivateJWK,
      migrations,
    },
  },
});
