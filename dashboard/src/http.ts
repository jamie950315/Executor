import { decodeBase64URL, encodeBase64URL, sha256Hex } from "./shared/crypto";

const browserCookieName = "__Host-executor-browser";
const grantLifetimeSeconds = 30 * 24 * 60 * 60;

export function jsonResponse(value: unknown, status = 200, headers?: Headers): Response {
  const responseHeaders = headers ?? new Headers();
  responseHeaders.set("cache-control", "no-store");
  responseHeaders.set("content-type", "application/json; charset=utf-8");
  return new Response(JSON.stringify(value), { status, headers: responseHeaders });
}

export function errorResponse(error: string, status: number): Response {
  return jsonResponse({ error }, status);
}

export function rejectCrossOrigin(request: Request): Response | null {
  const origin = request.headers.get("origin");
  if (origin !== null && origin !== new URL(request.url).origin) {
    return errorResponse("cross-origin request rejected", 403);
  }
  return null;
}

export function requireStateChangingRequest(request: Request): Response | null {
  if (request.headers.get("origin") !== new URL(request.url).origin) {
    return errorResponse("cross-origin request rejected", 403);
  }
  const contentType = request.headers.get("content-type")?.split(";", 1)[0]?.trim().toLowerCase();
  if (contentType !== "application/json") {
    return errorResponse("json required", 415);
  }
  return null;
}

export async function readBoundedJSON(request: Request, maximumBytes: number): Promise<unknown> {
  const contentLength = request.headers.get("content-length");
  if (contentLength !== null) {
    const length = Number(contentLength);
    if (!Number.isSafeInteger(length) || length < 0 || length > maximumBytes) {
      throw new Error("request too large");
    }
  }
  if (request.body === null) {
    throw new Error("invalid json");
  }
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      total += value.byteLength;
      if (total > maximumBytes) {
        try {
          await reader.cancel();
        } catch {
          // Preserve the deterministic request-too-large response even if the
          // client transport has already failed while cancellation propagates.
        }
        throw new Error("request too large");
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return JSON.parse(new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(bytes));
  } catch {
    throw new Error("invalid json");
  }
}

export function browserIdentity(request: Request): { id: string; setCookie: string | null } {
  const existing = cookieValue(request, browserCookieName);
  if (existing !== null && validBrowserID(existing)) {
    return { id: existing, setCookie: null };
  }
  const id = encodeBase64URL(crypto.getRandomValues(new Uint8Array(32)));
  return {
    id,
    setCookie: browserIdentitySetCookie(id),
  };
}

export function browserIdentitySetCookie(id: string): string {
  if (!validBrowserID(id)) {
    throw new Error("invalid browser identity");
  }
  return `${browserCookieName}=${id}; Path=/; Max-Age=${grantLifetimeSeconds}; Secure; HttpOnly; SameSite=Strict`;
}

export function cookieValue(request: Request, name: string): string | null {
  const cookie = request.headers.get("cookie");
  if (cookie === null || cookie.length > 32 * 1024) {
    return null;
  }
  for (const part of cookie.split(";")) {
    const separator = part.indexOf("=");
    if (separator < 1) {
      continue;
    }
    if (part.slice(0, separator).trim() === name) {
      return part.slice(separator + 1).trim();
    }
  }
  return null;
}

export async function deviceGrantCookieName(deviceID: string): Promise<string> {
  const digest = await sha256Hex(deviceID);
  return `__Secure-executor-grant-${digest.slice(0, 16)}`;
}

export async function deviceGrantSetCookie(deviceID: string, grant: string): Promise<string> {
  const name = await deviceGrantCookieName(deviceID);
  return `${name}=${grant}; Path=/api/devices/${encodeURIComponent(deviceID)}; Max-Age=${grantLifetimeSeconds}; Secure; HttpOnly; SameSite=Strict`;
}

function validBrowserID(value: string): boolean {
  try {
    return value.length === 43 && decodeBase64URL(value).byteLength === 32;
  } catch {
    return false;
  }
}
