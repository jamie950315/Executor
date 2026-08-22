import { DurableObject } from "cloudflare:workers";

import { getDevice } from "./db";
import { boundedResponseTotal, MAXIMUM_RELAY_MESSAGE_BYTES } from "./limits";
import { decodeBase64URL, encodeBase64URL, verifyDeviceChallenge } from "./shared/crypto";
import { canonicalEnvelope, decodeEnvelope, type RelayEnvelope } from "./shared/wire";

const relayTimeoutMilliseconds = 10_000;
const challengeLifetimeSeconds = 30;

interface PendingOnce {
  mode: "once";
  socket: WebSocket;
  resolve: (value: RelayOnceResult) => void;
  timeout: ReturnType<typeof setTimeout>;
}

interface PendingStream {
  mode: "stream";
  socket: WebSocket;
  controller: ReadableStreamDefaultController<Uint8Array>;
  nextSequence: number;
  totalBytes: number;
  timeout: ReturnType<typeof setTimeout>;
}

type PendingRelay = PendingOnce | PendingStream;

export type RelayOnceResult =
  | { ok: true; response: string }
  | { ok: false; error: RelayFailureCode };

export type RelayStreamResult =
  | { ok: true; stream: ReadableStream<Uint8Array> }
  | { ok: false; error: RelayFailureCode };

export type RelayFailureCode = "offline" | "duplicate" | "message_too_large" | "timeout" | "protocol";

interface ChallengeAttachment {
  version: 1;
  deviceID: string;
  generation: number;
  authenticated: false;
  nonce: string;
  issuedAt: number;
}

interface AuthenticatedAttachment {
  version: 1;
  deviceID: string;
  generation: number;
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
    const device = deviceID === null ? null : await getDevice(this.env.DB, deviceID);
    if (deviceID === null || device === null) {
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
      generation: device.generation,
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
    if (new TextEncoder().encode(wire).byteLength > MAXIMUM_RELAY_MESSAGE_BYTES) {
      return { ok: false, error: "message_too_large" };
    }

    return new Promise<RelayOnceResult>((resolve) => {
      const timeout = setTimeout(() => {
        if (this.pending.delete(requestID)) {
          this.sendCancellation(socket, requestID);
          resolve({ ok: false, error: "timeout" });
        }
      }, relayTimeoutMilliseconds);
      this.pending.set(requestID, { mode: "once", socket, resolve, timeout });
      try {
        socket.send(wire);
      } catch {
        this.failPending(requestID, "offline");
      }
    });
  }

