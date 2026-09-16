import { MAXIMUM_RELAY_MESSAGE_BYTES, MAXIMUM_RELAY_RESPONSE_BYTES } from "../limits";
import { decodeEnvelope } from "./wire";

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

export class DeviceResponseError extends DeviceActionError {
  readonly code: string;
  readonly requestID: string | null;
  readonly receivedBytes: number;

  constructor(code: string, requestID: string | null, receivedBytes = 0) {
    super(`Invalid device response (${code}${requestID === null ? "" : `; request ${requestID}`}); operation outcome unconfirmed`);
    this.name = "DeviceResponseError";
    this.code = code;
    this.requestID = requestID;
    this.receivedBytes = receivedBytes;
  }
}

// Only emitted after a complete, correlated failure envelope reaches EOF.
// This reports a device failure, not a guarantee that no side effects occurred.
export class DeviceExecutionError extends DeviceActionError {
  constructor(readonly requestID: string, readonly code: string) {
    super();
    this.name = "DeviceExecutionError";
  }
}

export function responseRequestID(response: Response): string | null {
  const value = response.headers.get("x-executor-request-id");
  return value !== null && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/iu.test(value) ? value : null;
}

export async function parseCallResponse(
  response: Response,
  bounds: { maximumBytes: number; maximumLineBytes: number } = {
    maximumBytes: MAXIMUM_RELAY_RESPONSE_BYTES,
    maximumLineBytes: MAXIMUM_RELAY_MESSAGE_BYTES,
  },
): Promise<CallResult> {
  const expectedRequestID = responseRequestID(response);
  if (response.body === null) throw new DeviceResponseError("missing_body", expectedRequestID);
  if (!response.headers.get("content-type")?.startsWith("application/x-ndjson")) {
    throw new DeviceResponseError("invalid_content_type", expectedRequestID);
  }
  const reader = response.body.getReader();
  let bodyCancelled = false;
  const cancelBody = async (): Promise<void> => {
    if (bodyCancelled) return;
    bodyCancelled = true;
    try {
      await reader.cancel();
    } catch {
      // The transport may already be aborted or errored.
    }
  };
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
  let deviceFailure: string | undefined;

  const consumeLine = (line: string): void => {
    if (line.length === 0 || encoder.encode(line).byteLength > bounds.maximumLineBytes || complete) {
      throw new DeviceActionError("Invalid device response");
    }
    const envelope = decodeEnvelope(line);
    if ((envelope.type === "response" || envelope.type === "stream_chunk") && expectedRequestID !== null && envelope.payload.request_id !== expectedRequestID) {
      throw new DeviceResponseError("request_mismatch", expectedRequestID, totalBytes);
    }
    if (envelope.type === "response") {
      if (requestID !== null) {
        throw new DeviceActionError("Invalid device response");
      }
      requestID = envelope.payload.request_id;
      if (envelope.payload.failure !== undefined) {
        const code = envelope.payload.failure.code;
        deviceFailure = /^[a-z_]{1,64}$/.test(code) ? code : "device_action_failed";
      }
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
      let part: ReadableStreamReadResult<Uint8Array>;
      try { part = await reader.read(); }
      catch { throw new DeviceResponseError("stream_read_failed", expectedRequestID, totalBytes); }
      const { done, value } = part;
      if (done) break;
      totalBytes += value.byteLength;
      if (totalBytes > bounds.maximumBytes) {
        await cancelBody();
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
      throw new DeviceResponseError(totalBytes === 0 ? "empty_body" : "incomplete_response", expectedRequestID, totalBytes);
    }
    if (deviceFailure !== undefined) throw new DeviceExecutionError(requestID, deviceFailure);
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
    await cancelBody();
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
