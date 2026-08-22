import { applyD1Migrations, env, evictDurableObject, SELF } from "cloudflare:test";
import { beforeEach, describe, expect, inject, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { canonicalDeviceChallenge, encodeBase64URL } from "../../src/shared/crypto";
import { decodeEnvelope, makeEnvelope } from "../../src/shared/wire";

const dashboardOrigin = "https://dashboard.example";
const enrollmentToken = "test-enrollment-token";

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.batch([env.DB.prepare("DELETE FROM audits"), env.DB.prepare("DELETE FROM devices")]);
});

describe("DeviceRelay WebSocket", () => {
  it("keeps a device offline until its canonical P-256 challenge is verified", async () => {
    await enroll();
    const socket = await connectSocket();
    const challenge = parseChallenge(await nextMessage(socket));

    socket.send(
      JSON.stringify({
        version: 1,
        type: "device_challenge_response",
        nonce: challenge.nonce,
        issued_at: challenge.issued_at,
        signature: encodeBase64URL(new Uint8Array(64)),
      }),
    );

    const close = await nextClose(socket);
    expect(close.code).toBe(1008);
    await expect(deviceState()).resolves.toBe("offline");
  });

  it("marks an authenticated device online and immediately offline on disconnect", async () => {
    await enroll();
    const socket = await connectAuthenticated();
    await expect(deviceState()).resolves.toBe("online");

    socket.close(1000, "test complete");
    await eventually(async () => (await deviceState()) === "offline");
  });

  it("restores authentication from a WebSocket attachment after hibernation", async () => {
    await enroll();
    const socket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    await evictDurableObject(stub);

    const sentAt = Math.floor(Date.now() / 1000);
    socket.send(
      JSON.stringify(
        makeEnvelope("heartbeat", "heartbeat-after-hibernation", {
          device_id: "device-vector-1",
          generation: 7,
          sent_at: sentAt,
        }),
      ),
    );

    await eventually(async () => {
      const row = await env.DB.prepare("SELECT state, last_seen_at FROM devices WHERE device_id = ?")
        .bind("device-vector-1")
        .first<{ state: string; last_seen_at: number | null }>();
      return row?.state === "online" && (row.last_seen_at ?? 0) >= sentAt * 1000;
    });
    socket.close(1000, "test complete");
  });

  it("rejects an offline relay immediately and never queues it for a later connection", async () => {
    await enroll();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-offline", {
      request_id: "request-offline",
      method: "device.status",
      arguments: {},
    });

    await expect(stub.relayOnce(request)).resolves.toEqual({ ok: false, error: "offline" });

    const socket = await connectAuthenticated();
    await expect(noMessage(socket, 20)).resolves.toBe(true);
    socket.close(1000, "test complete");
  });

  it("resolves only the response with the exact pending request correlation", async () => {
    await enroll();
    const socket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-correlation", {
      request_id: "request-correlation",
      method: "device.status",
      arguments: { verbose: true },
    });

    const responsePromise = stub.relayOnce(request);
    expect(decodeEnvelope(await nextMessage(socket))).toEqual(request);

    socket.send(
      JSON.stringify(
        makeEnvelope("response", "wrong-response-message", {
          request_id: "wrong-request",
          result: { ignored: true },
        }),
      ),
    );
    await expect(promiseStillPending(responsePromise, 20)).resolves.toBe(true);

    const expected = makeEnvelope("response", "matching-response-message", {
      request_id: "request-correlation",
      result: { ready: true },
    });
    socket.send(JSON.stringify(expected));
    await expect(responsePromise).resolves.toEqual({ ok: true, response: JSON.stringify(expected) });
    socket.close(1000, "test complete");
  });
});

async function enroll(): Promise<void> {
  const response = await SELF.fetch(`${dashboardOrigin}/api/device/enroll`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${enrollmentToken}`,
      "content-type": "application/json",
      origin: dashboardOrigin,
    },
    body: JSON.stringify({
      device_id: "device-vector-1",
      name: "Vector Device",
      platform: "darwin",
      arch: "arm64",
      version: "0.1.0",
      mcp_url: "https://device.example/mcp",
      public_jwk: vectors.device_public_key,
      generation: 7,
    }),
  });
  expect(response.status).toBe(201);
}

async function connectSocket(): Promise<WebSocket> {
  const response = await SELF.fetch(`${dashboardOrigin}/api/device/connect/device-vector-1`, {
    headers: { upgrade: "websocket" },
  });
  expect(response.status).toBe(101);
  expect(response.webSocket).not.toBeNull();
  const socket = response.webSocket as WebSocket;
  socket.accept();
  return socket;
}

async function connectAuthenticated(): Promise<WebSocket> {
  const socket = await connectSocket();
  const challenge = parseChallenge(await nextMessage(socket));
  const privateKey = await crypto.subtle.importKey(
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
  const signature = new Uint8Array(
    await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" },
      privateKey,
      new TextEncoder().encode(
        canonicalDeviceChallenge("device-vector-1", challenge.nonce, challenge.issued_at),
      ),
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
  expect(JSON.parse(await nextMessage(socket))).toEqual({ version: 1, type: "device_authenticated" });
  return socket;
}

function parseChallenge(message: string): { nonce: string; issued_at: number } {
  const value: unknown = JSON.parse(message);
  expect(value).toMatchObject({
    version: 1,
    type: "device_challenge",
    device_id: "device-vector-1",
  });
  const challenge = value as { nonce: string; issued_at: number };
  expect(challenge.nonce).toMatch(/^[A-Za-z0-9_-]{43}$/u);
  expect(challenge.issued_at).toBeGreaterThan(0);
  return challenge;
}

function nextMessage(socket: WebSocket): Promise<string> {
  return new Promise((resolve, reject) => {
    socket.addEventListener(
      "message",
      (event) => {
        if (typeof event.data === "string") {
          resolve(event.data);
        } else {
          reject(new Error("unexpected binary message"));
        }
      },
      { once: true },
    );
    socket.addEventListener("error", () => reject(new Error("websocket error")), { once: true });
  });
}

function nextClose(socket: WebSocket): Promise<CloseEvent> {
  return new Promise((resolve) => socket.addEventListener("close", resolve, { once: true }));
}

async function deviceState(): Promise<string | null> {
  const row = await env.DB.prepare("SELECT state FROM devices WHERE device_id = ?")
    .bind("device-vector-1")
    .first<{ state: string }>();
  return row?.state ?? null;
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

async function promiseStillPending(promise: Promise<unknown>, milliseconds: number): Promise<boolean> {
  return Promise.race([
    promise.then(() => false, () => false),
    scheduler.wait(milliseconds).then(() => true),
  ]);
}

async function noMessage(socket: WebSocket, milliseconds: number): Promise<boolean> {
  return Promise.race([
    nextMessage(socket).then(() => false),
    scheduler.wait(milliseconds).then(() => true),
  ]);
}
