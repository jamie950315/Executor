import { DurableObject } from "cloudflare:workers";

import { getDevice } from "./db";
import { decodeBase64URL, encodeBase64URL, verifyDeviceChallenge } from "./shared/crypto";
import { canonicalEnvelope, decodeEnvelope, type RelayEnvelope } from "./shared/wire";

const maximumMessageBytes = 16 * 1024 * 1024;
const relayTimeoutMilliseconds = 10_000;
const challengeLifetimeSeconds = 30;

interface PendingRelay {
  resolve: (value: RelayOnceResult) => void;
  timeout: ReturnType<typeof setTimeout>;
}

export type RelayOnceResult =
  | { ok: true; response: string }
  | { ok: false; error: "offline" | "duplicate" | "message_too_large" | "timeout" };

interface ChallengeAttachment {
  version: 1;
  deviceID: string;
  authenticated: false;
  nonce: string;
  issuedAt: number;
}

interface AuthenticatedAttachment {
  version: 1;
  deviceID: string;
  authenticated: true;
}

type RelayAttachment = ChallengeAttachment | AuthenticatedAttachment;

export class DeviceRelay extends DurableObject<Env> {
  private readonly pending = new Map<string, PendingRelay>();

  override async fetch(request: Request): Promise<Response> {
    if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
      return new Response(null, { status: 426 });
    }
    const deviceID = request.headers.get("x-executor-device-id");
    if (deviceID === null || (await getDevice(this.env.DB, deviceID)) === null) {
      return new Response(null, { status: 404 });
    }

    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    const nonce = encodeBase64URL(crypto.getRandomValues(new Uint8Array(32)));
    const issuedAt = Math.floor(Date.now() / 1000);
    server.serializeAttachment({
      version: 1,
      deviceID,
      authenticated: false,
      nonce,
      issuedAt,
    } satisfies ChallengeAttachment);
    this.ctx.acceptWebSocket(server, ["device"]);
    server.send(
      JSON.stringify({
        version: 1,
        type: "device_challenge",
        device_id: deviceID,
        nonce,
        issued_at: issuedAt,
      }),
    );
    return new Response(null, { status: 101, webSocket: client });
  }

  async relayOnce(envelope: RelayEnvelope): Promise<RelayOnceResult> {
    const requestID = relayRequestID(envelope);
    const socket = this.authenticatedSocket();
    if (socket === null) {
      return { ok: false, error: "offline" };
    }
    if (this.pending.has(requestID)) {
      return { ok: false, error: "duplicate" };
    }
    const wire = canonicalEnvelope(envelope);
    if (new TextEncoder().encode(wire).byteLength > maximumMessageBytes) {
      return { ok: false, error: "message_too_large" };
    }

    return new Promise<RelayOnceResult>((resolve) => {
      const timeout = setTimeout(() => {
        if (this.pending.delete(requestID)) {
          this.sendCancellation(socket, requestID);
          resolve({ ok: false, error: "timeout" });
        }
      }, relayTimeoutMilliseconds);
      this.pending.set(requestID, { resolve, timeout });
      try {
        socket.send(wire);
      } catch {
        this.failPending(requestID, "offline");
      }
    });
  }

  async disconnect(): Promise<void> {
    for (const socket of this.ctx.getWebSockets("device")) {
      socket.close(1000, "device removed");
    }
    this.failAllPending("offline");
    await this.setDeviceOffline();
  }

  override async webSocketMessage(socket: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const size = typeof message === "string" ? new TextEncoder().encode(message).byteLength : message.byteLength;
    if (size > maximumMessageBytes) {
      socket.close(1009, "message too large");
      return;
    }
    const attachment = relayAttachment(socket);
    if (attachment === null) {
      socket.close(1008, "invalid connection state");
      return;
    }
    const wire = typeof message === "string" ? message : new TextDecoder().decode(message);
    if (!attachment.authenticated) {
      await this.authenticateSocket(socket, attachment, wire);
      return;
    }
    await this.handleAuthenticatedMessage(socket, attachment, wire);
  }

  override async webSocketClose(socket: WebSocket): Promise<void> {
    const deviceID = relayAttachment(socket)?.deviceID ?? null;
    this.failAllPending("offline");
    if (!this.hasOtherAuthenticatedSocket(socket)) {
      await this.setDeviceOffline(deviceID);
    }
  }

  override async webSocketError(socket: WebSocket): Promise<void> {
    const deviceID = relayAttachment(socket)?.deviceID ?? null;
    this.failAllPending("offline");
    if (!this.hasOtherAuthenticatedSocket(socket)) {
      await this.setDeviceOffline(deviceID);
    }
  }

  private async authenticateSocket(
    socket: WebSocket,
    attachment: ChallengeAttachment,
    message: string,
  ): Promise<void> {
    const response = parseChallengeResponse(message);
    const now = Math.floor(Date.now() / 1000);
    if (
      response === null ||
      response.nonce !== attachment.nonce ||
      response.issued_at !== attachment.issuedAt ||
      now < attachment.issuedAt ||
      now - attachment.issuedAt > challengeLifetimeSeconds
    ) {
      socket.close(1008, "device authentication failed");
      return;
    }
    const device = await getDevice(this.env.DB, attachment.deviceID);
    if (
      device === null ||
      !(await verifyDeviceChallenge(
        device.public_jwk,
        attachment.deviceID,
        attachment.nonce,
        attachment.issuedAt,
        response.signature,
      ))
    ) {
      socket.close(1008, "device authentication failed");
      return;
    }

    for (const existing of this.ctx.getWebSockets("device")) {
      if (existing !== socket && relayAttachment(existing)?.authenticated === true) {
        existing.close(1000, "connection replaced");
      }
    }
    socket.serializeAttachment({
      version: 1,
      deviceID: attachment.deviceID,
      authenticated: true,
    } satisfies AuthenticatedAttachment);
    const updatedAt = Date.now();
    await this.env.DB.prepare(
      "UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ? WHERE device_id = ?",
    )
      .bind(updatedAt, updatedAt, attachment.deviceID)
      .run();
    socket.send(JSON.stringify({ version: 1, type: "device_authenticated" }));
  }

  private async handleAuthenticatedMessage(
    socket: WebSocket,
    attachment: AuthenticatedAttachment,
    message: string,
  ): Promise<void> {
    let envelope: RelayEnvelope;
    try {
      envelope = decodeEnvelope(message);
    } catch {
      socket.close(1008, "invalid relay message");
      return;
    }
    if (envelope.type === "heartbeat") {
      const device = await getDevice(this.env.DB, attachment.deviceID);
      if (
        device === null ||
        envelope.payload.device_id !== attachment.deviceID ||
        envelope.payload.generation !== device.generation
      ) {
        socket.close(1008, "invalid heartbeat");
        return;
      }
      const now = Date.now();
      await this.env.DB.prepare(
        "UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ? WHERE device_id = ?",
      )
        .bind(now, now, attachment.deviceID)
        .run();
      return;
    }
    if (envelope.type !== "response") {
      return;
    }
    const pending = this.pending.get(envelope.payload.request_id);
    if (pending === undefined) {
      return;
    }
    clearTimeout(pending.timeout);
    this.pending.delete(envelope.payload.request_id);
    pending.resolve({ ok: true, response: canonicalEnvelope(envelope) });
  }

  private authenticatedSocket(): WebSocket | null {
    for (const socket of this.ctx.getWebSockets("device")) {
      if (relayAttachment(socket)?.authenticated === true && socket.readyState === WebSocket.OPEN) {
        return socket;
      }
    }
    return null;
  }

  private hasOtherAuthenticatedSocket(excluded: WebSocket): boolean {
    return this.ctx
      .getWebSockets("device")
      .some(
        (socket) =>
          socket !== excluded &&
          socket.readyState === WebSocket.OPEN &&
          relayAttachment(socket)?.authenticated === true,
      );
  }

  private failPending(
    requestID: string,
    error: Extract<RelayOnceResult, { ok: false }>["error"],
  ): void {
    const pending = this.pending.get(requestID);
    if (pending === undefined) {
      return;
    }
    clearTimeout(pending.timeout);
    this.pending.delete(requestID);
    pending.resolve({ ok: false, error });
  }

  private failAllPending(error: Extract<RelayOnceResult, { ok: false }>["error"]): void {
    for (const requestID of this.pending.keys()) {
      this.failPending(requestID, error);
    }
  }

  private sendCancellation(socket: WebSocket, requestID: string): void {
    try {
      socket.send(
        canonicalEnvelope({
          version: 1,
          type: "cancellation",
          message_id: crypto.randomUUID(),
          payload: { request_id: requestID },
        }),
      );
    } catch {
      // The timeout result is already deterministic if the device disconnected.
    }
  }

  private async setDeviceOffline(knownDeviceID: string | null = null): Promise<void> {
    const deviceID = knownDeviceID ?? this.deviceIDFromAnyAttachment();
    if (deviceID === null) {
      return;
    }
    await this.env.DB.prepare("UPDATE devices SET state = 'offline', updated_at = ? WHERE device_id = ?")
      .bind(Date.now(), deviceID)
      .run();
  }

  private deviceIDFromAnyAttachment(): string | null {
    for (const socket of this.ctx.getWebSockets("device")) {
      const attachment = relayAttachment(socket);
      if (attachment !== null) {
        return attachment.deviceID;
      }
    }
    return null;
  }
}

