import { compactVerify, importJWK } from "jose";
import { getDevice, listDevices, writeAudit, type DeviceRecord } from "./db";
import { errorResponse, jsonResponse, readBoundedJSON } from "./http";
import { MAXIMUM_RELAY_MESSAGE_BYTES } from "./limits";
import { relayHTTPResponse } from "./relay-response";
import { sha256Hex } from "./shared/crypto";
import { makeEnvelope } from "./shared/wire";
import { parseCallResponse } from "./shared/relay-reader";

interface HubLink { device_id: string; device_generation: number; delegation_version: number; expires_at: number; grant: string }
const methods = new Set(["filesystem_read", "filesystem_write", "terminal", "terminal_output", "terminal_sessions", "device_status", "device_permissions", "desktop_observe", "desktop_control"]);

// This authenticates the machine transport, not authority to operate a device.
// The device independently verifies its local approval, grant, signature and
// persisted replay nonce. No browser identity or cookie participates here.
async function authenticate(request: Request, db: D1Database): Promise<string | null> {
  const header = request.headers.get("authorization") ?? "";
  if (!/^Bearer [A-Za-z0-9_-]{32,256}$/.test(header)) return null;
  const hash = await sha256Hex(header.slice(7));
  const row = await db.prepare("SELECT hub_id FROM hubs WHERE token_hash=? AND enabled=1").bind(hash).first<{ hub_id: string }>();
  return row?.hub_id ?? null;
}

export async function handleHubRoute(request: Request, env: Env): Promise<Response> {
  const hubID = await authenticate(request, env.DB);
  if (hubID === null) return errorResponse("unauthorized", 401);
  const path = new URL(request.url).pathname;
  const now = Date.now();
  if (request.method === "GET" && path === "/api/hub/devices") {
    const links = await env.DB.prepare("SELECT device_id, device_generation, delegation_version, expires_at, grant FROM hub_devices WHERE hub_id=?").bind(hubID).all<HubLink>();
    const byID = new Map(links.results.map(link => [link.device_id, link]));
    const devices = (await listDevices(env.DB)).map(device => {
      const link = byID.get(device.device_id);
      return { deviceId: device.device_id, name: device.name, platform: device.platform, generation: device.generation,
        online: device.state === "online", authorized: link !== undefined && link.grant.length > 0 && link.device_generation === device.generation && link.expires_at > now };
    });
    return jsonResponse({ devices });
  }
  const match = /^\/api\/hub\/devices\/([A-Za-z0-9_-]{1,256})\/(call|delegation)$/.exec(path);
  if (match === null || (match[2] === "call" ? request.method !== "POST" : request.method !== "GET")) return errorResponse("not found", 404);
  const deviceID = match[1]!;
  const device = await getDevice(env.DB, deviceID);
  const link = await env.DB.prepare("SELECT device_id, device_generation, delegation_version, expires_at, grant FROM hub_devices WHERE hub_id=? AND device_id=?").bind(hubID, deviceID).first<HubLink>();
  if (device === null || link === null || link.grant.length === 0 || link.device_generation !== device.generation || link.expires_at <= now) return errorResponse("device not delegated to Hub", 403);
  if (match[2] === "delegation") {
    if (!(await validDelegation(device, hubID, link, link.grant, now))) return errorResponse("invalid Hub delegation", 403);
    return jsonResponse({ device_key: device.public_jwk, grant: link.grant, generation: device.generation, version: link.delegation_version });
  }
  if (request.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase() !== "application/json") return errorResponse("json required", 415);
  let proof: Record<string, unknown>;
  let body: Record<string, unknown>;
  try {
    proof = exact(await readBoundedJSON(request, MAXIMUM_RELAY_MESSAGE_BYTES), ["request", "signature"]);
    body = exact(proof.request, ["version", "device_id", "hub_id", "caller_id", "request_id", "method", "input", "grant", "issued_at", "expires_at", "nonce"]);
    if (body.version !== 1 || body.device_id !== deviceID || body.hub_id !== hubID ||
        !text(body.caller_id, 256) || !text(body.request_id, 256) || !text(body.nonce, 256) ||
        typeof body.method !== "string" || !methods.has(body.method) || !text(body.input, MAXIMUM_RELAY_MESSAGE_BYTES) || !text(body.grant, 16384) ||
        typeof proof.signature !== "string" || !/^[A-Za-z0-9_-]{86}$/.test(proof.signature) ||
        !Number.isSafeInteger(body.issued_at) || !Number.isSafeInteger(body.expires_at)) throw new Error("invalid proof");
    const issued = body.issued_at as number, expires = body.expires_at as number;
    if (issued <= 0 || expires <= issued || expires - issued > 60 || issued > Math.floor(now / 1000) + 5 || expires <= Math.floor(now / 1000)) throw new Error("expired proof");
  } catch {
    return errorResponse("invalid signed Hub request", 400);
  }
  if (body.grant !== link.grant || !(await validDelegation(device, hubID, link, body.grant as string, now))) return errorResponse("invalid Hub delegation", 403);
  if (device.state !== "online") return errorResponse("device offline", 503);
  const requestID = body.request_id as string;
  const envelope = makeEnvelope("request", crypto.randomUUID(), { request_id: requestID, method: "hub.call", arguments: proof });
  const relayed = await env.DEVICE_RELAY.getByName(deviceID).relayStream(envelope);
  if (!relayed.ok) return jsonResponse({ error: "Hub relay unavailable", outcome: "unconfirmed", request_id: requestID }, relayed.error === "message_too_large" ? 413 : 503);
  let response = await relayHTTPResponse(relayed.stream, requestID, request.signal);
  if (response.ok) {
    try {
      const parsed = await parseCallResponse(response);
      if (parsed.requestID !== requestID) throw new Error("response mismatch");
      response = jsonResponse(parsed.result,200,new Headers({"x-executor-request-id":requestID}));
    } catch {
      response = jsonResponse({error:"Hub relay response unconfirmed",outcome:"unconfirmed",request_id:requestID},502);
    }
  }
  await writeAudit(env.DB, deviceID, "hub:" + hubID, body.method as string, response.status === 200 ? "forwarded" : "unconfirmed", now);
  return response;
}

