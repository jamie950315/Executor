import { applyD1Migrations, env, SELF } from "cloudflare:test";
import { exportJWK, generateKeyPair, importJWK, SignJWT } from "jose";
import { beforeAll, beforeEach, describe, expect, inject, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

const dashboardOrigin = "https://dashboard.example";
const accessIssuer = "https://executor-test.cloudflareaccess.com";
const accessAudience = "test-access-audience";
const enrollmentToken = "test-enrollment-token";

let accessPrivateKey: Awaited<ReturnType<typeof importJWK>>;

beforeAll(async () => {
  accessPrivateKey = await importJWK(inject("accessPrivateJWK"), "RS256");
});

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.batch([
    env.DB.prepare("DELETE FROM audits"),
    env.DB.prepare("DELETE FROM devices"),
    env.DB.prepare("DELETE FROM device_tombstones"),
  ]);
});

describe("dashboard HTTP control plane", () => {
  it("serves the minimal dashboard placeholder through Workers Static Assets", async () => {
    const response = await SELF.fetch(`${dashboardOrigin}/`);
    expect(response.status).toBe(200);
    expect(await response.text()).toContain("Executor Dashboard");
  });

  it("rejects protected routes without a cryptographically verified Access JWT", async () => {
    const response = await SELF.fetch(`${dashboardOrigin}/api/devices`);

    expect(response.status).toBe(401);
    await expect(response.json()).resolves.toEqual({ error: "unauthorized" });
  });

  it("accepts only the hashed enrollment bearer and stores an offline device", async () => {
    const denied = await enrollRequest(deviceFixture(), "wrong-token");
    expect(denied.status).toBe(401);

    const accepted = await enrollRequest(deviceFixture(), enrollmentToken);
    expect(accepted.status).toBe(201);
    await expect(accepted.json()).resolves.toMatchObject({
      device: {
        device_id: "device-vector-1",
        name: "Vector Mac",
        state: "offline",
        generation: 7,
      },
    });

    const row = await env.DB.prepare(
      "SELECT device_id, name, state, generation FROM devices WHERE device_id = ?",
    )
      .bind("device-vector-1")
      .first<{ device_id: string; name: string; state: string; generation: number }>();
    expect(row).toEqual({
      device_id: "device-vector-1",
      name: "Vector Mac",
      state: "offline",
      generation: 7,
    });
  });

  it("rejects MCP metadata URLs that could persist query or fragment secrets", async () => {
    const response = await enrollRequest(
      { ...deviceFixture(), mcp_url: "https://device.example/mcp?token=SENSITIVE" },
      enrollmentToken,
    );
    expect(response.status).toBe(400);
    await expect(response.json()).resolves.toEqual({ error: "invalid request" });
    const count = await env.DB.prepare("SELECT COUNT(*) AS count FROM devices").first<{ count: number }>();
    expect(count?.count).toBe(0);
  });

  it("keeps newer device metadata during stale enrollment retries and rejects replacement keys", async () => {
    expect((await enrollRequest(deviceFixture(), enrollmentToken)).status).toBe(201);

    const stale = await enrollRequest(
      {
        ...deviceFixture(),
        name: "Stale Mac",
        version: "0.0.1",
        mcp_url: "https://stale-device.example/mcp",
        generation: 7,
      },
      enrollmentToken,
    );
    expect(stale.status).toBe(200);
    await expect(stale.json()).resolves.toMatchObject({
      device: {
        name: "Vector Mac",
        version: "0.1.0",
        mcp_url: "https://device.example/mcp",
        generation: 7,
      },
    });

    const refresh = await enrollRequest(
      { ...deviceFixture(), name: "Renamed Mac", version: "0.2.0", generation: 8 },
      enrollmentToken,
    );
    expect(refresh.status).toBe(200);
    await expect(refresh.json()).resolves.toMatchObject({
      device: { name: "Renamed Mac", version: "0.2.0", generation: 8 },
    });

    const replacementPair = await generateKeyPair("ES256", { extractable: true });
    const replacementJWK = await exportJWK(replacementPair.publicKey);
    const replacement = await enrollRequest(
      {
        ...deviceFixture(),
        public_jwk: {
          kty: replacementJWK.kty ?? "EC",
          crv: replacementJWK.crv ?? "P-256",
          x: replacementJWK.x ?? "",
          y: replacementJWK.y ?? "",
        },
      },
      enrollmentToken,
    );
    expect(replacement.status).toBe(409);
    await expect(replacement.json()).resolves.toEqual({ error: "device key conflict" });
  });

  it("marks a higher-generation enrollment offline until the new relay authenticates", async () => {
    expect((await enrollRequest(deviceFixture(), enrollmentToken)).status).toBe(201);
    await env.DB.prepare(
      "UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ? WHERE device_id = ?",
    )
      .bind(1_700_000_000_001, 1_700_000_000_001, "device-vector-1")
      .run();

    const refreshed = await enrollRequest(
      { ...deviceFixture(), generation: 8, name: "Generation Eight" },
      enrollmentToken,
    );
    expect(refreshed.status).toBe(200);
    await expect(refreshed.json()).resolves.toMatchObject({
      device: { generation: 8, state: "offline", last_seen_at: null },
    });
    await expect(
      env.DB.prepare("SELECT generation, state, last_seen_at FROM devices WHERE device_id = ?")
        .bind("device-vector-1")
        .first(),
    ).resolves.toEqual({ generation: 8, state: "offline", last_seen_at: null });
  });

  it("rejects a non-canonical base64url P-256 enrollment key", async () => {
    const fixture = deviceFixture();
    const response = await enrollRequest(
      {
        ...fixture,
        public_jwk: {
          ...fixture.public_jwk,
          x: `${fixture.public_jwk.x.slice(0, -1)}Z`,
        },
      },
      enrollmentToken,
    );

    expect(response.status).toBe(400);
    await expect(response.json()).resolves.toEqual({ error: "invalid request" });
  });

  it("validates Access issuer, audience, time claims, signature, and subject", async () => {
    await enrollRequest(deviceFixture(), enrollmentToken);

    const valid = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken() },
    });
    expect(valid.status).toBe(200);

    const wrongAudience = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ audience: "wrong-audience" }) },
    });
    expect(wrongAudience.status).toBe(401);

    const expired = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ expiresAt: Math.floor(Date.now() / 1000) - 1 }) },
    });
    expect(expired.status).toBe(401);

    const future = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ notBefore: Math.floor(Date.now() / 1000) + 60 }) },
    });
    expect(future.status).toBe(401);

    const missingNotBefore = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ omitNotBefore: true }) },
    });
    expect(missingNotBefore.status).toBe(401);

    const wrongIssuer = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ issuer: "https://other.cloudflareaccess.com" }) },
    });
    expect(wrongIssuer.status).toBe(401);

    const noSubject = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken({ subject: "" }) },
    });
    expect(noSubject.status).toBe(401);
  });

  it("returns registry metadata and issues a strict host-only browser cookie", async () => {
    await enrollRequest(deviceFixture(), enrollmentToken);
    const response = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken() },
    });

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({
      devices: [
        expect.objectContaining({
          device_id: "device-vector-1",
          name: "Vector Mac",
          platform: "darwin",
          arch: "arm64",
          version: "0.1.0",
          mcp_url: "https://device.example/mcp",
          public_jwk: vectors.device_public_key,
          generation: 7,
          state: "offline",
        }),
      ],
    });
    expect(response.headers.get("set-cookie")).toMatch(
      /^__Host-executor-browser=[A-Za-z0-9_-]{43}; Path=\/; Max-Age=2592000; Secure; HttpOnly; SameSite=Strict$/,
    );
  });

  it("returns only the Access subject and browser identity from the no-store session endpoint", async () => {
    const first = await SELF.fetch(`${dashboardOrigin}/api/session`, {
      headers: { "cf-access-jwt-assertion": await accessToken() },
    });

    expect(first.status).toBe(200);
    expect(first.headers.get("cache-control")).toBe("no-store");
    const cookie = first.headers.get("set-cookie");
    expect(cookie).toMatch(
      /^__Host-executor-browser=[A-Za-z0-9_-]{43}; Path=\/; Max-Age=2592000; Secure; HttpOnly; SameSite=Strict$/,
    );
    const browserID = cookie?.match(/^__Host-executor-browser=([^;]+)/u)?.[1];
    expect(browserID).toBeTruthy();
    await expect(first.json()).resolves.toEqual({
      access_subject: "access-user-1",
      browser_id: browserID,
    });

    const second = await SELF.fetch(`${dashboardOrigin}/api/session`, {
      headers: {
        "cf-access-jwt-assertion": await accessToken(),
        cookie: `__Host-executor-browser=${browserID}`,
      },
    });
    expect(second.headers.get("set-cookie")).toBeNull();
    const secondBody = await second.text();
    expect(JSON.parse(secondBody)).toEqual({
      access_subject: "access-user-1",
      browser_id: browserID,
    });
    expect(secondBody).not.toContain("email");
  });

  it("adds production security headers to Static Assets without intercepting API routes", async () => {
    const response = await SELF.fetch(`${dashboardOrigin}/deep/workspace/path`);

    expect(response.status).toBe(200);
    expect(response.headers.get("content-security-policy")).toContain("default-src 'self'");
    expect(response.headers.get("content-security-policy")).toContain("frame-ancestors 'none'");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("referrer-policy")).toBe("no-referrer");

    const api = await SELF.fetch(`${dashboardOrigin}/api/not-an-asset`);
    expect(api.status).toBe(401);
  });

  it("rejects cross-origin requests and non-JSON state changes", async () => {
    const token = await accessToken();
    const crossOrigin = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: {
        "cf-access-jwt-assertion": token,
        origin: "https://attacker.example",
      },
    });
    expect(crossOrigin.status).toBe(403);
    await expect(crossOrigin.json()).resolves.toEqual({ error: "cross-origin request rejected" });

    const nonJSON = await SELF.fetch(`${dashboardOrigin}/api/device/enroll`, {
      method: "POST",
      headers: {
        authorization: `Bearer ${enrollmentToken}`,
        origin: dashboardOrigin,
        "content-type": "text/plain",
      },
      body: "not json",
    });
    expect(nonJSON.status).toBe(415);
    await expect(nonJSON.json()).resolves.toEqual({ error: "json required" });
  });

  it("uses only approved D1 columns and never writes enrollment payloads to audit", async () => {
    const marker = "SENSITIVE-ENROLLMENT-MARKER";
    const response = await enrollRequest(
      { ...deviceFixture(), name: marker, mcp_url: `https://device.example/${marker}` },
      enrollmentToken,
    );
    expect(response.status).toBe(201);

    const deviceColumns = await env.DB.prepare("PRAGMA table_info(devices)").all<{ name: string }>();
    expect(deviceColumns.results.map((column) => column.name)).toEqual([
      "device_id",
      "name",
      "platform",
      "arch",
      "version",
      "mcp_url",
      "public_jwk",
      "generation",
      "state",
      "created_at",
      "updated_at",
      "last_seen_at",
    ]);

    const auditColumns = await env.DB.prepare("PRAGMA table_info(audits)").all<{ name: string }>();
    expect(auditColumns.results.map((column) => column.name)).toEqual([
      "id",
      "device_id",
      "access_subject",
      "action",
      "outcome",
      "created_at",
    ]);
    const audits = await env.DB.prepare("SELECT * FROM audits").all();
    expect(JSON.stringify(audits.results)).not.toContain(marker);
    expect(audits.results).toEqual([
      expect.objectContaining({
        device_id: "device-vector-1",
        access_subject: null,
        action: "device.enroll",
        outcome: "success",
      }),
    ]);
  });

  it("returns a fixed internal error without echoing corrupted registry data", async () => {
    await enrollRequest(deviceFixture(), enrollmentToken);
    const marker = "CORRUPTED-REGISTRY-SENSITIVE-MARKER";
    await env.DB.prepare("UPDATE devices SET public_jwk = ? WHERE device_id = ?")
      .bind(marker, "device-vector-1")
      .run();

    const response = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
      headers: { "cf-access-jwt-assertion": await accessToken() },
    });
    expect(response.status).toBe(500);
    const body = await response.text();
    expect(body).toBe('{"error":"internal error"}');
    expect(body).not.toContain(marker);
  });
});

