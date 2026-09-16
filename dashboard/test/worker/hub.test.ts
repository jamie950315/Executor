import { applyD1Migrations, env, SELF } from "cloudflare:test";
import { beforeEach, expect, inject, it } from "vitest";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { handleHubRoute } from "../../src/hub";
import { canonicalEnvelope, makeEnvelope } from "../../src/shared/wire";

const token = "hub-test-" + "a".repeat(64);
const origin = "https://dashboard.example";

beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.prepare("DELETE FROM hub_devices").run();
  await env.DB.prepare("DELETE FROM hubs").run();
  await env.DB.prepare("DELETE FROM devices").run();
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token))), b => b.toString(16).padStart(2, "0")).join("");
  await env.DB.prepare("INSERT INTO hubs (hub_id, token_hash, enabled) VALUES (?, ?, 1)").bind("pi5", hash).run();
});

it("requires independent machine authentication and never sets browser cookies", async () => {
  const unauthorized = await SELF.fetch(origin + "/api/hub/devices", { headers: { cookie: "__Host-executor-browser=fake" } });
  expect(unauthorized.status).toBe(401);
  const response = await SELF.fetch(origin + "/api/hub/devices", { headers: { authorization: "Bearer " + token } });
  expect(response.status).toBe(200);
  expect(response.headers.get("set-cookie")).toBeNull();
  expect(await response.json()).toEqual({ devices: [] });
});

it("revocation immediately denies subsequent directory requests", async () => {
  await env.DB.prepare("UPDATE hubs SET enabled=0 WHERE hub_id=?").bind("pi5").run();
  const response = await SELF.fetch(origin + "/api/hub/devices", { headers: { authorization: "Bearer " + token } });
  expect(response.status).toBe(401);
});

it("does not accept browser control envelopes or unknown targets", async () => {
  const response = await SELF.fetch(origin + "/api/hub/devices/missing/call", {
    method: "POST", headers: { authorization: "Bearer " + token, "content-type": "application/json" },
    body: JSON.stringify({ method: "filesystem_write", arguments: {}, session_id: "caller" }),
  });
  expect(response.status).toBe(403);
});

it.each(["response","chunks","wrong-id","truncated","failure","wrong-failure-id"])("handles real relay wire format: %s", async (mode) => {
  const keys = await generateKeyPair("ES256", { extractable: true });
  const publicKey = await exportJWK(keys.publicKey);
  const now = Date.now(), seconds = Math.floor(now / 1000);
  await env.DB.prepare("INSERT INTO devices VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)").bind("mac", "Mac", "darwin", "arm64", "test", "https://unused.example/mcp", JSON.stringify(publicKey), 1, "online", now, now, now).run();
  await env.DB.prepare("INSERT INTO hub_devices (hub_id,device_id,device_generation,delegation_version,expires_at) VALUES (?, ?, ?, ?, ?)").bind("pi5", "mac", 1, 2, now + 3600000).run();
  const grant = await new SignJWT({ version: 1, device_id: "mac", hub_id: "pi5", hub_key_id: "a".repeat(64), generation: 1, delegation_version: 2, issued_at: seconds, expires_at: seconds + 3600, jti: "fixture" })
    .setProtectedHeader({ alg: "ES256", typ: "executor-hub-grant+jwt", version: 1 }).sign(keys.privateKey);
  await env.DB.prepare("UPDATE hub_devices SET grant=? WHERE hub_id=? AND device_id=?").bind(grant, "pi5", "mac").run();
  const delegationResponse = await SELF.fetch(origin + "/api/hub/devices/mac/delegation", { headers: { authorization: "Bearer " + token } });
  expect(delegationResponse.status).toBe(200);
  expect(await delegationResponse.json()).toEqual({ device_key: publicKey, grant, generation: 1, version: 2 });
  const proof = { request: { version: 1, device_id: "mac", hub_id: "pi5", caller_id: "caller", request_id: "request", method: "filesystem_write", input: "e30=", grant, issued_at: seconds, expires_at: seconds + 60, nonce: "fixture-nonce" }, signature: "a".repeat(86) };
  let calls = 0;
  const fixtureEnv = { ...env, DEVICE_RELAY: { getByName(id: string) {
    expect(id).toBe("mac");
    return { relayStream(envelope: { payload: { request_id: string; method: string; arguments: unknown } }) {
      calls++;
      expect(envelope.payload).toEqual({ request_id: "request", method: "hub.call", arguments: proof });
      const frames = mode === "chunks" || mode === "truncated"
        ? [makeEnvelope("stream_chunk",crypto.randomUUID(),{request_id:"request",sequence:0,data:btoa('{"ok":'),final:false}),
          ...(mode === "chunks" ? [makeEnvelope("stream_chunk",crypto.randomUUID(),{request_id:"request",sequence:1,data:btoa('true}'),final:true})] : [])]
        : [makeEnvelope("response",crypto.randomUUID(),{request_id:mode === "wrong-id" || mode === "wrong-failure-id" ? "another-request" : "request",...(mode === "failure" || mode === "wrong-failure-id" ? {failure:{code:"host_action_failed"}} : {result:{ok:true}})})];
      const wire = frames.map(frame => canonicalEnvelope(frame)+"\n").join("");
      return { ok: true, stream: new ReadableStream<Uint8Array>({ start(c) { c.enqueue(new TextEncoder().encode(wire)); c.close(); } }) };
    } };
  } } } as unknown as Env;
  const request = () => new Request(origin + "/api/hub/devices/mac/call", { method: "POST", headers: { authorization: "Bearer " + token, "content-type": "application/json" }, body: JSON.stringify(proof) });
  const response = await handleHubRoute(request(), fixtureEnv);
  if (mode === "failure") {
    expect(response.status).toBe(422);
    expect(await response.json()).toEqual({code:"device_reported_failure",request_id:"request"});
    expect(calls).toBe(1);
    return;
  }
  if (mode === "wrong-id" || mode === "truncated" || mode === "wrong-failure-id") {
    expect(response.status).toBe(502);
    expect(await response.json()).toMatchObject({outcome:"unconfirmed"});
    expect(calls).toBe(1);
    return;
  }
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ ok: true });
  expect(calls).toBe(1);
  await env.DB.prepare("UPDATE hub_devices SET delegation_version=3 WHERE hub_id=? AND device_id=?").bind("pi5", "mac").run();
  expect((await handleHubRoute(request(), fixtureEnv)).status).toBe(403);
  expect(calls).toBe(1);
});
