import { generateKeyPair, SignJWT, type JWTVerifyGetKey } from "jose";
import { describe, expect, it, vi } from "vitest";

import { createAccessVerifier } from "../../src/access";

describe("Cloudflare Access JWKS resolver cache", () => {
  it("reuses one resolver per issuer and isolates different issuers", async () => {
    const { privateKey, publicKey } = await generateKeyPair("RS256");
    const factory = vi.fn((url: URL): JWTVerifyGetKey => {
      expect(url.pathname).toBe("/cdn-cgi/access/certs");
      return async () => publicKey;
    });
    const verifyAccess = createAccessVerifier(factory);
    const firstEnv = {
      ACCESS_TEAM_DOMAIN: "first.cloudflareaccess.com",
      ACCESS_AUD: "first-audience",
    };
    const secondEnv = {
      ACCESS_TEAM_DOMAIN: "second.cloudflareaccess.com",
      ACCESS_AUD: "second-audience",
    };

    await expect(
      verifyAccess(accessRequest(await accessToken(privateKey, firstEnv)), firstEnv),
    ).resolves.toEqual({ subject: "access-user-1" });
    await expect(
      verifyAccess(accessRequest(await accessToken(privateKey, firstEnv)), firstEnv),
    ).resolves.toEqual({ subject: "access-user-1" });
    await expect(
      verifyAccess(accessRequest(await accessToken(privateKey, secondEnv)), secondEnv),
    ).resolves.toEqual({ subject: "access-user-1" });

    expect(factory.mock.calls.map(([url]) => url.href)).toEqual([
      "https://first.cloudflareaccess.com/cdn-cgi/access/certs",
      "https://second.cloudflareaccess.com/cdn-cgi/access/certs",
    ]);
  });
});

function accessRequest(assertion: string): Request {
  return new Request("https://dashboard.example/api/devices", {
    headers: { "cf-access-jwt-assertion": assertion },
  });
}

async function accessToken(
  privateKey: CryptoKey,
  env: { ACCESS_TEAM_DOMAIN: string; ACCESS_AUD: string },
): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  return new SignJWT({})
    .setProtectedHeader({ alg: "RS256", kid: "access-test-key" })
    .setIssuer(`https://${env.ACCESS_TEAM_DOMAIN}`)
    .setAudience(env.ACCESS_AUD)
    .setSubject("access-user-1")
    .setIssuedAt(now)
    .setNotBefore(now - 1)
    .setExpirationTime(now + 300)
    .sign(privateKey);
}
