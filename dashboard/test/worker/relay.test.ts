import { applyD1Migrations, env, evictDurableObject, runInDurableObject, SELF } from "cloudflare:test";
import { beforeEach, describe, expect, inject, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { deleteDeviceConditionally, getDevice } from "../../src/db";
import { DeviceRelay } from "../../src/device-relay";
import {
  canonicalDeviceChallenge,
  canonicalDeviceRefresh,
  encodeBase64URL,
  type DeviceRefresh,
} from "../../src/shared/crypto";
import { decodeEnvelope, makeEnvelope } from "../../src/shared/wire";

const dashboardOrigin = "https://dashboard.example";
const enrollmentToken = "test-enrollment-token";

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.batch([
    env.DB.prepare("DELETE FROM audits"),
    env.DB.prepare("DELETE FROM devices"),
    env.DB.prepare("DELETE FROM device_tombstones"),
  ]);
});

describe("DeviceRelay WebSocket", () => {
  it("negotiates version one before challenge and rejects a replay", async () => {
    await enroll();
    const socket = await connectSocket();
    const offer = decodeEnvelope(await nextMessage(socket));
    expect(offer.type).toBe("version_negotiation");
    if (offer.type !== "version_negotiation") {
      throw new Error("expected version negotiation");
    }
    expect(offer.payload.supported_versions).toEqual([1]);
    const selection = makeEnvelope("version_negotiation", offer.message_id, {
      supported_versions: [1],
    });
    socket.send(JSON.stringify(selection));
    parseChallenge(await nextMessage(socket));

    socket.send(JSON.stringify(selection));
    expect((await nextClose(socket)).code).toBe(1008);
    await expect(deviceState()).resolves.toBe("offline");
  });

  it("rejects malformed or non-overlapping version selections", async () => {
    await enroll();
    for (const supportedVersions of [[1, 1], [2]]) {
      const socket = await connectSocket();
      const offer = decodeEnvelope(await nextMessage(socket));
      socket.send(
        JSON.stringify(
          makeEnvelope("version_negotiation", offer.message_id, {
            supported_versions: supportedVersions,
          }),
        ),
      );
      expect((await nextClose(socket)).code).toBe(1008);
    }
    await expect(deviceState()).resolves.toBe("offline");
  });

  it("keeps a device offline until its canonical P-256 challenge is verified", async () => {
    await enroll();
    const socket = await connectSocket();
    const challenge = await negotiateVersion(socket);

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

  it("keeps an authenticated socket offline and relay-ineligible until refresh acknowledgement", async () => {
    await enroll();
    const socket = await connectAuthenticatedWithoutRefresh();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-before-refresh", {
      request_id: "request-before-refresh",
      method: "device.status",
      arguments: {},
    });

    await expect(deviceState()).resolves.toBe("offline");
    await expect(stub.relayOnce(request)).resolves.toEqual({ ok: false, error: "offline" });
    await expect(noMessage(socket, 20)).resolves.toBe(true);

    await refreshAuthenticatedSocket(socket, 7);
    await expect(deviceState()).resolves.toBe("online");
    const responsePromise = stub.relayOnce(request);
    expect(decodeEnvelope(await nextMessage(socket))).toEqual(request);
    const response = makeEnvelope("response", "response-after-refresh", {
      request_id: "request-before-refresh",
      result: { ready: true },
    });
    socket.send(JSON.stringify(response));
    await expect(responsePromise).resolves.toEqual({ ok: true, response: JSON.stringify(response) });
    socket.close(1000, "test complete");
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

  it("accepts a signed monotonic generation refresh and rejects its replay", async () => {
    await enroll();
    const socket = await connectAuthenticated();
    const sameGeneration: DeviceRefresh = {
      device_id: "device-vector-1",
      generation: 7,
      name: "Metadata Refresh",
      platform: "darwin",
      arch: "arm64",
      executor_version: "dev",
      mcp_url: "https://device.example/mcp",
      issued_at: Math.floor(Date.now() / 1000) - 1,
    };
    socket.send(
      JSON.stringify({
        version: 1,
        type: "device_refresh",
        ...sameGeneration,
        signature: await signDeviceRefresh(sameGeneration),
      }),
    );
    expect(JSON.parse(await nextMessage(socket))).toEqual({
      version: 1,
      type: "device_refreshed",
      generation: 7,
    });
    await expect(getDevice(env.DB, "device-vector-1")).resolves.toEqual(
      expect.objectContaining({ generation: 7, name: "Metadata Refresh" }),
    );
    const refresh: DeviceRefresh = {
      device_id: "device-vector-1",
      generation: 8,
      name: "Refreshed Device",
      platform: "darwin",
      arch: "arm64",
      executor_version: "dev",
      mcp_url: "https://device.example/mcp",
      issued_at: sameGeneration.issued_at,
    };
    const signature = await signDeviceRefresh(refresh);
    const message = JSON.stringify({ version: 1, type: "device_refresh", ...refresh, signature });
    socket.send(message);
    expect(JSON.parse(await nextMessage(socket))).toEqual({
      version: 1,
      type: "device_refreshed",
      generation: 8,
    });
    await expect(getDevice(env.DB, "device-vector-1")).resolves.toEqual(
      expect.objectContaining({ generation: 8, name: "Refreshed Device" }),
    );

    socket.send(message);
    const close = await nextClose(socket);
    expect(close.code).toBe(1008);
    await expect(getDevice(env.DB, "device-vector-1")).resolves.toEqual(
      expect.objectContaining({ generation: 8, name: "Refreshed Device" }),
    );
  });

  it("rejects a lower-generation or incorrectly signed device refresh", async () => {
    await enroll();
    const lowerSocket = await connectAuthenticated();
    const lower: DeviceRefresh = {
      device_id: "device-vector-1",
      generation: 6,
      name: "Lower Device",
      platform: "darwin",
      arch: "arm64",
      executor_version: "dev",
      mcp_url: "https://device.example/mcp",
      issued_at: Math.floor(Date.now() / 1000),
    };
    lowerSocket.send(
      JSON.stringify({ version: 1, type: "device_refresh", ...lower, signature: await signDeviceRefresh(lower) }),
    );
    expect((await nextClose(lowerSocket)).code).toBe(1008);

    const wrongKeySocket = await connectAuthenticated();
    const current = { ...lower, generation: 7, name: "Wrong Key Device", issued_at: lower.issued_at + 1 };
    wrongKeySocket.send(
      JSON.stringify({
        version: 1,
        type: "device_refresh",
        ...current,
        signature: encodeBase64URL(crypto.getRandomValues(new Uint8Array(64))),
      }),
    );
    expect((await nextClose(wrongKeySocket)).code).toBe(1008);
    await expect(getDevice(env.DB, "device-vector-1")).resolves.toEqual(
      expect.objectContaining({ generation: 7, name: "Vector Device" }),
    );
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

  it("keeps an authenticated request pending when a second challenge socket closes", async () => {
    await enroll();
    const authenticated = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-authenticated-owner", {
      request_id: "request-authenticated-owner",
      method: "device.status",
      arguments: {},
    });

    const responsePromise = stub.relayOnce(request);
    expect(decodeEnvelope(await nextMessage(authenticated))).toEqual(request);

    const challengeOnly = await connectSocket();
    await nextMessage(challengeOnly);
    challengeOnly.close(1000, "challenge abandoned");
    await scheduler.wait(20);
    await expect(promiseStillPending(responsePromise, 20)).resolves.toBe(true);

    const expected = makeEnvelope("response", "authenticated-owner-response", {
      request_id: "request-authenticated-owner",
      result: { ready: true },
    });
    authenticated.send(JSON.stringify(expected));
    await expect(responsePromise).resolves.toEqual({ ok: true, response: JSON.stringify(expected) });
    authenticated.close(1000, "test complete");
  });

  it("disconnects only the deleted generation and preserves a re-enrolled connection", async () => {
    await enroll();
    const oldSocket = await connectAuthenticated();
    const snapshot = await getDevice(env.DB, "device-vector-1");
    if (snapshot === null) {
      throw new Error("missing device snapshot");
    }
    await expect(deleteDeviceConditionally(env.DB, snapshot, Date.now())).resolves.toEqual({ deleted: true });
    await enroll(9);
    const replacement = await connectAuthenticated(9);
    await eventually(async () => oldSocket.readyState === WebSocket.CLOSED);

    await env.DEVICE_RELAY.getByName("device-vector-1").disconnect(7);

    await expect(noClose(replacement, 20)).resolves.toBe(true);
    await expect(deviceState()).resolves.toBe("online");
    replacement.close(1000, "test complete");
  });

  it("clears the request deadline timer when a response stream is cancelled", async () => {
    await enroll();
    const socket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-cancel-timer", {
      request_id: "request-cancel-timer",
      method: "terminal.output",
      arguments: {},
    });

    await runInDurableObject(stub, async (instance: DeviceRelay) => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      const clearTimeoutSpy = vi.spyOn(globalThis, "clearTimeout");
      try {
        const result = instance.relayStream(request);
        if (!result.ok) {
          throw new Error(`unexpected relay failure: ${result.error}`);
        }
        const deadline = setTimeoutSpy.mock.results.at(-1)?.value;
        expect(deadline).toBeDefined();
        await result.stream.cancel("browser cancelled");
        expect(clearTimeoutSpy).toHaveBeenCalledWith(deadline);
      } finally {
        setTimeoutSpy.mockRestore();
        clearTimeoutSpy.mockRestore();
      }
    });

    socket.close(1000, "test complete");
  });

  it("uses one absolute deadline timer across all non-final stream chunks", async () => {
    await enroll();
    const clientSocket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-absolute-deadline", {
      request_id: "request-absolute-deadline",
      method: "terminal.output",
      arguments: {},
    });

    await runInDurableObject(stub, async (instance: DeviceRelay, state) => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      try {
        const result = instance.relayStream(request);
        if (!result.ok) {
          throw new Error(`unexpected relay failure: ${result.error}`);
        }
        expect(setTimeoutSpy).toHaveBeenCalledTimes(1);
        const serverSocket = state.getWebSockets("device")[0];
        if (serverSocket === undefined) {
          throw new Error("missing relay socket");
        }
        await instance.webSocketMessage(
          serverSocket,
          JSON.stringify(
            makeEnvelope("stream_chunk", "absolute-deadline-0", {
              request_id: "request-absolute-deadline",
              sequence: 0,
              data: btoa("first"),
              final: false,
            }),
          ),
        );
        expect(setTimeoutSpy).toHaveBeenCalledTimes(1);
        await result.stream.cancel("test complete");
      } finally {
        setTimeoutSpy.mockRestore();
      }
    });

    clientSocket.close(1000, "test complete");
  });

  it("keeps sensitive lifecycle requests pending long enough to return one-time recovery material", async () => {
    await enroll();
    const clientSocket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-lifecycle-deadline", {
      request_id: "request-lifecycle-deadline",
      method: "control.rotate",
      arguments: {},
    });

    await runInDurableObject(stub, async (instance: DeviceRelay) => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      try {
        const result = instance.relayStream(request);
        if (!result.ok) {
          throw new Error(`unexpected relay failure: ${result.error}`);
        }
        expect(setTimeoutSpy.mock.calls.at(-1)?.[1]).toBe(120_000);
        await result.stream.cancel("test complete");
      } finally {
        setTimeoutSpy.mockRestore();
      }
    });

    clientSocket.close(1000, "test complete");
  });

  it("keeps resume pending beyond the host readiness-check window", async () => {
    await enroll();
    const clientSocket = await connectAuthenticated();
    const stub = env.DEVICE_RELAY.getByName("device-vector-1");
    const request = makeEnvelope("request", "message-resume-deadline", {
      request_id: "request-resume-deadline",
      method: "control.resume",
      arguments: {},
    });

    await runInDurableObject(stub, async (instance: DeviceRelay) => {
      const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
      try {
        const result = instance.relayStream(request);
        if (!result.ok) {
          throw new Error(`unexpected relay failure: ${result.error}`);
        }
        expect(setTimeoutSpy.mock.calls.at(-1)?.[1]).toBe(120_000);
        await result.stream.cancel("test complete");
      } finally {
        setTimeoutSpy.mockRestore();
      }
    });

    clientSocket.close(1000, "test complete");
  });

  it.each(["desktop_control", "device_permissions", "control.permissions"])(
    "keeps interactive %s requests pending beyond the ordinary relay deadline",
    async (method) => {
      await enroll();
      const clientSocket = await connectAuthenticated();
      const stub = env.DEVICE_RELAY.getByName("device-vector-1");
      const request = makeEnvelope("request", `message-${method}-deadline`, {
        request_id: `request-${method}-deadline`,
        method,
        arguments: {},
      });

      await runInDurableObject(stub, async (instance: DeviceRelay) => {
        const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
        try {
          const result = instance.relayStream(request);
          if (!result.ok) {
            throw new Error(`unexpected relay failure: ${result.error}`);
          }
          expect(setTimeoutSpy.mock.calls.at(-1)?.[1]).toBe(120_000);
          await result.stream.cancel("test complete");
        } finally {
          setTimeoutSpy.mockRestore();
        }
      });

      clientSocket.close(1000, "test complete");
    },
  );
});

