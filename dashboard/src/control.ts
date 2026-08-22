import type { AccessIdentity } from "./access";
import { deleteDeviceConditionally, getDevice, writeAudit, type DeviceRecord } from "./db";
import type { RelayFailureCode, RelayStreamResult } from "./device-relay";
import {
  browserIdentity,
  cookieValue,
  deviceGrantCookieName,
  deviceGrantSetCookie,
  errorResponse,
  jsonResponse,
  readBoundedJSON,
  requireStateChangingRequest,
} from "./http";
import { verifyDeviceGrant } from "./shared/crypto";
import { decodeEnvelope, makeEnvelope } from "./shared/wire";

const maximumControlBodyBytes = 16 * 1024 * 1024;

const allowedControlMethods = new Set([
  "terminal",
  "terminal_output",
  "terminal_sessions",
  "filesystem_read",
  "filesystem_write",
  "desktop_observe",
  "desktop_control",
  "device_status",
  "device_permissions",
  "control.status",
  "control.audit",
  "control.permissions",
  "control.rotate",
  "control.kill",
  "control.resume",
]);

interface ControlRoute {
  deviceID: string;
  action: "unlock" | "call" | "delete";
}

interface UnlockedContext {
  device: DeviceRecord;
  browserID: string;
  grant: string;
}

export function matchControlRoute(request: Request, url: URL): ControlRoute | null {
  const match = url.pathname.match(/^\/api\/devices\/([^/]+)(?:\/(unlock|call))?\/?$/u);
  if (match?.[1] === undefined) {
    return null;
  }
  try {
    const deviceID = decodeURIComponent(match[1]);
    if (!validText(deviceID, 256)) {
      return null;
    }
    if (request.method === "DELETE" && (match[2] === undefined || match[2] === "")) {
      return { deviceID, action: "delete" };
    }
    if (request.method === "POST" && (match[2] === "unlock" || match[2] === "call")) {
      return { deviceID, action: match[2] };
    }
    return null;
  } catch {
    return null;
  }
}

export async function handleControlRoute(
  request: Request,
  env: Env,
  access: AccessIdentity,
  route: ControlRoute,
): Promise<Response> {
  const requestError = requireStateChangingRequest(request);
  if (requestError !== null) {
    return requestError;
  }
  switch (route.action) {
    case "unlock":
      return handleUnlock(request, env, access, route.deviceID);
    case "call":
      return handleCall(request, env, access, route.deviceID);
    case "delete":
      return handleDelete(request, env, access, route.deviceID);
  }
}

async function handleUnlock(
  request: Request,
  env: Env,
  access: AccessIdentity,
  deviceID: string,
): Promise<Response> {
  const device = await getDevice(env.DB, deviceID);
  if (device === null) {
    return errorResponse("device not found", 404);
  }
  const browser = browserIdentity(request);
  if (browser.setCookie !== null) {
    const headers = new Headers({ "set-cookie": browser.setCookie });
    return jsonResponse({ error: "browser identity required" }, 409, headers);
  }

  let envelope;
  try {
    const body = await readBoundedJSON(request, maximumControlBodyBytes);
    envelope = decodeEnvelope(
      JSON.stringify({
        version: 1,
        type: "recovery_unlock",
        message_id: crypto.randomUUID(),
        payload: body,
      }),
    );
    if (
      envelope.type !== "recovery_unlock" ||
      envelope.payload.envelope.device_id !== deviceID ||
      envelope.payload.envelope.access_subject !== access.subject ||
      envelope.payload.envelope.browser_id !== browser.id ||
      envelope.payload.envelope.generation !== device.generation
    ) {
      throw new Error("invalid unlock context");
    }
  } catch {
    await writeAudit(env.DB, deviceID, access.subject, "device.unlock", "rejected", Date.now());
    return errorResponse("invalid request", 400);
  }

  const relayed = await env.DEVICE_RELAY.getByName(deviceID).relayOnce(envelope);
  if (!relayed.ok) {
    await writeAudit(env.DB, deviceID, access.subject, "device.unlock", relayed.error, Date.now());
    return relayErrorResponse(relayed.error);
  }
  const grant = responseGrant(relayed.response, envelope.message_id);
  if (grant === null) {
    await writeAudit(env.DB, deviceID, access.subject, "device.unlock", "invalid_response", Date.now());
    return errorResponse("unlock rejected", 502);
  }
  try {
    await verifyDeviceGrant(device.public_jwk, grant, {
      deviceID,
      accessSubject: access.subject,
      browserID: browser.id,
      generation: device.generation,
      now: new Date(),
    });
  } catch {
    await writeAudit(env.DB, deviceID, access.subject, "device.unlock", "invalid_grant", Date.now());
    return errorResponse("unlock rejected", 401);
  }

  const headers = new Headers({ "set-cookie": await deviceGrantSetCookie(deviceID, grant) });
  await writeAudit(env.DB, deviceID, access.subject, "device.unlock", "success", Date.now());
  return jsonResponse({ unlocked: true }, 200, headers);
}

