import { describe, expect, it, vi } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import { performUnlock } from "../../src/ui/device-actions";

describe("recovery unlock", () => {
  it("sends only an encrypted envelope and never touches browser storage", async () => {
    const marker = "SENSITIVE_BROWSER_RECOVERY_MARKER";
    const localSpy = vi.spyOn(Storage.prototype, "setItem");
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = String(init?.body);
      expect(body).not.toContain(marker);
      expect(body).toContain("ciphertext");
      return new Response('{"unlocked":true}', {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });

    await performUnlock(
      {
        device_id: "device-vector-1",
        public_jwk: { kty: "EC", crv: "P-256", x: vectors.device_public_key.x, y: vectors.device_public_key.y },
        generation: 7,
      },
      { access_subject: "access-user-1", browser_id: "browser-user-1" },
      marker,
      undefined,
      fetcher,
    );

    expect(fetcher).toHaveBeenCalledOnce();
    expect(localSpy).not.toHaveBeenCalled();
    localSpy.mockRestore();
  });
});
