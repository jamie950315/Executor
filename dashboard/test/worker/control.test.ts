import { applyD1Migrations, env, runInDurableObject, SELF } from "cloudflare:test";
import { importJWK, SignJWT } from "jose";
import { beforeAll, beforeEach, describe, expect, inject, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import {
  canonicalDeviceChallenge,
  encodeBase64URL,
  type GrantClaims,
} from "../../src/shared/crypto";
import { decodeEnvelope, makeEnvelope, type RecoveryEnvelope } from "../../src/shared/wire";

const dashboardOrigin = "https://dashboard.example";
const accessIssuer = "https://executor-test.cloudflareaccess.com";
const accessAudience = "test-access-audience";
const accessSubject = "access-user-1";
const deviceID = "device-vector-1";
const enrollmentToken = "test-enrollment-token";
const recoveryMarker = "SENSITIVE_RECOVERY_CIPHERTEXT_MARKER";

let accessPrivateKey: Awaited<ReturnType<typeof importJWK>>;
let devicePrivateKey: CryptoKey;

beforeAll(async () => {
  accessPrivateKey = await importJWK(inject("accessPrivateJWK"), "RS256");
  devicePrivateKey = await crypto.subtle.importKey(
    "jwk",
    {
      kty: "EC",
      crv: "P-256",
      x: vectors.test_only_device_private_key.x,
      y: vectors.test_only_device_private_key.y,
      d: vectors.test_only_device_private_key.d,
    },
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["sign"],
  );
});

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.batch([
    env.DB.prepare("DELETE FROM audits"),
    env.DB.prepare("DELETE FROM devices"),
    env.DB.prepare("DELETE FROM device_tombstones"),
  ]);
});

describe("unlocked device control", () => {
  it("relays opaque recovery ciphertext and scopes a verified grant cookie to one device", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);

    expect(unlocked.grantCookie).toMatch(
      /^__Secure-executor-grant-[0-9a-f]{16}=.+; Path=\/api\/devices\/device-vector-1; Max-Age=2592000; Secure; HttpOnly; SameSite=Strict$/,
    );
    expect(unlocked.grantCookie).not.toContain("grant-device-vector-1");

    const audits = await env.DB.prepare("SELECT * FROM audits ORDER BY id").all();
    expect(JSON.stringify(audits.results)).not.toContain(recoveryMarker);
    expect(audits.results).toContainEqual(
      expect.objectContaining({
        device_id: deviceID,
        access_subject: accessSubject,
        action: "device.unlock",
        outcome: "success",
      }),
    );
    await runInDurableObject(env.DEVICE_RELAY.getByName(deviceID), async (_instance, state) => {
      expect(await state.storage.list()).toEqual(new Map());
      for (const relaySocket of state.getWebSockets()) {
        expect(JSON.stringify(relaySocket.deserializeAttachment())).not.toContain(recoveryMarker);
      }
    });
    socket.close(1000, "test complete");
  });

  it("revalidates the grant and strictly correlates each versioned call response", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);
    const argumentMarker = "SENSITIVE_CALL_ARGUMENT_MARKER";

    const callPromise = controlRequest("call", unlocked, {
      method: "device.status",
      arguments: { marker: argumentMarker },
    });
    const request = decodeEnvelope(await nextMessage(socket));
    expect(request.type).toBe("request");
    if (request.type !== "request") {
      throw new Error("expected request");
    }
    expect(request.payload).toEqual({
      request_id: request.payload.request_id,
      method: "device.status",
      arguments: { marker: argumentMarker },
    });
    const responseEnvelope = makeEnvelope("response", "device-call-response", {
      request_id: request.payload.request_id,
      result: { ready: true },
    });
    socket.send(JSON.stringify(responseEnvelope));

    const response = await callPromise;
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe("application/x-ndjson; charset=utf-8");
    expect(new TextDecoder().decode(await response.arrayBuffer()).trim()).toBe(
      JSON.stringify(responseEnvelope),
    );
    const audits = await env.DB.prepare("SELECT * FROM audits ORDER BY id").all();
    expect(JSON.stringify(audits.results)).not.toContain(argumentMarker);
    expect(audits.results).toContainEqual(
      expect.objectContaining({ action: "device.call", outcome: "forwarded" }),
    );
    socket.close(1000, "test complete");
  });

  it("streams bounded, ordered response chunks without buffering a combined result", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);
    const callPromise = controlRequest("call", unlocked, {
      method: "terminal.output",
      arguments: {},
    });
    const request = decodeEnvelope(await nextMessage(socket));
    if (request.type !== "request") {
      throw new Error("expected request");
    }
    const chunks = [
      makeEnvelope("stream_chunk", "stream-0", {
        request_id: request.payload.request_id,
        sequence: 0,
        data: btoa("first"),
        final: false,
      }),
      makeEnvelope("stream_chunk", "stream-1", {
        request_id: request.payload.request_id,
        sequence: 1,
        data: btoa("second"),
        final: true,
      }),
    ];
    socket.send(JSON.stringify(chunks[0]));
    socket.send(JSON.stringify(chunks[1]));

    const response = await callPromise;
    const lines = new TextDecoder()
      .decode(await response.arrayBuffer())
      .trim()
      .split("\n");
    expect(lines).toEqual(chunks.map((chunk) => JSON.stringify(chunk)));
    socket.close(1000, "test complete");
  });

  it("invalidates an otherwise valid cookie immediately when generation changes", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);
    await env.DB.prepare("UPDATE devices SET generation = generation + 1 WHERE device_id = ?")
      .bind(deviceID)
      .run();

    const response = await controlRequest("call", unlocked, {
      method: "device.status",
      arguments: {},
    });
    expect(response.status).toBe(401);
    await expect(response.json()).resolves.toEqual({ error: "device locked" });
    await expect(noMessage(socket, 20)).resolves.toBe(true);
    socket.close(1000, "test complete");
  });

  it("returns 503 without queueing when the granted device is offline", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);
    socket.close(1000, "go offline");
    await eventually(async () => (await deviceState()) === "offline");

    const response = await controlRequest("call", unlocked, {
      method: "device.status",
      arguments: {},
    });
    expect(response.status).toBe(503);
    await expect(response.json()).resolves.toEqual({ error: "device offline" });
  });

  it("deletes only under a valid grant, disconnects, and leaves no registry row", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);

    const closed = nextClose(socket);
    const response = await controlRequest("exact-delete", unlocked, {}, "DELETE");
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({ deleted: true });
    await expect(
      env.DB.prepare("SELECT device_id FROM devices WHERE device_id = ?").bind(deviceID).first(),
    ).resolves.toBeNull();
    const audits = await env.DB.prepare("SELECT * FROM audits ORDER BY id").all();
    expect(audits.results).toContainEqual(
      expect.objectContaining({ action: "device.delete", outcome: "success" }),
    );
    await closed;
  });

  it("keeps a monotonic deletion tombstone so an old grant cannot resurrect", async () => {
    const socket = await enrolledAuthenticatedSocket();
    const unlocked = await unlock(socket);
    const closed = nextClose(socket);
    const deleted = await controlRequest("exact-delete", unlocked, {}, "DELETE");
    expect(deleted.status).toBe(200);
    await closed;

    expect((await enrollDeviceGeneration(7)).status).toBe(409);
    expect((await enrollDeviceGeneration(8)).status).toBe(409);
    expect((await enrollDeviceGeneration(9)).status).toBe(201);

    const oldGrantCall = await controlRequest("call", unlocked, {
      method: "device.status",
      arguments: {},
    });
    expect(oldGrantCall.status).toBe(401);
    await expect(oldGrantCall.json()).resolves.toEqual({ error: "device locked" });
  });
});

