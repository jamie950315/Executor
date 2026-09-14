import { describe, expect, it } from "vitest";

import { makeEnvelope } from "../../src/shared/wire";
import { parseCallResponse } from "../../src/ui/api";

function ndjsonResponse(body: BodyInit | null): Response {
  return new Response(body, {
    headers: { "content-type": "application/x-ndjson; charset=utf-8" },
  });
}

describe("relay result validation regressions", () => {
  it.each([
    { name: "missing body", body: null },
    { name: "zero-byte body", body: "" },
    { name: "empty line", body: "\n" },
  ])("rejects a $name", async ({ body }) => {
    await expect(parseCallResponse(ndjsonResponse(body))).rejects.toThrow("Invalid device response");
  });

  it("rejects the legacy response that omitted both result and failure", async () => {
    const wire = JSON.stringify({
      version: 1,
      type: "response",
      message_id: "legacy-empty-message",
      payload: { request_id: "legacy-empty-request" },
    });
    await expect(parseCallResponse(ndjsonResponse(`${wire}\n`))).rejects.toThrow("Invalid device response");
  });

  it("recognizes an explicit invalid_result response as a device action failure", async () => {
    const wire = JSON.stringify(makeEnvelope("response", "invalid-result-message", {
      request_id: "invalid-result-request",
      failure: { code: "invalid_result" },
    }));
    await expect(parseCallResponse(ndjsonResponse(`${wire}\n`))).rejects.toThrow("Device action failed");
  });

  it.each([
    { name: "null", result: null },
    { name: "empty string", result: "" },
    { name: "empty object", result: {} },
    { name: "empty array", result: [] },
    { name: "false", result: false },
    { name: "zero", result: 0 },
    { name: "empty file", result: { content: "", encoding: "base64", size: 0, returnedBytes: 0, eof: true } },
  ])("preserves a valid $name result", async ({ result }) => {
    const wire = JSON.stringify(makeEnvelope("response", "valid-result-message", {
      request_id: "valid-result-request",
      result,
    }));
    await expect(parseCallResponse(ndjsonResponse(`${wire}\n`))).resolves.toEqual({
      requestID: "valid-result-request",
      result,
    });
  });

  it("rejects a transport error before the first byte", async () => {
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.error(new Error("device offline"));
      },
    });
    await expect(parseCallResponse(ndjsonResponse(stream))).rejects.toThrow("Invalid device response");
  });

  it("rejects a stream that ends before its final chunk", async () => {
    const wire = JSON.stringify(makeEnvelope("stream_chunk", "incomplete-message", {
      request_id: "incomplete-request",
      sequence: 0,
      data: btoa("{}"),
      final: false,
    }));
    await expect(parseCallResponse(ndjsonResponse(`${wire}\n`))).rejects.toThrow("Invalid device response");
  });
});
