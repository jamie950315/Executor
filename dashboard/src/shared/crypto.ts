import type { PublicKeyJWK, RecoveryContext, RecoveryEnvelope } from "./wire";

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: false });
const RECOVERY_ALGORITHM = "ECDH-P256+HKDF-SHA256+A256GCM" as const;
const RECOVERY_HKDF_INFO = encoder.encode("executor/recovery-envelope/v1");
const GRANT_TYPE = "executor-device-grant+jwt";
const MAX_GRANT_SECONDS = 30 * 24 * 60 * 60;
const MAX_GRANT_CLOCK_SKEW_SECONDS = 2 * 60;

export interface PrivateKeyJWK extends PublicKeyJWK {
  d: string;
}

export interface GrantClaims {
  version: 1;
  device_id: string;
  access_subject: string;
  browser_id: string;
  generation: number;
  issued_at: number;
  expires_at: number;
  jti: string;
}

export interface GrantExpectation {
  deviceID: string;
  accessSubject: string;
  browserID: string;
  generation: number;
  now: Date;
}

export function recoveryAdditionalData(context: RecoveryContext): Uint8Array {
  validateRecoveryContext(context);
  return encoder.encode(
    JSON.stringify({
      version: 1,
      algorithm: RECOVERY_ALGORITHM,
      device_id: context.device_id,
      access_subject: context.access_subject,
      browser_id: context.browser_id,
      generation: context.generation,
    }),
  );
}

export async function sealRecoveryEnvelope(
  devicePublicKey: PublicKeyJWK,
  context: RecoveryContext,
  recoveryKey: string,
): Promise<RecoveryEnvelope> {
  const pair = (await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, true, [
    "deriveBits",
  ])) as CryptoKeyPair;
  const privateJWK = await crypto.subtle.exportKey("jwk", pair.privateKey);
  const salt = crypto.getRandomValues(new Uint8Array(32));
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  try {
    return await sealRecoveryEnvelopeWithMaterial(
      devicePublicKey,
      context,
      recoveryKey,
      privateJWK as PrivateKeyJWK,
      salt,
      nonce,
    );
  } finally {
    salt.fill(0);
    nonce.fill(0);
  }
}

export async function sealRecoveryEnvelopeWithMaterial(
  devicePublicKey: PublicKeyJWK,
  context: RecoveryContext,
  recoveryKey: string,
  ephemeralPrivateKey: PrivateKeyJWK,
  salt: Uint8Array,
  nonce: Uint8Array,
): Promise<RecoveryEnvelope> {
  validateP256PublicJWK(devicePublicKey);
  validateP256PrivateJWK(ephemeralPrivateKey);
  validateRecoveryContext(context);
  if (recoveryKey.length === 0 || salt.byteLength !== 32 || nonce.byteLength !== 12) {
    throw new Error("invalid recovery envelope");
  }

  try {
    const privateKey = await crypto.subtle.importKey(
      "jwk",
      ephemeralPrivateKey,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      ["deriveBits"],
    );
    const publicKey = await crypto.subtle.importKey(
      "jwk",
      devicePublicKey,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      [],
    );
    const deriveAlgorithm: SubtleCryptoDeriveKeyAlgorithm = { name: "ECDH", $public: publicKey };
    Object.assign(deriveAlgorithm, { public: publicKey });
    const sharedSecret = new Uint8Array(await crypto.subtle.deriveBits(deriveAlgorithm, privateKey, 256));
    try {
      const hkdfKey = await crypto.subtle.importKey("raw", toArrayBuffer(sharedSecret), "HKDF", false, [
        "deriveKey",
      ]);
      const encryptionKey = await crypto.subtle.deriveKey(
        {
          name: "HKDF",
          hash: "SHA-256",
          salt: toArrayBuffer(salt),
          info: toArrayBuffer(RECOVERY_HKDF_INFO),
        },
        hkdfKey,
        { name: "AES-GCM", length: 256 },
        false,
        ["encrypt"],
      );
      const plaintext = encoder.encode(recoveryKey);
      try {
        const ciphertext = new Uint8Array(
          await crypto.subtle.encrypt(
            {
              name: "AES-GCM",
              iv: toArrayBuffer(nonce),
              additionalData: toArrayBuffer(recoveryAdditionalData(context)),
            },
            encryptionKey,
            toArrayBuffer(plaintext),
          ),
        );
        return {
          version: 1,
          algorithm: RECOVERY_ALGORITHM,
          device_id: context.device_id,
          access_subject: context.access_subject,
          browser_id: context.browser_id,
          generation: context.generation,
          ephemeral_public_key: {
            kty: "EC",
            crv: "P-256",
            x: ephemeralPrivateKey.x,
            y: ephemeralPrivateKey.y,
          },
          salt: encodeBase64URL(salt),
          nonce: encodeBase64URL(nonce),
          ciphertext: encodeBase64URL(ciphertext),
        };
      } finally {
        plaintext.fill(0);
      }
    } finally {
      sharedSecret.fill(0);
    }
  } catch {
    throw new Error("invalid recovery envelope");
  }
}

