import { describe, expect, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import {
  generateSensitiveResponseKey,
  openSensitiveResultEnvelope,
  sensitiveResultAdditionalData,
} from "../../src/ui/sensitive-result";
import type { SensitiveResultContext } from "../../src/ui/sensitive-result";

describe("browser-sensitive lifecycle results", () => {
  it("opens the deterministic Go-compatible envelope with exact canonical AAD", async () => {
    const fixture = vectors.sensitive_result;
    expect(new TextDecoder().decode(sensitiveResultAdditionalData(fixture.context as SensitiveResultContext))).toBe(fixture.aad);
    const privateKey = await crypto.subtle.importKey(
      "jwk",
      fixture.test_only_browser_private_key,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      ["deriveBits"],
    );

    await expect(
      openSensitiveResultEnvelope(privateKey, fixture.expected_envelope, fixture.context as SensitiveResultContext),
    ).resolves.toEqual(JSON.parse(fixture.test_only_plaintext));
  });

  it("generates a non-extractable private key and exports only its public JWK", async () => {
    const responseKey = await generateSensitiveResponseKey();
    expect(responseKey.privateKey.extractable).toBe(false);
    expect(Object.keys(responseKey.publicJWK).sort()).toEqual(["crv", "kty", "x", "y"]);
    await expect(crypto.subtle.exportKey("jwk", responseKey.privateKey)).rejects.toThrow();
  });

  it("fails closed when ciphertext or any bound context field is changed", async () => {
    const fixture = vectors.sensitive_result;
    const privateKey = await crypto.subtle.importKey(
      "jwk",
      fixture.test_only_browser_private_key,
      { name: "ECDH", namedCurve: "P-256" },
      false,
      ["deriveBits"],
    );
    await expect(
      openSensitiveResultEnvelope(
        privateKey,
        { ...fixture.expected_envelope, method: "control.kill" },
        fixture.context as SensitiveResultContext,
      ),
    ).rejects.toThrow("Sensitive result could not be verified");
    await expect(
      openSensitiveResultEnvelope(
        privateKey,
        { ...fixture.expected_envelope, ciphertext: `${fixture.expected_envelope.ciphertext.slice(0, -1)}A` },
        fixture.context as SensitiveResultContext,
      ),
    ).rejects.toThrow("Sensitive result could not be verified");
  });
});