function deviceFixture() {
  return {
    device_id: "device-vector-1",
    name: "Vector Mac",
    platform: "darwin",
    arch: "arm64",
    version: "0.1.0",
    mcp_url: "https://device.example/mcp",
    public_jwk: vectors.device_public_key,
    generation: 7,
  };
}

function enrollRequest(body: ReturnType<typeof deviceFixture>, bearer: string): Promise<Response> {
  return SELF.fetch(`${dashboardOrigin}/api/device/enroll`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${bearer}`,
      "content-type": "application/json",
      origin: dashboardOrigin,
    },
    body: JSON.stringify(body),
  });
}

async function accessToken(
  overrides: {
    audience?: string;
    expiresAt?: number;
    notBefore?: number;
    omitNotBefore?: boolean;
    issuer?: string;
    subject?: string;
  } = {},
): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  let token = new SignJWT({})
    .setProtectedHeader({ alg: "RS256", kid: "access-test-key" })
    .setIssuer(overrides.issuer ?? accessIssuer)
    .setAudience(overrides.audience ?? accessAudience)
    .setSubject(overrides.subject ?? "access-user-1")
    .setIssuedAt(now);
  if (!overrides.omitNotBefore) {
    token = token.setNotBefore(overrides.notBefore ?? now - 1);
  }
  return token.setExpirationTime(overrides.expiresAt ?? now + 300).sign(accessPrivateKey);
}