async function handleCall(
  request: Request,
  env: Env,
  access: AccessIdentity,
  deviceID: string,
): Promise<Response> {
  const unlocked = await verifyUnlocked(request, env, access, deviceID);
  if (unlocked === null) {
    await writeAudit(env.DB, deviceID, access.subject, "device.call", "locked", Date.now());
    return errorResponse("device locked", 401);
  }
  let method: string;
  let argumentsValue: unknown;
  try {
    const body = await readBoundedJSON(request, maximumControlBodyBytes);
    const record = exactRecord(body, ["method", "arguments"]);
    if (!validText(record.method, 256) || !allowedControlMethods.has(record.method)) {
      throw new Error("invalid method");
    }
    method = record.method;
    argumentsValue = record.arguments;
  } catch (error) {
    const tooLarge = error instanceof Error && error.message === "request too large";
    return errorResponse(tooLarge ? "request too large" : "invalid request", tooLarge ? 413 : 400);
  }
  const requestID = crypto.randomUUID();
  const relayEnvelope = makeEnvelope("request", crypto.randomUUID(), {
    request_id: requestID,
    method,
    arguments: {
      authorization: {
        grant: unlocked.grant,
        access_subject: access.subject,
        browser_id: unlocked.browserID,
      },
      input: argumentsValue,
    },
  });
  const relayed: RelayStreamResult = await env.DEVICE_RELAY.getByName(deviceID).relayStream(relayEnvelope);
  if (!relayed.ok) {
    await writeAudit(env.DB, deviceID, access.subject, method, relayed.error, Date.now());
    return relayErrorResponse(relayed.error);
  }
  await writeAudit(env.DB, deviceID, access.subject, method, "forwarded", Date.now());
  return new Response(relayed.stream, {
    headers: {
      "cache-control": "no-store",
      "content-type": "application/x-ndjson; charset=utf-8",
    },
  });
}

async function handleDelete(
  request: Request,
  env: Env,
  access: AccessIdentity,
  deviceID: string,
): Promise<Response> {
  const unlocked = await verifyUnlocked(request, env, access, deviceID);
  if (unlocked === null) {
    await writeAudit(env.DB, deviceID, access.subject, "device.delete", "locked", Date.now());
    return errorResponse("device locked", 401);
  }
  try {
    exactRecord(await readBoundedJSON(request, 1024), []);
  } catch {
    return errorResponse("invalid request", 400);
  }
  const deleted = await deleteDeviceConditionally(env.DB, unlocked.device, Date.now());
  if (!deleted.deleted) {
    await writeAudit(env.DB, deviceID, access.subject, "device.delete", "changed", Date.now());
    return errorResponse("device changed", 409);
  }
  await env.DEVICE_RELAY.getByName(deviceID).disconnect(unlocked.device.generation);
  await writeAudit(env.DB, deviceID, access.subject, "device.delete", "success", Date.now());
  return jsonResponse({ deleted: true });
}

async function verifyUnlocked(
  request: Request,
  env: Env,
  access: AccessIdentity,
  deviceID: string,
): Promise<UnlockedContext | null> {
  const device = await getDevice(env.DB, deviceID);
  if (device === null) {
    return null;
  }
  const browser = browserIdentity(request);
  if (browser.setCookie !== null) {
    return null;
  }
  const cookieName = await deviceGrantCookieName(deviceID);
  const grant = cookieValue(request, cookieName);
  if (grant === null) {
    return null;
  }
  try {
    await verifyDeviceGrant(device.public_jwk, grant, {
      deviceID,
      accessSubject: access.subject,
      browserID: browser.id,
      generation: device.generation,
      now: new Date(),
    });
    return { device, browserID: browser.id, grant };
  } catch {
    return null;
  }
}

function responseGrant(response: string, requestID: string): string | null {
  try {
    const envelope = decodeEnvelope(response);
    if (envelope.type !== "response" || envelope.payload.request_id !== requestID) {
      return null;
    }
    const result = exactRecord(envelope.payload.result, ["grant"]);
    return typeof result.grant === "string" && result.grant.length <= 16 * 1024 ? result.grant : null;
  } catch {
    return null;
  }
}

function relayErrorResponse(error: RelayFailureCode): Response {
  switch (error) {
    case "offline":
      return errorResponse("device offline", 503);
    case "message_too_large":
      return errorResponse("request too large", 413);
    case "duplicate":
    case "timeout":
    case "protocol":
      return errorResponse("relay unavailable", 503);
  }
}

function exactRecord(value: unknown, keys: readonly string[]): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("invalid request");
  }
  const record = value as Record<string, unknown>;
  if (Object.keys(record).length !== keys.length || !keys.every((key) => Object.hasOwn(record, key))) {
    throw new Error("invalid request");
  }
  return record;
}

function validText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.length <= maximum;
}
