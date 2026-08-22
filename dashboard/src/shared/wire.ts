export const PROTOCOL_VERSION = 1 as const;

export const MESSAGE_TYPES = [
  "version_negotiation",
  "enrollment",
  "heartbeat",
  "request",
  "response",
  "stream_chunk",
  "cancellation",
  "recovery_unlock",
  "grant_verification",
] as const;

export type MessageType = (typeof MESSAGE_TYPES)[number];

export interface PublicKeyJWK {
  kty: "EC";
  crv: "P-256";
  x: string;
  y: string;
}

export interface RecoveryContext {
  device_id: string;
  access_subject: string;
  browser_id: string;
  generation: number;
}

export interface RecoveryEnvelope extends RecoveryContext {
  version: 1;
  algorithm: "ECDH-P256+HKDF-SHA256+A256GCM";
  ephemeral_public_key: PublicKeyJWK;
  salt: string;
  nonce: string;
  ciphertext: string;
}

interface PayloadMap {
  version_negotiation: { supported_versions: number[] };
  enrollment: RecoveryContext & { device_public_key: PublicKeyJWK };
  heartbeat: { device_id: string; generation: number; sent_at: number };
  request: { request_id: string; method: string; arguments: unknown };
  response: { request_id: string; result?: unknown; failure?: { code: string } };
  stream_chunk: { request_id: string; sequence: number; data: string; final: boolean };
  cancellation: { request_id: string };
  recovery_unlock: { envelope: RecoveryEnvelope };
  grant_verification: { grant: string };
}

export type RelayEnvelope<T extends MessageType = MessageType> = T extends MessageType
  ? {
      version: 1;
      type: T;
      message_id: string;
      payload: PayloadMap[T];
    }
  : never;

const envelopeKeys = ["version", "type", "message_id", "payload"];

export function decodeEnvelope(input: string | Uint8Array): RelayEnvelope {
  let value: unknown;
  try {
    value = JSON.parse(
      typeof input === "string"
        ? input
        : new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(input),
    );
  } catch {
    throw invalidEnvelope();
  }
  return validateEnvelope(value);
}

export function canonicalEnvelope(envelope: RelayEnvelope): string {
  const valid = validateEnvelope(envelope);
  return JSON.stringify({
    version: valid.version,
    type: valid.type,
    message_id: valid.message_id,
    payload: canonicalPayload(valid),
  });
}

export function makeEnvelope<T extends MessageType>(
  type: T,
  messageID: string,
  payload: PayloadMap[T],
): RelayEnvelope<T> {
  return validateEnvelope({
    version: PROTOCOL_VERSION,
    type,
    message_id: messageID,
    payload,
  }) as RelayEnvelope<T>;
}

function validateEnvelope(value: unknown): RelayEnvelope {
  const record = exactRecord(value, envelopeKeys);
  if (
    record.version !== PROTOCOL_VERSION ||
    !isMessageType(record.type) ||
    !nonEmptyString(record.message_id, 256)
  ) {
    throw invalidEnvelope();
  }
  validatePayload(record.type, record.payload);
  return record as unknown as RelayEnvelope;
}

function validatePayload(type: MessageType, payload: unknown): void {
  switch (type) {
    case "version_negotiation": {
      const value = exactRecord(payload, ["supported_versions"]);
      if (
        !Array.isArray(value.supported_versions) ||
        value.supported_versions.length === 0 ||
        !value.supported_versions.every((version) => unsignedInteger(version, 65_535))
      ) {
        throw invalidEnvelope();
      }
      return;
    }
    case "enrollment": {
      const value = exactRecord(payload, [
        "device_id",
        "access_subject",
        "browser_id",
        "generation",
        "device_public_key",
      ]);
      validateContext(value);
      validatePublicJWK(value.device_public_key);
      return;
    }
    case "heartbeat": {
      const value = exactRecord(payload, ["device_id", "generation", "sent_at"]);
      if (
        !nonEmptyString(value.device_id, 256) ||
        !positiveInteger(value.generation) ||
        !positiveInteger(value.sent_at)
      ) {
        throw invalidEnvelope();
      }
      return;
    }
    case "request": {
      const value = exactRecord(payload, ["request_id", "method", "arguments"]);
      if (!nonEmptyString(value.request_id, 256) || !nonEmptyString(value.method, 256)) {
        throw invalidEnvelope();
      }
      return;
    }
    case "response": {
      const value = recordWithAllowedKeys(payload, ["request_id", "result", "failure"]);
      if (!nonEmptyString(value.request_id, 256)) {
        throw invalidEnvelope();
      }
      const hasResult = Object.hasOwn(value, "result");
      const hasFailure = Object.hasOwn(value, "failure");
      if (hasResult === hasFailure) {
        throw invalidEnvelope();
      }
      if (hasFailure) {
        const failure = exactRecord(value.failure, ["code"]);
        if (!nonEmptyString(failure.code, 128)) {
          throw invalidEnvelope();
        }
      }
      return;
    }
    case "stream_chunk": {
      const value = exactRecord(payload, ["request_id", "sequence", "data", "final"]);
      if (
        !nonEmptyString(value.request_id, 256) ||
        !unsignedInteger(value.sequence, Number.MAX_SAFE_INTEGER) ||
        typeof value.data !== "string" ||
        typeof value.final !== "boolean"
      ) {
        throw invalidEnvelope();
      }
      return;
    }
    case "cancellation": {
      const value = exactRecord(payload, ["request_id"]);
      if (!nonEmptyString(value.request_id, 256)) {
        throw invalidEnvelope();
      }
      return;
    }
    case "recovery_unlock": {
      const value = exactRecord(payload, ["envelope"]);
      validateRecoveryEnvelope(value.envelope);
      return;
    }
    case "grant_verification": {
      const value = exactRecord(payload, ["grant"]);
      if (!nonEmptyString(value.grant, 16 * 1024)) {
        throw invalidEnvelope();
      }
      return;
    }
  }
}