export async function verifyDeviceGrant(
  publicJWK: PublicKeyJWK,
  token: string,
  expected: GrantExpectation,
): Promise<GrantClaims> {
  try {
    validateP256PublicJWK(publicJWK);
    validateGrantExpectation(expected);
    if (token.length === 0 || token.length > 16 * 1024) {
      throw new Error();
    }
    const parts = token.split(".");
    if (parts.length !== 3 || parts.some((part) => part.length === 0)) {
      throw new Error();
    }
    const [headerSegment, claimsSegment, signatureSegment] = parts as [string, string, string];
    const header = parseGrantObject(headerSegment, ["alg", "typ", "version"]);
    if (header.alg !== "ES256" || header.typ !== GRANT_TYPE || header.version !== 1) {
      throw new Error();
    }
    const signature = decodeBase64URL(signatureSegment);
    if (signature.byteLength !== 64) {
      throw new Error();
    }
    const key = await crypto.subtle.importKey(
      "jwk",
      publicJWK,
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["verify"],
    );
    const verified = await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      key,
      toArrayBuffer(signature),
      toArrayBuffer(encoder.encode(`${headerSegment}.${claimsSegment}`)),
    );
    if (!verified) {
      throw new Error();
    }
    const claims = parseGrantClaims(claimsSegment);
    const now = Math.floor(expected.now.getTime() / 1000);
    if (
      claims.device_id !== expected.deviceID ||
      claims.access_subject !== expected.accessSubject ||
      claims.browser_id !== expected.browserID ||
      claims.generation !== expected.generation ||
      claims.expires_at <= now ||
      claims.issued_at > now + MAX_GRANT_CLOCK_SKEW_SECONDS
    ) {
      throw new Error();
    }
    return claims;
  } catch {
    throw new Error("invalid device grant");
  }
}

export function canonicalDeviceChallenge(deviceID: string, nonce: string, issuedAt: number): string {
  if (!nonEmpty(deviceID, 256) || !nonEmpty(nonce, 256) || !positiveInteger(issuedAt)) {
    throw new Error("invalid device challenge");
  }
  return JSON.stringify({
    version: 1,
    purpose: "executor-device-connect",
    device_id: deviceID,
    nonce,
    issued_at: issuedAt,
  });
}

export async function verifyDeviceChallenge(
  publicJWK: PublicKeyJWK,
  deviceID: string,
  nonce: string,
  issuedAt: number,
  signature: Uint8Array,
): Promise<boolean> {
  try {
    validateP256PublicJWK(publicJWK);
    if (signature.byteLength !== 64) {
      return false;
    }
    const key = await crypto.subtle.importKey(
      "jwk",
      publicJWK,
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["verify"],
    );
    return await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      key,
      toArrayBuffer(signature),
      toArrayBuffer(encoder.encode(canonicalDeviceChallenge(deviceID, nonce, issuedAt))),
    );
  } catch {
    return false;
  }
}

export async function sha256Hex(value: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", encoder.encode(value)));
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function encodeBase64URL(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

export function decodeBase64URL(value: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]+$/u.test(value)) {
    throw new Error("invalid base64url");
  }
  const remainder = value.length % 4;
  if (remainder === 1) {
    throw new Error("invalid base64url");
  }
  const padded = value.replaceAll("-", "+").replaceAll("_", "/") + "=".repeat((4 - remainder) % 4);
  const binary = atob(padded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

function parseGrantObject(segment: string, expectedKeys: readonly string[]): Record<string, unknown> {
  const bytes = decodeBase64URL(segment);
  if (bytes.byteLength === 0 || bytes.byteLength > 8 * 1024) {
    throw new Error();
  }
  const value: unknown = JSON.parse(decoder.decode(bytes));
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error();
  }
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  if (keys.length !== expectedKeys.length || !expectedKeys.every((key) => Object.hasOwn(record, key))) {
    throw new Error();
  }
  return record;
}

function parseGrantClaims(segment: string): GrantClaims {
  const record = parseGrantObject(segment, [
    "version",
    "device_id",
    "access_subject",
    "browser_id",
    "generation",
    "issued_at",
    "expires_at",
    "jti",
  ]);
  if (
    record.version !== 1 ||
    !nonEmpty(record.device_id, 256) ||
    !nonEmpty(record.access_subject, 512) ||
    !nonEmpty(record.browser_id, 256) ||
    !positiveInteger(record.generation) ||
    !positiveInteger(record.issued_at) ||
    !positiveInteger(record.expires_at) ||
    record.expires_at <= record.issued_at ||
    record.expires_at - record.issued_at > MAX_GRANT_SECONDS ||
    !nonEmpty(record.jti, 256)
  ) {
    throw new Error();
  }
  return record as unknown as GrantClaims;
}

function validateGrantExpectation(expected: GrantExpectation): void {
  if (
    !nonEmpty(expected.deviceID, 256) ||
    !nonEmpty(expected.accessSubject, 512) ||
    !nonEmpty(expected.browserID, 256) ||
    !positiveInteger(expected.generation) ||
    Number.isNaN(expected.now.getTime())
  ) {
    throw new Error();
  }
}

function validateRecoveryContext(context: RecoveryContext): void {
  if (
    !nonEmpty(context.device_id, 256) ||
    !nonEmpty(context.access_subject, 512) ||
    !nonEmpty(context.browser_id, 256) ||
    !positiveInteger(context.generation)
  ) {
    throw new Error("invalid recovery envelope");
  }
}

function validateP256PublicJWK(jwk: PublicKeyJWK): void {
  if (
    jwk.kty !== "EC" ||
    jwk.crv !== "P-256" ||
    decodeBase64URL(jwk.x).byteLength !== 32 ||
    decodeBase64URL(jwk.y).byteLength !== 32
  ) {
    throw new Error();
  }
}

function validateP256PrivateJWK(jwk: PrivateKeyJWK): void {
  validateP256PublicJWK(jwk);
  if (decodeBase64URL(jwk.d).byteLength !== 32) {
    throw new Error();
  }
}

function nonEmpty(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.length <= maximum;
}

function positiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function toArrayBuffer(value: Uint8Array): ArrayBuffer {
  return Uint8Array.from(value).buffer;
}