function relayRequestID(envelope: RelayEnvelope): string {
  if (envelope.type === "request") {
    return envelope.payload.request_id;
  }
  if (envelope.type === "recovery_unlock") {
    return envelope.message_id;
  }
  throw new Error("unsupported relay request");
}

function relayAttachment(socket: WebSocket): RelayAttachment | null {
  const value: unknown = socket.deserializeAttachment();
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  const record = value as Record<string, unknown>;
  if (
    record.version !== 1 ||
    typeof record.deviceID !== "string" ||
    record.deviceID.length === 0 ||
    typeof record.authenticated !== "boolean"
  ) {
    return null;
  }
  if (record.authenticated) {
    return Object.keys(record).length === 3
      ? { version: 1, deviceID: record.deviceID, authenticated: true }
      : null;
  }
  if (
    Object.keys(record).length !== 5 ||
    typeof record.nonce !== "string" ||
    typeof record.issuedAt !== "number" ||
    !Number.isSafeInteger(record.issuedAt)
  ) {
    return null;
  }
  return {
    version: 1,
    deviceID: record.deviceID,
    authenticated: false,
    nonce: record.nonce,
    issuedAt: record.issuedAt,
  };
}

function parseChallengeResponse(
  message: string,
): { nonce: string; issued_at: number; signature: Uint8Array } | null {
  try {
    const value: unknown = JSON.parse(message);
    if (typeof value !== "object" || value === null || Array.isArray(value)) {
      return null;
    }
    const record = value as Record<string, unknown>;
    if (
      Object.keys(record).length !== 5 ||
      record.version !== 1 ||
      record.type !== "device_challenge_response" ||
      typeof record.nonce !== "string" ||
      record.nonce.length !== 43 ||
      typeof record.issued_at !== "number" ||
      !Number.isSafeInteger(record.issued_at) ||
      typeof record.signature !== "string"
    ) {
      return null;
    }
    const signature = decodeBase64URL(record.signature);
    if (signature.byteLength !== 64) {
      return null;
    }
    return { nonce: record.nonce, issued_at: record.issued_at, signature };
  } catch {
    return null;
  }
}
