import { describe, expect, it } from "vitest";
import vectors from "../../../internal/relay/testdata/wire-vectors.json";

import {
  canonicalEnvelope,
  decodeEnvelope,
  type MessageType,
} from "../../src/shared/wire";

const expectedTypes = [
  "version_negotiation",
  "enrollment",
  "heartbeat",
  "request",
  "response",
  "stream_chunk",
  "cancellation",
  "recovery_unlock",
  "grant_verification",
] as const satisfies readonly MessageType[];

describe("Go relay wire vectors", () => {
  it("accepts and reproduces all nine canonical messages byte-for-byte", () => {
    expect(Object.keys(vectors.messages)).toEqual(expectedTypes);

    for (const messageType of expectedTypes) {
      const fixture = vectors.messages[messageType];
      const expected = JSON.stringify(fixture);
      const decoded = decodeEnvelope(expected);

      expect(decoded.type).toBe(messageType);
      expect(canonicalEnvelope(decoded)).toBe(expected);
    }
  });

  it("rejects unknown fields, null payloads, and unsupported versions", () => {
    const fixture = vectors.messages.request;
    expect(() => decodeEnvelope(JSON.stringify({ ...fixture, secret: "must-not-pass" }))).toThrow(
      "invalid relay envelope",
    );
    expect(() => decodeEnvelope(JSON.stringify({ ...fixture, payload: null }))).toThrow(
      "invalid relay envelope",
    );
    expect(() => decodeEnvelope(JSON.stringify({ ...fixture, version: 2 }))).toThrow(
      "invalid relay envelope",
    );
  });

  it("rejects stream bytes that Go cannot decode from standard Base64", () => {
    const fixture = vectors.messages.stream_chunk;
    expect(() =>
      decodeEnvelope(
        JSON.stringify({
          ...fixture,
          payload: { ...fixture.payload, data: "not-standard-base64_" },
        }),
      ),
    ).toThrow("invalid relay envelope");
  });
});