interface UnlockedSession {
  accessToken: string;
  browserCookie: string;
  grantCookie: string;
}

async function unlock(socket: WebSocket): Promise<UnlockedSession> {
  const accessToken = await makeAccessToken();
  const listResponse = await SELF.fetch(`${dashboardOrigin}/api/devices`, {
    headers: { "cf-access-jwt-assertion": accessToken },
  });
  const browserCookie = cookiePair(requiredHeader(listResponse, "set-cookie"));
  const browserID = browserCookie.slice(browserCookie.indexOf("=") + 1);
  const envelope: RecoveryEnvelope = {
    ...vectors.recovery.expected_envelope,
    version: 1,
    algorithm: "ECDH-P256+HKDF-SHA256+A256GCM",
    access_subject: accessSubject,
    browser_id: browserID,
    ciphertext: recoveryMarker,
    ephemeral_public_key: {
      kty: "EC",
      crv: "P-256",
      x: vectors.recovery.expected_envelope.ephemeral_public_key.x,
      y: vectors.recovery.expected_envelope.ephemeral_public_key.y,
    },
  };

  const unlockPromise = SELF.fetch(`${dashboardOrigin}/api/devices/${deviceID}/unlock`, {
    method: "POST",
    headers: controlHeaders(accessToken, browserCookie),
    body: JSON.stringify({ envelope }),
  });
  const unlockRequest = decodeEnvelope(await nextMessage(socket));
  expect(unlockRequest.type).toBe("recovery_unlock");
  if (unlockRequest.type !== "recovery_unlock") {
    throw new Error("expected recovery unlock");
  }
  expect(unlockRequest.payload.envelope).toEqual(envelope);
  const grant = await signGrant(browserID);
  socket.send(
    JSON.stringify(
      makeEnvelope("response", "unlock-response", {
        request_id: unlockRequest.message_id,
        result: { grant },
      }),
    ),
  );
  const response = await unlockPromise;
  expect(response.status).toBe(200);
  await expect(response.json()).resolves.toEqual({ unlocked: true });
  return {
    accessToken,
    browserCookie,
    grantCookie: requiredHeader(response, "set-cookie"),
  };
}

