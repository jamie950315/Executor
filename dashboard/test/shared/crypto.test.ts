import { describe, expect, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import {
  canonicalDeviceChallenge,
  canonicalDeviceRefresh,
  decodeBase64URL as decodeStrictBase64URL,
  recoveryAdditionalData,
  sealRecoveryEnvelopeWithMaterial,
  verifyDeviceChallenge,
  verifyDeviceGrant,
} from "../../src/shared/crypto";

const encoder = new TextEncoder();
const devicePublicKey = {
  kty: "EC",
  crv: "P-256",
  x: vectors.device_public_key.x,
  y: vectors.device_public_key.y,
} as const;
const testPrivateKey = {
  kty: "EC",
  crv: "P-256",
  x: vectors.test_only_device_private_key.x,
  y: vectors.test_only_device_private_key.y,
  d: vectors.test_only_device_private_key.d,
} as const;
const testEphemeralPrivateKey = {
  kty: "EC",
  crv: "P-256",
  x: vectors.recovery.test_only_ephemeral_private_key.x,
  y: vectors.recovery.test_only_ephemeral_private_key.y,
  d: vectors.recovery.test_only_ephemeral_private_key.d,
} as const;

function decodeBase64URL(value: string): Uint8Array {
  return Uint8Array.from(Buffer.from(value, "base64url"));
}

describe("cross-language relay crypto", () => {
  it("canonicalizes signed generation refresh metadata byte for byte", () => {
    expect(
      canonicalDeviceRefresh({
        device_id: "device-vector-1",
        generation: 8,
        name: "Owner Mac",
        platform: "darwin",
        arch: "arm64",
        executor_version: "dev",
        mcp_url: "https://executor.example.test/mcp",
        issued_at: 1_700_000_001,
      }),
    ).toBe(
      '{"version":1,"purpose":"executor-device-refresh","device_id":"device-vector-1","generation":8,"name":"Owner Mac","platform":"darwin","arch":"arm64","executor_version":"dev","mcp_url":"https://executor.example.test/mcp","issued_at":1700000001}',
    );
  });

  it("reproduces the Go recovery AAD and deterministic envelope", async () => {
    expect(new TextDecoder().decode(recoveryAdditionalData(vectors.recovery.context))).toBe(
      vectors.recovery.aad,
    );

    const envelope = await sealRecoveryEnvelopeWithMaterial(
      devicePublicKey,
      vectors.recovery.context,
      vectors.recovery.test_only_plaintext,
      testEphemeralPrivateKey,
      decodeBase64URL(vectors.recovery.salt),
      decodeBase64URL(vectors.recovery.nonce),
    );

    expect(envelope).toEqual(vectors.recovery.expected_envelope);
  });

  it("verifies the Go ES256 compact grant with P1363 signature bytes", async () => {
    await expect(
      verifyDeviceGrant(devicePublicKey, vectors.grant.compact_jws, {
        deviceID: vectors.grant.claims.device_id,
        accessSubject: vectors.grant.claims.access_subject,
        browserID: vectors.grant.claims.browser_id,
        generation: vectors.grant.claims.generation,
        now: new Date(vectors.grant.claims.issued_at * 1000),
      }),
    ).resolves.toEqual(vectors.grant.claims);
  });

  it("rejects a grant whose signed context or signature is changed", async () => {
    await expect(
      verifyDeviceGrant(devicePublicKey, vectors.grant.compact_jws, {
        deviceID: "another-device",
        accessSubject: vectors.grant.claims.access_subject,
        browserID: vectors.grant.claims.browser_id,
        generation: vectors.grant.claims.generation,
        now: new Date(vectors.grant.claims.issued_at * 1000),
      }),
    ).rejects.toThrow("invalid device grant");

    const token = vectors.grant.compact_jws;
    const tampered = `${token.slice(0, -1)}${token.endsWith("A") ? "B" : "A"}`;
    await expect(
      verifyDeviceGrant(devicePublicKey, tampered, {
        deviceID: vectors.grant.claims.device_id,
        accessSubject: vectors.grant.claims.access_subject,
        browserID: vectors.grant.claims.browser_id,
        generation: vectors.grant.claims.generation,
        now: new Date(vectors.grant.claims.issued_at * 1000),
      }),
    ).rejects.toThrow("invalid device grant");
  });

  it("rejects non-canonical base64url unused bits in keys and grant signatures", async () => {
    const nonCanonicalX = `${vectors.device_public_key.x.slice(0, -1)}Z`;
    expect(() => decodeStrictBase64URL(nonCanonicalX)).toThrow("invalid base64url");

    const tokenParts = vectors.grant.compact_jws.split(".");
    const signature = tokenParts[2];
    if (tokenParts.length !== 3 || signature === undefined || !signature.endsWith("w")) {
      throw new Error("unexpected grant fixture");
    }
    const nonCanonicalGrant = `${tokenParts[0]}.${tokenParts[1]}.${signature.slice(0, -1)}x`;
    await expect(
      verifyDeviceGrant(devicePublicKey, nonCanonicalGrant, {
        deviceID: vectors.grant.claims.device_id,
        accessSubject: vectors.grant.claims.access_subject,
        browserID: vectors.grant.claims.browser_id,
        generation: vectors.grant.claims.generation,
        now: new Date(vectors.grant.claims.issued_at * 1000),
      }),
    ).rejects.toThrow("invalid device grant");
  });

  it("verifies only a signature over the canonical device challenge", async () => {
    const privateKey = await crypto.subtle.importKey(
      "jwk",
      testPrivateKey,
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["sign"],
    );
    const challenge = canonicalDeviceChallenge("device-vector-1", "nonce-vector", 1_700_000_000);
    const signature = new Uint8Array(
      await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, privateKey, encoder.encode(challenge)),
    );

    await expect(
      verifyDeviceChallenge(
        devicePublicKey,
        "device-vector-1",
        "nonce-vector",
        1_700_000_000,
        signature,
      ),
    ).resolves.toBe(true);
    await expect(
      verifyDeviceChallenge(
        devicePublicKey,
        "device-vector-1",
        "different-nonce",
        1_700_000_000,
        signature,
      ),
    ).resolves.toBe(false);
  });
});