async function enroll(generation = 7): Promise<void> {
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
      generation,
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

async function connectAuthenticated(generation = 7): Promise<WebSocket> {
  const socket = await connectAuthenticatedWithoutRefresh(generation);
  await refreshAuthenticatedSocket(socket, generation);
  return socket;
}

async function connectAuthenticatedWithoutRefresh(generation = 7): Promise<WebSocket> {
  const socket = await connectSocket();
  const challenge = await negotiateVersion(socket);
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

async function negotiateVersion(socket: WebSocket): Promise<{ nonce: string; issued_at: number }> {
  const offer = decodeEnvelope(await nextMessage(socket));
  expect(offer.type).toBe("version_negotiation");
  if (offer.type !== "version_negotiation") {
    throw new Error("expected version negotiation");
  }
  expect(offer.payload.supported_versions).toEqual([1]);
  socket.send(
    JSON.stringify(
      makeEnvelope("version_negotiation", offer.message_id, { supported_versions: [1] }),
    ),
  );
  return parseChallenge(await nextMessage(socket));
}

async function refreshAuthenticatedSocket(socket: WebSocket, generation: number): Promise<void> {
  const refresh: DeviceRefresh = {
    device_id: "device-vector-1",
    generation,
    name: "Vector Device",
    platform: "darwin",
    arch: "arm64",
    executor_version: "dev",
    mcp_url: "https://device.example/mcp",
    issued_at: Math.floor(Date.now() / 1000) - 5,
  };
  socket.send(
    JSON.stringify({
      version: 1,
      type: "device_refresh",
      ...refresh,
      signature: await signDeviceRefresh(refresh),
    }),
  );
  expect(JSON.parse(await nextMessage(socket))).toEqual({
    version: 1,
    type: "device_refreshed",
    generation,
  });
}

async function signDeviceRefresh(refresh: DeviceRefresh): Promise<string> {
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
      new TextEncoder().encode(canonicalDeviceRefresh(refresh)),
    ),
  );
  return encodeBase64URL(signature);
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

async function noClose(socket: WebSocket, milliseconds: number): Promise<boolean> {
  return Promise.race([
    nextClose(socket).then(() => false),
    scheduler.wait(milliseconds).then(() => true),
  ]);
}