  relayStream(envelope: RelayEnvelope<"request">): RelayStreamResult {
    const requestID = envelope.payload.request_id;
    const socket = this.authenticatedSocket();
    if (socket === null) {
      return { ok: false, error: "offline" };
    }
    if (this.pending.has(requestID)) {
      return { ok: false, error: "duplicate" };
    }
    const wire = canonicalEnvelope(envelope);
    if (new TextEncoder().encode(wire).byteLength > MAXIMUM_RELAY_MESSAGE_BYTES) {
      return { ok: false, error: "message_too_large" };
    }

    let streamController: ReadableStreamDefaultController<Uint8Array> | null = null;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        streamController = controller;
      },
      cancel: () => {
        const pending = this.pending.get(requestID);
        if (pending !== undefined && pending.socket === socket) {
          clearTimeout(pending.timeout);
          this.pending.delete(requestID);
          this.sendCancellation(socket, requestID);
        }
      },
    });
    if (streamController === null) {
      return { ok: false, error: "offline" };
    }
    const timeout = this.streamTimeout(requestID, socket);
    this.pending.set(requestID, {
      mode: "stream",
      socket,
      controller: streamController,
      nextSequence: 0,
      totalBytes: 0,
      timeout,
    });
    try {
      socket.send(wire);
    } catch {
      this.failPending(requestID, "offline");
      return { ok: false, error: "offline" };
    }
    return { ok: true, stream };
  }

  async disconnect(generation: number): Promise<void> {
    for (const socket of this.ctx.getWebSockets("device")) {
      if (relayAttachment(socket)?.generation === generation) {
        this.failPendingForSocket(socket, "offline");
        socket.close(1000, "device removed");
      }
    }
    await this.setDeviceOffline(null, generation);
  }

  override async webSocketMessage(socket: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const size = typeof message === "string" ? new TextEncoder().encode(message).byteLength : message.byteLength;
    if (size > MAXIMUM_RELAY_MESSAGE_BYTES) {
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
    const attachment = relayAttachment(socket);
    if (attachment?.authenticated !== true) {
      return;
    }
    this.failPendingForSocket(socket, "offline");
    if (!this.hasOtherAuthenticatedSocket(socket, attachment.generation)) {
      await this.setDeviceOffline(attachment.deviceID, attachment.generation);
    }
  }

  override async webSocketError(socket: WebSocket): Promise<void> {
    const attachment = relayAttachment(socket);
    if (attachment?.authenticated !== true) {
      return;
    }
    this.failPendingForSocket(socket, "offline");
    if (!this.hasOtherAuthenticatedSocket(socket, attachment.generation)) {
      await this.setDeviceOffline(attachment.deviceID, attachment.generation);
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
      device.generation !== attachment.generation ||
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

    const updatedAt = Date.now();
    const updated = await this.env.DB.prepare(
      `UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ?
      WHERE device_id = ? AND generation = ?`,
    )
      .bind(updatedAt, updatedAt, attachment.deviceID, attachment.generation)
      .run();
    if (updated.meta.changes !== 1) {
      socket.close(1008, "device authentication failed");
      return;
    }
    socket.serializeAttachment({
      version: 1,
      deviceID: attachment.deviceID,
      generation: attachment.generation,
      authenticated: true,
    } satisfies AuthenticatedAttachment);
    for (const existing of this.ctx.getWebSockets("device")) {
      if (existing !== socket && relayAttachment(existing)?.authenticated === true) {
        existing.close(1000, "connection replaced");
      }
    }
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
        envelope.payload.generation !== attachment.generation ||
        attachment.generation !== device.generation
      ) {
        socket.close(1008, "invalid heartbeat");
        return;
      }
      const now = Date.now();
      await this.env.DB.prepare(
        `UPDATE devices SET state = 'online', last_seen_at = ?, updated_at = ?
        WHERE device_id = ? AND generation = ?`,
      )
        .bind(now, now, attachment.deviceID, attachment.generation)
        .run();
      return;
    }
    if (envelope.type === "stream_chunk") {
      this.handleStreamChunk(socket, envelope);
      return;
    }
    if (envelope.type !== "response") {
      return;
    }
    const pending = this.pending.get(envelope.payload.request_id);
    if (pending === undefined || pending.socket !== socket) {
      return;
    }
    clearTimeout(pending.timeout);
    this.pending.delete(envelope.payload.request_id);
    const wire = `${canonicalEnvelope(envelope)}\n`;
    if (pending.mode === "once") {
      pending.resolve({ ok: true, response: wire.trimEnd() });
      return;
    }
    const bytes = new TextEncoder().encode(wire);
    if (boundedResponseTotal(pending.totalBytes, bytes.byteLength) === null) {
      this.sendCancellation(socket, envelope.payload.request_id);
      pending.controller.error(new Error("relay response too large"));
      return;
    }
    pending.controller.enqueue(bytes);
    pending.controller.close();
  }

  private authenticatedSocket(): WebSocket | null {
    for (const socket of this.ctx.getWebSockets("device")) {
      if (relayAttachment(socket)?.authenticated === true && socket.readyState === WebSocket.OPEN) {
        return socket;
      }
    }
    return null;
  }

  private hasOtherAuthenticatedSocket(excluded: WebSocket, generation: number): boolean {
    return this.ctx
      .getWebSockets("device")
      .some(
        (socket) =>
          socket !== excluded &&
          socket.readyState === WebSocket.OPEN &&
          relayAttachment(socket)?.authenticated === true &&
          relayAttachment(socket)?.generation === generation,
      );
  }

  private failPending(
    requestID: string,
    error: RelayFailureCode,
  ): void {
    const pending = this.pending.get(requestID);
    if (pending === undefined) {
      return;
    }
    clearTimeout(pending.timeout);
    this.pending.delete(requestID);
    if (pending.mode === "once") {
      pending.resolve({ ok: false, error });
    } else {
      pending.controller.error(new Error(relayFailureMessage(error)));
    }
  }

  private failPendingForSocket(socket: WebSocket, error: RelayFailureCode): void {
    for (const [requestID, pending] of this.pending) {
      if (pending.socket === socket) {
        this.failPending(requestID, error);
      }
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

  private handleStreamChunk(socket: WebSocket, envelope: RelayEnvelope<"stream_chunk">): void {
    const requestID = envelope.payload.request_id;
    const pending = this.pending.get(requestID);
    if (pending === undefined || pending.mode !== "stream" || pending.socket !== socket) {
      return;
    }
    if (envelope.payload.sequence !== pending.nextSequence) {
      this.sendCancellation(socket, requestID);
      this.failPending(requestID, "protocol");
      return;
    }
    const bytes = new TextEncoder().encode(`${canonicalEnvelope(envelope)}\n`);
    const total = boundedResponseTotal(pending.totalBytes, bytes.byteLength);
    if (total === null) {
      this.sendCancellation(socket, requestID);
      this.failPending(requestID, "message_too_large");
      return;
    }
    pending.controller.enqueue(bytes);
    pending.totalBytes = total;
    pending.nextSequence += 1;
    if (envelope.payload.final) {
      clearTimeout(pending.timeout);
      this.pending.delete(requestID);
      pending.controller.close();
    }
  }

  private streamTimeout(requestID: string, socket: WebSocket): ReturnType<typeof setTimeout> {
    return setTimeout(() => {
      if (this.pending.has(requestID)) {
        this.sendCancellation(socket, requestID);
        this.failPending(requestID, "timeout");
      }
    }, relayTimeoutMilliseconds);
  }

  private async setDeviceOffline(
    knownDeviceID: string | null = null,
    knownGeneration: number | null = null,
  ): Promise<void> {
    const deviceID = knownDeviceID ?? this.deviceIDFromAnyAttachment();
    const generation = knownGeneration ?? this.generationFromAnyAttachment();
    if (deviceID === null || generation === null) {
      return;
    }
    await this.env.DB.prepare(
      "UPDATE devices SET state = 'offline', updated_at = ? WHERE device_id = ? AND generation = ?",
    )
      .bind(Date.now(), deviceID, generation)
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

  private generationFromAnyAttachment(): number | null {
    for (const socket of this.ctx.getWebSockets("device")) {
      const attachment = relayAttachment(socket);
      if (attachment !== null) {
        return attachment.generation;
      }
    }
    return null;
  }
}

function relayFailureMessage(error: RelayFailureCode): string {
  switch (error) {
    case "offline":
      return "device offline";
    case "duplicate":
      return "duplicate relay request";
    case "message_too_large":
      return "relay response too large";
    case "timeout":
      return "relay timeout";
    case "protocol":
      return "invalid relay response";
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
    typeof record.generation !== "number" ||
    !Number.isSafeInteger(record.generation) ||
    record.generation <= 0 ||
    typeof record.authenticated !== "boolean"
  ) {
    return null;
  }
  if (record.authenticated) {
    return Object.keys(record).length === 4
      ? { version: 1, deviceID: record.deviceID, generation: record.generation, authenticated: true }
      : null;
  }
  if (
    Object.keys(record).length !== 6 ||
    typeof record.nonce !== "string" ||
    typeof record.issuedAt !== "number" ||
    !Number.isSafeInteger(record.issuedAt)
  ) {
    return null;
  }
  return {
    version: 1,
    deviceID: record.deviceID,
    generation: record.generation,
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
