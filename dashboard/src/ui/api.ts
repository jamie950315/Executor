import { DeviceLockedError, DeviceOfflineError, DeviceActionError, DeviceResponseError, responseRequestID, parseCallResponse, type CallResult } from "../shared/relay-reader";
export { DeviceLockedError, DeviceOfflineError, DeviceActionError, DeviceResponseError, parseCallResponse } from "../shared/relay-reader";
export type { CallResult } from "../shared/relay-reader";

export interface SessionContext {
  access_subject: string;
  browser_id: string;
}

export interface DeviceRecord {
  device_id: string;
  name: string;
  platform: string;
  arch: string;
  version: string;
  mcp_url: string;
  public_jwk: { kty: "EC"; crv: "P-256"; x: string; y: string };
  generation: number;
  state: "online" | "offline";
  created_at: number;
  updated_at: number;
  last_seen_at: number | null;
}

type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export async function fetchSession(signal?: AbortSignal, fetcher: Fetcher = fetch): Promise<SessionContext> {
  const response = await fetcher("/api/session", {
    credentials: "same-origin",
    headers: { accept: "application/json" },
    signal,
  });
  const value = await strictJSONResponse(response);
  if (!isRecord(value) || !validText(value.access_subject, 512) || !validText(value.browser_id, 256)) {
    throw new DeviceActionError("Session is unavailable");
  }
  return { access_subject: value.access_subject, browser_id: value.browser_id };
}

export async function fetchDevices(signal?: AbortSignal, fetcher: Fetcher = fetch): Promise<DeviceRecord[]> {
  const response = await fetcher("/api/devices", {
    credentials: "same-origin",
    headers: { accept: "application/json" },
    signal,
  });
  const value = await strictJSONResponse(response);
  if (!isRecord(value) || !Array.isArray(value.devices)) {
    throw new DeviceActionError("Device registry is unavailable");
  }
  return value.devices as DeviceRecord[];
}

export async function unlockDevice(
  deviceID: string,
  envelope: unknown,
  signal?: AbortSignal,
  fetcher: Fetcher = fetch,
): Promise<void> {
  const response = await fetcher(`/api/devices/${encodeURIComponent(deviceID)}/unlock`, {
    method: "POST",
    credentials: "same-origin",
    headers: { accept: "application/json", "content-type": "application/json" },
    body: JSON.stringify({ envelope }),
    signal,
  });
  if (!response.ok) {
    throw responseError(response.status);
  }
  const value = await strictJSONResponse(response);
  if (!isRecord(value) || value.unlocked !== true) {
    throw new DeviceActionError("Unlock failed");
  }
}

export async function removeDevice(
  deviceID: string,
  signal?: AbortSignal,
  fetcher: Fetcher = fetch,
): Promise<void> {
  const response = await fetcher(`/api/devices/${encodeURIComponent(deviceID)}/`, {
    method: "DELETE",
    credentials: "same-origin",
    headers: { accept: "application/json", "content-type": "application/json" },
    body: "{}",
    signal,
  });
  if (!response.ok) {
    throw responseError(response.status);
  }
  const value = await strictJSONResponse(response);
  if (!isRecord(value) || value.deleted !== true) {
    throw new DeviceActionError("Remove failed");
  }
}

export async function callDevice<T = unknown>(
  deviceID: string,
  method: string,
  argumentsValue: unknown,
  signal?: AbortSignal,
  fetcher: Fetcher = fetch,
): Promise<CallResult<T>> {
  const response = await fetcher(`/api/devices/${encodeURIComponent(deviceID)}/call`, {
    method: "POST",
    credentials: "same-origin",
    headers: { accept: "application/x-ndjson", "content-type": "application/json" },
    body: JSON.stringify({ method, arguments: argumentsValue }),
    signal,
  });
  if (!response.ok) {
    const code = response.headers.get("x-executor-relay-error");
    if (code !== null && ["relay_empty_body", "relay_stream_failed", "relay_response_too_large", "relay_cancelled", "hub_owner_unconfirmed"].includes(code)) {
      // Use fixed error codes and a validated server-generated ID, never remote error text.
      try { await response.body?.cancel(); } catch { /* transport already closed */ }
      throw new DeviceResponseError(code, responseRequestID(response));
    }
    throw responseError(response.status);
  }
  return parseCallResponse(response) as Promise<CallResult<T>>;
}

async function strictJSONResponse(response: Response): Promise<unknown> {
  if (!response.ok) throw responseError(response.status);
  if (!response.headers.get("content-type")?.startsWith("application/json")) {
    throw new DeviceActionError("Invalid server response");
  }
  try {
    return await response.json();
  } catch {
    throw new DeviceActionError("Invalid server response");
  }
}

function responseError(status: number): Error {
  if (status === 401) return new DeviceLockedError();
  if (status === 503) return new DeviceOfflineError();
  if (status === 413) return new DeviceActionError("Request or response is too large");
  return new DeviceActionError();
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= maximum;
}
