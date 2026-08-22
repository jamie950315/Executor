import { verifyAccess } from "./access";
import { handleControlRoute, matchControlRoute } from "./control";
import { enrollDevice, getDevice, listDevices, writeAudit, type EnrollmentInput } from "./db";
import { browserIdentity, errorResponse, jsonResponse, readBoundedJSON, rejectCrossOrigin, requireStateChangingRequest } from "./http";
import type { PublicKeyJWK } from "./shared/wire";

export { DeviceRelay } from "./device-relay";

const maximumEnrollmentBytes = 64 * 1024;

export default {
  async fetch(request, env): Promise<Response> {
    try {
      return await routeRequest(request, env);
    } catch {
      return errorResponse("internal error", 500);
    }
  },
} satisfies ExportedHandler<Env>;

async function routeRequest(request: Request, env: Env): Promise<Response> {
  const crossOrigin = rejectCrossOrigin(request);
  if (crossOrigin !== null) {
    return crossOrigin;
  }
  const url = new URL(request.url);

  if (request.method === "POST" && url.pathname === "/api/device/enroll") {
    return handleEnrollment(request, env);
  }
  const connectDeviceID = deviceConnectID(request, url);
  if (connectDeviceID !== null) {
    return handleDeviceConnect(request, env, connectDeviceID);
  }

  if (url.pathname.startsWith("/api/")) {
    const access = await verifyAccess(request, env);
    if (access === null) {
      return errorResponse("unauthorized", 401);
    }
    const browser = browserIdentity(request);
    const controlRoute = matchControlRoute(request, url);
    if (controlRoute !== null) {
      return handleControlRoute(request, env, access, controlRoute);
    }
    if (request.method === "GET" && url.pathname === "/api/devices") {
      const headers = new Headers();
      if (browser.setCookie !== null) {
        headers.append("set-cookie", browser.setCookie);
      }
      return jsonResponse({ devices: await listDevices(env.DB) }, 200, headers);
    }
    return errorResponse("not found", 404);
  }

  return env.ASSETS.fetch(request);
}

async function handleEnrollment(request: Request, env: Env): Promise<Response> {
  const requestError = requireStateChangingRequest(request);
  if (requestError !== null) {
    return requestError;
  }
  if (!(await validEnrollmentBearer(request, env.ENROLLMENT_TOKEN_HASH))) {
    return errorResponse("unauthorized", 401);
  }
  let input: EnrollmentInput;
  try {
    input = await parseEnrollment(await readBoundedJSON(request, maximumEnrollmentBytes));
  } catch (error) {
    return errorResponse(error instanceof Error && error.message === "request too large" ? "request too large" : "invalid request", error instanceof Error && error.message === "request too large" ? 413 : 400);
  }
  const now = Date.now();
  const enrolled = await enrollDevice(env.DB, input, now);
  if (enrolled === null) {
    await writeAudit(env.DB, input.device_id, null, "device.enroll", "conflict", now);
    return errorResponse("device key conflict", 409);
  }
  await writeAudit(env.DB, input.device_id, null, "device.enroll", "success", now);
  return jsonResponse({ device: enrolled.device }, enrolled.created ? 201 : 200);
}

async function handleDeviceConnect(request: Request, env: Env, deviceID: string): Promise<Response> {
  if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
    return errorResponse("websocket upgrade required", 426);
  }
  if ((await getDevice(env.DB, deviceID)) === null) {
    return errorResponse("device not found", 404);
  }
  const headers = new Headers(request.headers);
  headers.set("x-executor-device-id", deviceID);
  return env.DEVICE_RELAY.getByName(deviceID).fetch(new Request(request, { headers }));
}

function deviceConnectID(request: Request, url: URL): string | null {
  if (request.method !== "GET") {
    return null;
  }
  const match = url.pathname.match(/^\/api\/device\/connect\/([^/]+)$/u);
  if (match?.[1] === undefined) {
    return null;
  }
  try {
    const deviceID = decodeURIComponent(match[1]);
    return validText(deviceID, 256) ? deviceID : null;
  } catch {
    return null;
  }
}

async function validEnrollmentBearer(request: Request, expectedHash: string): Promise<boolean> {
  const authorization = request.headers.get("authorization");
  const match = authorization?.match(/^Bearer ([^\s]{1,512})$/u);
  const token = match?.[1] ?? "";
  const actual = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token)));
  const expected = decodeHexHash(expectedHash);
  const comparison = expected ?? new Uint8Array(32);
  const equal = crypto.subtle.timingSafeEqual(actual, comparison);
  return match !== null && match !== undefined && expected !== null && equal;
}

function decodeHexHash(value: string): Uint8Array | null {
  if (!/^[0-9a-f]{64}$/u.test(value)) {
    return null;
  }
  const bytes = new Uint8Array(32);
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(value.slice(index * 2, index * 2 + 2), 16);
  }
  return bytes;
}

async function parseEnrollment(value: unknown): Promise<EnrollmentInput> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("invalid request");
  }
  const record = value as Record<string, unknown>;
  const keys = ["device_id", "name", "platform", "arch", "version", "mcp_url", "public_jwk", "generation"];
  if (Object.keys(record).length !== keys.length || !keys.every((key) => Object.hasOwn(record, key))) {
    throw new Error("invalid request");
  }
  if (
    !validText(record.device_id, 256) ||
    !validText(record.name, 256) ||
    !validText(record.platform, 64) ||
    !validText(record.arch, 64) ||
    !validText(record.version, 64) ||
    typeof record.mcp_url !== "string" ||
    !validMCPURL(record.mcp_url) ||
    !Number.isSafeInteger(record.generation) ||
    (record.generation as number) <= 0 ||
    !isPublicJWK(record.public_jwk)
  ) {
    throw new Error("invalid request");
  }
  try {
    await crypto.subtle.importKey(
      "jwk",
      record.public_jwk,
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["verify"],
    );
  } catch {
    throw new Error("invalid request");
  }
  return {
    device_id: record.device_id,
    name: record.name,
    platform: record.platform,
    arch: record.arch,
    version: record.version,
    mcp_url: record.mcp_url,
    public_jwk: record.public_jwk,
    generation: record.generation as number,
  };
}

function isPublicJWK(value: unknown): value is PublicKeyJWK {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const record = value as Record<string, unknown>;
  return (
    Object.keys(record).length === 4 &&
    record.kty === "EC" &&
    record.crv === "P-256" &&
    typeof record.x === "string" &&
    /^[A-Za-z0-9_-]{43}$/u.test(record.x) &&
    typeof record.y === "string" &&
    /^[A-Za-z0-9_-]{43}$/u.test(record.y)
  );
}

function validText(value: unknown, maximum: number): value is string {
  if (typeof value !== "string" || value.trim().length === 0 || value.length > maximum) {
    return false;
  }
  return !Array.from(value).some((character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 31 || codePoint === 127;
  });
}

function validMCPURL(value: string): boolean {
  if (value.length > 2048) {
    return false;
  }
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.username === "" && url.password === "";
  } catch {
    return false;
  }
}