async function controlRequest(
  suffix: string,
  session: UnlockedSession,
  body: unknown,
  method = "POST",
): Promise<Response> {
  const path =
    suffix === ""
      ? `/api/devices/${deviceID}/`
      : suffix === "exact-delete"
        ? `/api/devices/${deviceID}`
        : `/api/devices/${deviceID}/${suffix}`;
  return SELF.fetch(`${dashboardOrigin}${path}`, {
    method,
    headers: controlHeaders(
      session.accessToken,
      `${session.browserCookie}; ${cookiePair(session.grantCookie)}`,
    ),
    body: JSON.stringify(body),
  });
}

function controlHeaders(accessToken: string, cookie: string): HeadersInit {
  return {
    "cf-access-jwt-assertion": accessToken,
    "content-type": "application/json",
    cookie,
    origin: dashboardOrigin,
  };
}

async function enrolledAuthenticatedSocket(): Promise<WebSocket> {
  const enrolled = await enrollDeviceGeneration(7);
  expect(enrolled.status).toBe(201);
  const response = await SELF.fetch(`${dashboardOrigin}/api/device/connect/${deviceID}`, {
    headers: { upgrade: "websocket" },
  });
  expect(response.status).toBe(101);
  const socket = response.webSocket as WebSocket;
  socket.accept();
  const challenge = JSON.parse(await nextMessage(socket)) as { nonce: string; issued_at: number };
  const signature = new Uint8Array(
    await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" },
      devicePrivateKey,
      new TextEncoder().encode(canonicalDeviceChallenge(deviceID, challenge.nonce, challenge.issued_at)),
    ),
  );
  socket.send(
    JSON.stringify({
      version: 1,
      type: "device_challenge_response",
      nonce: challenge.nonce,
      issued_at: challenge.issued_at,
      signature: encodeBase64URL(signature),
    }),
  );
  await nextMessage(socket);
  return socket;
}

function enrollDeviceGeneration(generation: number): Promise<Response> {
  return SELF.fetch(`${dashboardOrigin}/api/device/enroll`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${enrollmentToken}`,
      "content-type": "application/json",
      origin: dashboardOrigin,
    },
    body: JSON.stringify({
      device_id: deviceID,
      name: "Vector Device",
      platform: "darwin",
      arch: "arm64",
      version: "0.1.0",
      mcp_url: "https://device.example/mcp",
      public_jwk: vectors.device_public_key,
      generation,
    }),
  });
}

async function signGrant(browserID: string): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  const claims: GrantClaims = {
    version: 1,
    device_id: deviceID,
    access_subject: accessSubject,
    browser_id: browserID,
    generation: 7,
    issued_at: now,
    expires_at: now + 3600,
    jti: crypto.randomUUID(),
  };
  const header = encodeBase64URL(
    new TextEncoder().encode(
      JSON.stringify({ alg: "ES256", typ: "executor-device-grant+jwt", version: 1 }),
    ),
  );
  const payload = encodeBase64URL(new TextEncoder().encode(JSON.stringify(claims)));
  const signature = new Uint8Array(
    await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" },
      devicePrivateKey,
      new TextEncoder().encode(`${header}.${payload}`),
    ),
  );
  return `${header}.${payload}.${encodeBase64URL(signature)}`;
}

async function makeAccessToken(): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  return new SignJWT({})
    .setProtectedHeader({ alg: "RS256", kid: "access-test-key" })
    .setIssuer(accessIssuer)
    .setAudience(accessAudience)
    .setSubject(accessSubject)
    .setIssuedAt(now)
    .setNotBefore(now - 1)
    .setExpirationTime(now + 300)
    .sign(accessPrivateKey);
}

function requiredHeader(response: Response, name: string): string {
  const value = response.headers.get(name);
  if (value === null) {
    throw new Error(`missing ${name}`);
  }
  return value;
}

function cookiePair(setCookie: string): string {
  return setCookie.split(";", 1)[0] ?? "";
}

function nextMessage(socket: WebSocket): Promise<string> {
  return new Promise((resolve, reject) => {
    socket.addEventListener(
      "message",
      (event) => (typeof event.data === "string" ? resolve(event.data) : reject(new Error("binary message"))),
      { once: true },
    );
  });
}

function nextClose(socket: WebSocket): Promise<CloseEvent> {
  return new Promise((resolve) => socket.addEventListener("close", resolve, { once: true }));
}

async function noMessage(socket: WebSocket, milliseconds: number): Promise<boolean> {
  return Promise.race([
    nextMessage(socket).then(() => false),
    scheduler.wait(milliseconds).then(() => true),
  ]);
}

async function eventually(check: () => Promise<boolean>): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (await check()) {
      return;
    }
    await scheduler.wait(2);
  }
  throw new Error("condition not met");
}

async function deviceState(): Promise<string | null> {
  const row = await env.DB.prepare("SELECT state FROM devices WHERE device_id = ?")
    .bind(deviceID)
    .first<{ state: string }>();
  return row?.state ?? null;
}
