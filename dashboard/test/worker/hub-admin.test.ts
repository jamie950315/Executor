import { applyD1Migrations, env, SELF } from "cloudflare:test";
import { exportJWK, generateKeyPair, importJWK, SignJWT } from "jose";
import { beforeEach, expect, inject, it } from "vitest";
import { finalizeHubOwner, prepareHubOwner } from "../../src/hub-owner";
import { getDevice } from "../../src/db";
import { sha256Hex } from "../../src/shared/crypto";
import { canonicalEnvelope, makeEnvelope } from "../../src/shared/wire";
import { parseCallResponse } from "../../src/shared/relay-reader";

const origin = "https://dashboard.example";
function deviceResponse(id: string,result: unknown) { return new Response(canonicalEnvelope(makeEnvelope("response",crypto.randomUUID(),{request_id:id,result}))+"\n",{headers:{"content-type":"application/x-ndjson"}}); }
beforeEach(async () => {
  await applyD1Migrations(env.DB, inject("migrations"));
  await env.DB.prepare("DELETE FROM hub_devices").run();
  await env.DB.prepare("DELETE FROM hubs").run();
  await env.DB.prepare("DELETE FROM devices").run();
});

it("persists validated device delegation and retains revocation against late responses", async () => {
  const hubPair = await generateKeyPair("ES256",{extractable:true});
  const publicHub = await exportJWK(hubPair.publicKey);
  const devicePair = await generateKeyPair("ES256",{extractable:true});
  const publicDevice = await exportJWK(devicePair.publicKey);
  const now = Date.now(), seconds = Math.floor(now/1000);
  await env.DB.prepare("INSERT INTO hubs (hub_id,token_hash,public_jwk,enabled) VALUES (?,?,?,1)").bind("pi5","a".repeat(64),JSON.stringify(publicHub)).run();
  await env.DB.prepare("INSERT INTO devices VALUES (?,?,?,?,?,?,?,?,?,?,?,?)").bind("mac","Mac","darwin","arm64","test","https://unused.example/mcp",JSON.stringify(publicDevice),1,"online",now,now,now).run();
  const device = await getDevice(env.DB,"mac");
  expect(device).not.toBeNull();
  const action = await prepareHubOwner(env.DB,"hub.delegate",{hub_id:"pi5"});
  expect(action.input.public_key).toEqual(publicHub);
  await expect(prepareHubOwner(env.DB,"hub.delegate",{hub_id:"pi5",public_key:publicDevice})).rejects.toThrow();
  const keyID = await sha256Hex(JSON.stringify({crv:publicHub.crv,kty:publicHub.kty,x:publicHub.x,y:publicHub.y}));
  const grant = await new SignJWT({version:1,device_id:"mac",hub_id:"pi5",hub_key_id:keyID,generation:1,delegation_version:1,issued_at:seconds,expires_at:seconds+3600,jti:"fixture"}).setProtectedHeader({alg:"ES256",typ:"executor-hub-grant+jwt",version:1}).sign(devicePair.privateKey);
  const output = {hub_id:"pi5",device_id:"mac",generation:1,delegation_version:1,enabled:true,grant,expires_at:seconds+3600};
  const request = new Request(origin+"/api/devices/mac/call");
  const finalized = await finalizeHubOwner(request,deviceResponse("approve",output),env,device!,action,"approve");
  expect(finalized.status).toBe(200);
  const decoded = await parseCallResponse(finalized);
  expect(decoded.requestID).toBe("approve");
  expect(decoded.result).toMatchObject({hub_id:"pi5",device_id:"mac",enabled:true});
  expect(decoded.result).not.toHaveProperty("grant");
  const revoke = await prepareHubOwner(env.DB,"hub.revoke",{hub_id:"pi5"});
  expect((await finalizeHubOwner(request,deviceResponse("revoke",{hub_id:"pi5",device_id:"mac",generation:1,delegation_version:2,enabled:false}),env,device!,revoke,"revoke")).status).toBe(200);
  expect((await finalizeHubOwner(request,deviceResponse("late",output),env,device!,action,"late")).status).toBe(502);
  expect(await env.DB.prepare("SELECT delegation_version,grant,expires_at FROM hub_devices WHERE hub_id='pi5' AND device_id='mac'").first()).toEqual({delegation_version:2,grant:"",expires_at:0});
});

async function ownerHeaders() {
  const key = await importJWK(inject("accessPrivateJWK"), "RS256");
  const jwt = await new SignJWT({ email: "owner@example.test" }).setProtectedHeader({ alg: "RS256", kid: "access-test-key" })
    .setSubject("owner").setIssuer("https://executor-test.cloudflareaccess.com").setAudience("test-access-audience").setIssuedAt().setNotBefore(Math.floor(Date.now()/1000)-1).setExpirationTime("5m").sign(key);
  return { origin, "content-type": "application/json", "cf-access-jwt-assertion": jwt };
}

it("registers only an authenticated owner-provided public identity and token hash", async () => {
  const pair = await generateKeyPair("ES256", { extractable: true });
  const input = { hub_id: "pi5", public_key: await exportJWK(pair.publicKey), token_hash: "a".repeat(64) };
  const headers = await ownerHeaders();
  const request = (h: HeadersInit = headers) => SELF.fetch(origin + "/api/hubs", { method: "POST", headers: h, body: JSON.stringify(input) });
  expect((await request({ origin, "content-type": "application/json" })).status).toBe(401);
  expect((await request()).status).toBe(201);
  expect((await request()).status).toBe(200);
  const listed = await SELF.fetch(origin + "/api/hubs", { headers });
  expect(listed.status).toBe(200);
  const text = await listed.text();
  expect(text).toContain('"hub_id":"pi5"');
  expect(text).not.toContain(input.token_hash);
  input.token_hash = "b".repeat(64);
  expect((await request()).status).toBe(409);
  expect((await SELF.fetch(origin+"/api/hubs/pi5/disable",{method:"POST",headers,body:"{}"})).status).toBe(200);
  input.token_hash = "a".repeat(64);
  const repeated = await request();
  expect(repeated.status).toBe(200);
  expect((await repeated.json() as {enabled:boolean}).enabled).toBe(false);
});

it("rejects private key input and cross-origin owner mutations", async () => {
  const pair = await generateKeyPair("ES256", { extractable: true });
  const headers = await ownerHeaders();
  const input = { hub_id: "pi5", public_key: await exportJWK(pair.privateKey), token_hash: "a".repeat(64) };
  expect((await SELF.fetch(origin + "/api/hubs", { method: "POST", headers, body: JSON.stringify(input) })).status).toBe(400);
  expect((await SELF.fetch(origin + "/api/hubs", { method: "POST", headers: { ...headers, origin: "https://other.example" }, body: JSON.stringify(input) })).status).toBe(403);
});
