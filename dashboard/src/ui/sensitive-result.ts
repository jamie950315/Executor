import { decodeBase64URL } from "../shared/base64";
import type { PublicKeyJWK } from "../shared/wire";

const encoder = new TextEncoder();
const algorithm = "ECDH-P256+HKDF-SHA256+A256GCM";
const hkdfInfo = encoder.encode("executor/sensitive-result/v1");

export interface SensitiveResultContext {
  device_id: string;
  access_subject: string;
  browser_id: string;
  generation: number;
  request_id: string;
  method: "control.rotate" | "control.kill";
}

export interface SensitiveResultEnvelope extends SensitiveResultContext {
  version: 1;
  algorithm: typeof algorithm;
  ephemeral_public_key: PublicKeyJWK;
  salt: string;
  nonce: string;
  ciphertext: string;
}

export interface SensitiveResult {
  recovery_key: string;
  url_secret: string;
  dashboard: string;
  partial?: boolean;
}

export async function generateSensitiveResponseKey(): Promise<{
  privateKey: CryptoKey;
  publicJWK: PublicKeyJWK;
}> {
  const pair = (await crypto.subtle.generateKey(
    { name: "ECDH", namedCurve: "P-256" },
    false,
    ["deriveBits"],
  )) as CryptoKeyPair;
  const exported = await crypto.subtle.exportKey("jwk", pair.publicKey);
  const publicJWK = strictPublicJWK({
    kty: exported.kty,
    crv: exported.crv,
    x: exported.x,
    y: exported.y,
  });
  return { privateKey: pair.privateKey, publicJWK };
}

export function sensitiveResultAdditionalData(context: SensitiveResultContext): Uint8Array {
  validateContext(context);
  return encoder.encode(
    JSON.stringify({
      version: 1,
      algorithm,
      device_id: context.device_id,
      access_subject: context.access_subject,
      browser_id: context.browser_id,
      generation: context.generation,
      request_id: context.request_id,
      method: context.method,
    }),
  );
}

export async function openSensitiveResultEnvelope(
  privateKey: CryptoKey,
  envelopeValue: unknown,
  expected: SensitiveResultContext,
): Promise<SensitiveResult> {
  try {
    validateContext(expected);
    const envelope = parseEnvelope(envelopeValue);
    if (
      envelope.device_id !== expected.device_id ||
      envelope.access_subject !== expected.access_subject ||
      envelope.browser_id !== expected.browser_id ||
      envelope.generation !== expected.generation ||
      envelope.request_id !== expected.request_id ||
      envelope.method !== expected.method
    ) {
      throw new Error();
    }
    if (privateKey.type !== "private" || privateKey.algorithm.name !== "ECDH" || privateKey.usages.length !== 1 || privateKey.usages[0] !== "deriveBits") {
      throw new Error();
    }
    const peer = await crypto.subtle.importKey(
      "jwk",
      envelope.ephemeral_public_key,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      [],
    );
    const shared = new Uint8Array(
      await crypto.subtle.deriveBits({ name: "ECDH", public: peer }, privateKey, 256),
    );
    const salt = decodeBase64URL(envelope.salt);
    const nonce = decodeBase64URL(envelope.nonce);
    const ciphertext = decodeBase64URL(envelope.ciphertext);
    try {
      const keyMaterial = await crypto.subtle.importKey("raw", shared, "HKDF", false, ["deriveKey"]);
      const key = await crypto.subtle.deriveKey(
        { name: "HKDF", hash: "SHA-256", salt, info: hkdfInfo },
        keyMaterial,
        { name: "AES-GCM", length: 256 },
        false,
        ["decrypt"],
      );
      const plaintext = new Uint8Array(
        await crypto.subtle.decrypt(
          { name: "AES-GCM", iv: nonce, additionalData: sensitiveResultAdditionalData(expected) },
          key,
          ciphertext,
        ),
      );
      try {
        return parseSensitiveResult(
          JSON.parse(new TextDecoder("utf-8", { fatal: true, ignoreBOM: false }).decode(plaintext)),
        );
      } finally {
        plaintext.fill(0);
      }
    } finally {
      shared.fill(0);
      salt.fill(0);
      nonce.fill(0);
      ciphertext.fill(0);
    }
  } catch {
    throw new Error("Sensitive result could not be verified");
  }
}

function parseEnvelope(value: unknown): SensitiveResultEnvelope {
  const keys = [
    "version", "algorithm", "device_id", "access_subject", "browser_id", "generation",
    "request_id", "method", "ephemeral_public_key", "salt", "nonce", "ciphertext",
  ];
  const record = exactRecord(value, keys);
  const context = {
    device_id: record.device_id,
    access_subject: record.access_subject,
    browser_id: record.browser_id,
    generation: record.generation,
    request_id: record.request_id,
    method: record.method,
  } as SensitiveResultContext;
  validateContext(context);
  if (record.version !== 1 || record.algorithm !== algorithm) throw new Error();
  const publicJWK = strictPublicJWK(record.ephemeral_public_key);
  if (typeof record.salt !== "string" || decodeBase64URL(record.salt).byteLength !== 32) throw new Error();
  if (typeof record.nonce !== "string" || decodeBase64URL(record.nonce).byteLength !== 12) throw new Error();
  if (typeof record.ciphertext !== "string" || decodeBase64URL(record.ciphertext).byteLength < 16) throw new Error();
  return {
    version: 1,
    algorithm,
    ...context,
    ephemeral_public_key: publicJWK,
    salt: record.salt,
    nonce: record.nonce,
    ciphertext: record.ciphertext,
  };
}

function parseSensitiveResult(value: unknown): SensitiveResult {
  const record = exactRecord(value, isRecord(value) && Object.hasOwn(value, "partial")
    ? ["recovery_key", "url_secret", "dashboard", "partial"]
    : ["recovery_key", "url_secret", "dashboard"]);
  if (
    !validText(record.recovery_key, 16 * 1024) ||
    !validText(record.url_secret, 16 * 1024) ||
    !validText(record.dashboard, 16 * 1024) ||
    (Object.hasOwn(record, "partial") && typeof record.partial !== "boolean")
  ) throw new Error();
  return {
    recovery_key: record.recovery_key,
    url_secret: record.url_secret,
    dashboard: record.dashboard,
    ...(typeof record.partial === "boolean" ? { partial: record.partial } : {}),
  };
}

function strictPublicJWK(value: unknown): PublicKeyJWK {
  const record = exactRecord(value, ["kty", "crv", "x", "y"]);
  if (
    record.kty !== "EC" || record.crv !== "P-256" ||
    typeof record.x !== "string" || decodeBase64URL(record.x).byteLength !== 32 ||
    typeof record.y !== "string" || decodeBase64URL(record.y).byteLength !== 32
  ) throw new Error();
  return { kty: "EC", crv: "P-256", x: record.x, y: record.y };
}

function validateContext(context: SensitiveResultContext): void {
  if (
    !validText(context.device_id, 256) || !validText(context.access_subject, 512) ||
    !validText(context.browser_id, 256) || !Number.isSafeInteger(context.generation) || context.generation <= 0 ||
    !validText(context.request_id, 256) ||
    (context.method !== "control.rotate" && context.method !== "control.kill")
  ) throw new Error();
}

function exactRecord(value: unknown, keys: readonly string[]): Record<string, unknown> {
  if (!isRecord(value)) throw new Error();
  const actual = Object.keys(value);
  if (actual.length !== keys.length || !keys.every((key) => Object.hasOwn(value, key))) throw new Error();
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= maximum;
}