export async function validDelegation(device: DeviceRecord, hubID: string, link: HubLink, token: string, now: number, expectedKeyID?: string, expectedExpiry?: number): Promise<boolean> {
  try {
    const key = await importJWK(device.public_jwk, "ES256");
    const verified = await compactVerify(token, key, { algorithms: ["ES256"] });
    if (verified.protectedHeader.typ !== "executor-hub-grant+jwt" || verified.protectedHeader.version !== 1) return false;
    const claims = exact(JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(verified.payload)), ["version", "device_id", "hub_id", "hub_key_id", "generation", "delegation_version", "issued_at", "expires_at", "jti"]);
    const issued = claims.issued_at, expires = claims.expires_at;
    return claims.version === 1 && claims.device_id === device.device_id && claims.hub_id === hubID &&
      claims.generation === device.generation && claims.delegation_version === link.delegation_version &&
      (expectedKeyID === undefined || claims.hub_key_id === expectedKeyID) && (expectedExpiry === undefined || claims.expires_at === expectedExpiry) &&
      typeof claims.hub_key_id === "string" && /^[0-9a-f]{64}$/.test(claims.hub_key_id) && text(claims.jti, 256) &&
      typeof issued === "number" && typeof expires === "number" && Number.isSafeInteger(issued) && Number.isSafeInteger(expires) &&
      issued > 0 && expires > issued && expires - issued <= 30 * 86400 && issued <= Math.floor(now / 1000) + 120 && expires > Math.floor(now / 1000);
  } catch { return false; }
}

function text(value: unknown, maximum: number): value is string { return typeof value === "string" && value.trim().length > 0 && value.length <= maximum; }
function exact(value: unknown, keys: string[]): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid object");
  const record = value as Record<string, unknown>;
  if (Object.keys(record).length !== keys.length || !keys.every(key => Object.hasOwn(record, key))) throw new Error("invalid fields");
  return record;
}
