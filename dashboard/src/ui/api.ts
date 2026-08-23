import { MAXIMUM_RELAY_MESSAGE_BYTES, MAXIMUM_RELAY_RESPONSE_BYTES } from "../limits";
import { decodeEnvelope } from "../shared/wire";

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

export interface CallResult<T = unknown> {
  requestID: string;
  result: T;
}

export class DeviceLockedError extends Error {
  constructor() {
    super("Device is locked");
    this.name = "DeviceLockedError";
  }
}

export class DeviceOfflineError extends Error {
  constructor() {
    super("Device is offline");
    this.name = "DeviceOfflineError";
  }
}

export class DeviceActionError extends Error {
  constructor(message = "Device action failed") {
    super(message);
    this.name = "DeviceActionError";
  }
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
    throw responseError(response.status);
  }
  return parseCallResponse(response) as Promise<CallResult<T>>;
}

export async function parseCallResponse(
  response: Response,
  bounds: { maximumBytes: number; maximumLineBytes: number } = {
    maximumBytes: MAXIMUM_RELAY_RESPONSE_BYTES,
    maximumLineBytes: MAXIMUM_RELAY_MESSAGE_BYTES,
  },
): Promise<CallResult> {
  if (response.body === null || !response.headers.get("content-type")?.startsWith("application/x-ndjson")) {
    throw new DeviceActionError("Invalid device response");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false });
  const encoder = new TextEncoder();
  const streamParts: Uint8Array[] = [];
  let textBuffer = "";
  let totalBytes = 0;
  let decodedBytes = 0;
  let requestID: string | null = null;
  let nextSequence = 0;
  let complete = false;
  let responseResult: unknown;

  const consumeLine = (line: string): void => {
    if (line.length === 0 || encoder.encode(line).byteLength > bounds.maximumLineBytes || complete) {
      throw new DeviceActionError("Invalid device response");
    }
    const envelope = decodeEnvelope(line);
    if (envelope.type === "response") {
      if (requestID !== null || envelope.payload.failure !== undefined) {
        if (envelope.payload.failure !== undefined) {
          throw new DeviceActionError();
        }
        throw new DeviceActionError("Invalid device response");
      }
      requestID = envelope.payload.request_id;
      responseResult = envelope.payload.result;
      complete = true;
      return;
    }
    if (envelope.type !== "stream_chunk") {
      throw new DeviceActionError("Invalid device response");
    }
    if (
      envelope.payload.sequence !== nextSequence ||
      (requestID !== null && requestID !== envelope.payload.request_id)
    ) {
      throw new DeviceActionError("Invalid device response");
    }
    requestID = envelope.payload.request_id;
    const part = decodeStandardBase64(envelope.payload.data);
    decodedBytes += part.byteLength;
    if (decodedBytes > bounds.maximumBytes) {
      part.fill(0);
      throw new DeviceActionError("Device response is too large");
    }
    streamParts.push(part);
    nextSequence += 1;
    complete = envelope.payload.final;
  };

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      totalBytes += value.byteLength;
      if (totalBytes > bounds.maximumBytes) {
        await reader.cancel();
        throw new DeviceActionError("Device response is too large");
      }
      textBuffer += decoder.decode(value, { stream: true });
      let newline = textBuffer.indexOf("\n");
      while (newline >= 0) {
        consumeLine(textBuffer.slice(0, newline));
        textBuffer = textBuffer.slice(newline + 1);
        newline = textBuffer.indexOf("\n");
      }
      if (encoder.encode(textBuffer).byteLength > bounds.maximumLineBytes) {
        throw new DeviceActionError("Device response is too large");
      }
    }
    textBuffer += decoder.decode();
    if (textBuffer.length > 0) consumeLine(textBuffer);
    if (!complete || requestID === null) {
      throw new DeviceActionError("Invalid device response");
    }
    if (streamParts.length === 0) {
      return { requestID, result: responseResult };
    }
    const combined = new Uint8Array(decodedBytes);
    let offset = 0;
    for (const part of streamParts) {
      combined.set(part, offset);
      offset += part.byteLength;
      part.fill(0);
    }
    try {
      return {
        requestID,
        result: JSON.parse(new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(combined)),
      };
    } finally {
      combined.fill(0);
    }
  } catch (error) {
    for (const part of streamParts) part.fill(0);
    if (error instanceof DeviceActionError) throw error;
    throw new DeviceActionError("Invalid device response");
  } finally {
    reader.releaseLock();
  }
}

function decodeStandardBase64(value: string): Uint8Array {
  if (value.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) {
    throw new DeviceActionError("Invalid device response");
  }
  try {
    const binary = atob(value);
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    if (btoa(binary) !== value) throw new Error();
    return bytes;
  } catch {
    throw new DeviceActionError("Invalid device response");
  }
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
