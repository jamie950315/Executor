import { describe, expect, it, vi } from "vitest";
import { callDevice, DeviceResponseError, parseCallResponse } from "../../src/ui/api";
import { makeEnvelope } from "../../src/shared/wire";

const requestID = "d54c893c-4c31-4f97-b275-a0dbd0e05d0f";
const headers = { "content-type": "application/x-ndjson", "x-executor-request-id": requestID };
const bytes = (text: string): Uint8Array => new TextEncoder().encode(text);

describe("browser response diagnostics", () => {
  it.each(["relay_empty_body", "relay_stream_failed", "relay_response_too_large", "relay_cancelled"])("retains %s and request correlation without retrying", async (code) => {
    const fetcher = vi.fn().mockResolvedValue(new Response("SENSITIVE_REMOTE_ERROR", { status: 502, headers: { ...headers, "x-executor-relay-error": code } }));
    await expect(callDevice("fixture", "filesystem_write", {}, undefined, fetcher)).rejects.toMatchObject({ name: "DeviceResponseError", code, requestID, receivedBytes: 0 });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each([{ body: null, code: "missing_body" }, { body: "", code: "empty_body" }])("distinguishes $code", async ({ body, code }) => {
    await expect(parseCallResponse(new Response(body, { headers }))).rejects.toMatchObject({ code, requestID, receivedBytes: 0 });
  });

  it("distinguishes EOF after a partial response from a zero-byte body", async () => {
    const chunk = `${JSON.stringify(makeEnvelope("stream_chunk", "partial", { request_id: requestID, sequence: 0, data: btoa("{}"), final: false }))}\n`;
    await expect(parseCallResponse(new Response(chunk, { headers }))).rejects.toMatchObject({ code: "incomplete_response", requestID, receivedBytes: bytes(chunk).byteLength });
  });

  it("records read failure bytes without exposing transport exception text", async () => {
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
    const promise = parseCallResponse(new Response(body, { headers }));
    const assertion = expect(promise).rejects.toMatchObject({ code: "stream_read_failed", requestID, receivedBytes: 0 });
    controller.error(new Error("SENSITIVE_TRANSPORT_ERROR"));
    await assertion;
    await expect(promise).rejects.toThrow("operation outcome unconfirmed");
    try { await promise; } catch (error) { expect(String(error)).not.toContain("SENSITIVE_TRANSPORT_ERROR"); }
  });

  it("rejects a valid response with the wrong HTTP request correlation", async () => {
    const chunk = JSON.stringify(makeEnvelope("response", "wrong", { request_id: "another-request", result: {} }));
    await expect(parseCallResponse(new Response(chunk, { headers }))).rejects.toMatchObject({ code: "request_mismatch", requestID });
  });

  it("ignores untrusted correlation and unrecognized remote error headers", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("SENSITIVE_BODY", { status: 502, headers: { "x-executor-request-id": "SENSITIVE_ID", "x-executor-relay-error": "SENSITIVE_REASON" } }));
    try { await callDevice("fixture", "filesystem_write", {}, undefined, fetcher); throw new Error("expected failure"); }
    catch (error) { expect(String(error)).not.toContain("SENSITIVE"); }
    await expect(parseCallResponse(new Response("", { headers: { ...headers, "x-executor-request-id": "SENSITIVE_ID" } }))).rejects.toMatchObject({ code: "empty_body", requestID: null });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("preserves normal responses with a matching correlation ID", async () => {
    const line = JSON.stringify(makeEnvelope("response", "success", { request_id: requestID, result: { content: "", eof: true } }));
    await expect(parseCallResponse(new Response(line, { headers }))).resolves.toEqual({ requestID, result: { content: "", eof: true } });
    expect(new DeviceResponseError("empty_body", requestID)).toBeInstanceOf(Error);
  });
});