function canonicalPayload(envelope: RelayEnvelope): unknown {
  switch (envelope.type) {
    case "version_negotiation":
      return { supported_versions: envelope.payload.supported_versions };
    case "enrollment":
      return {
        device_id: envelope.payload.device_id,
        access_subject: envelope.payload.access_subject,
        browser_id: envelope.payload.browser_id,
        generation: envelope.payload.generation,
        device_public_key: canonicalPublicJWK(envelope.payload.device_public_key),
      };
    case "heartbeat":
      return {
        device_id: envelope.payload.device_id,
        generation: envelope.payload.generation,
        sent_at: envelope.payload.sent_at,
      };
    case "request":
      return {
        request_id: envelope.payload.request_id,
        method: envelope.payload.method,
        arguments: envelope.payload.arguments,
      };
    case "response":
      return envelope.payload.failure === undefined
        ? { request_id: envelope.payload.request_id, result: envelope.payload.result }
        : { request_id: envelope.payload.request_id, failure: { code: envelope.payload.failure.code } };
    case "stream_chunk":
      return {
        request_id: envelope.payload.request_id,
        sequence: envelope.payload.sequence,
        data: envelope.payload.data,
        final: envelope.payload.final,
      };
    case "cancellation":
      return { request_id: envelope.payload.request_id };
    case "recovery_unlock":
      return { envelope: canonicalRecoveryEnvelope(envelope.payload.envelope) };
    case "grant_verification":
      return { grant: envelope.payload.grant };
  }
}

function validateRecoveryEnvelope(value: unknown): asserts value is RecoveryEnvelope {
  const envelope = exactRecord(value, [
    "version",
    "algorithm",
    "device_id",
    "access_subject",
    "browser_id",
    "generation",
    "ephemeral_public_key",
    "salt",
    "nonce",
    "ciphertext",
  ]);
  validateContext(envelope);
  if (
    envelope.version !== PROTOCOL_VERSION ||
    envelope.algorithm !== "ECDH-P256+HKDF-SHA256+A256GCM" ||
    !base64URLString(envelope.salt, 43, 43) ||
    !base64URLString(envelope.nonce, 16, 16) ||
    !base64URLString(envelope.ciphertext, 22, 16 * 1024 * 1024)
  ) {
    throw invalidEnvelope();
  }
  validatePublicJWK(envelope.ephemeral_public_key);
}

function canonicalRecoveryEnvelope(value: RecoveryEnvelope): RecoveryEnvelope {
  return {
    version: value.version,
    algorithm: value.algorithm,
    device_id: value.device_id,
    access_subject: value.access_subject,
    browser_id: value.browser_id,
    generation: value.generation,
    ephemeral_public_key: canonicalPublicJWK(value.ephemeral_public_key),
    salt: value.salt,
    nonce: value.nonce,
    ciphertext: value.ciphertext,
  };
}

function validateContext(value: Record<string, unknown>): void {
  if (
    !nonEmptyString(value.device_id, 256) ||
    !nonEmptyString(value.access_subject, 512) ||
    !nonEmptyString(value.browser_id, 256) ||
    !positiveInteger(value.generation)
  ) {
    throw invalidEnvelope();
  }
}

function validatePublicJWK(value: unknown): asserts value is PublicKeyJWK {
  const jwk = exactRecord(value, ["kty", "crv", "x", "y"]);
  if (
    jwk.kty !== "EC" ||
    jwk.crv !== "P-256" ||
    !base64URLString(jwk.x, 43, 43) ||
    !base64URLString(jwk.y, 43, 43)
  ) {
    throw invalidEnvelope();
  }
}

function canonicalPublicJWK(value: PublicKeyJWK): PublicKeyJWK {
  return { kty: value.kty, crv: value.crv, x: value.x, y: value.y };
}

function exactRecord(value: unknown, keys: readonly string[]): Record<string, unknown> {
  const record = recordWithAllowedKeys(value, keys);
  if (Object.keys(record).length !== keys.length || !keys.every((key) => Object.hasOwn(record, key))) {
    throw invalidEnvelope();
  }
  return record;
}

function recordWithAllowedKeys(value: unknown, keys: readonly string[]): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw invalidEnvelope();
  }
  const record = value as Record<string, unknown>;
  if (Object.keys(record).some((key) => !keys.includes(key))) {
    throw invalidEnvelope();
  }
  return record;
}

function isMessageType(value: unknown): value is MessageType {
  return typeof value === "string" && (MESSAGE_TYPES as readonly string[]).includes(value);
}

function nonEmptyString(value: unknown, maximumLength: number): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.length <= maximumLength;
}

function unsignedInteger(value: unknown, maximum: number): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 && value <= maximum;
}

function positiveInteger(value: unknown): value is number {
  return unsignedInteger(value, Number.MAX_SAFE_INTEGER) && value > 0;
}

function base64URLString(value: unknown, minimumLength: number, maximumLength: number): value is string {
  return (
    typeof value === "string" &&
    value.length >= minimumLength &&
    value.length <= maximumLength &&
    /^[A-Za-z0-9_-]+$/.test(value)
  );
}

function invalidEnvelope(): Error {
  return new Error("invalid relay envelope");
}
