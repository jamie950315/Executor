import { describe, expect, it, vi } from "vitest";

import { DeviceLockedError, callDevice, parseCallResponse } from "../../src/ui/api";
import { makeEnvelope } from "../../src/shared/wire";

const encoder = new TextEncoder();

function responseFromChunks(chunks: string[], status = 200): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
  return new Response(stream, {
    status,
    headers: { "content-type": "application/x-ndjson; charset=utf-8" },
  });
}

describe("Dashboard call client", () => {
  it("parses a strict response envelope split across transport chunks and preserves its request ID", async () => {
    const wire = `${JSON.stringify(makeEnvelope("response", "message-1", {
      request_id: "request-sensitive-1",
      result: { ready: true },
    }))}\n`;
    const result = await parseCallResponse(responseFromChunks([wire.slice(0, 13), wire.slice(13)]));
    expect(result).toEqual({ requestID: "request-sensitive-1", result: { ready: true } });
  });

  it("concatenates ordered standard-base64 stream chunks before strict JSON parsing", async () => {
    const payload = JSON.stringify({ content: "streamed" });
    const parts = [payload.slice(0, 8), payload.slice(8)];
    const lines = parts.map((part, sequence) =>
      JSON.stringify(makeEnvelope("stream_chunk", `message-${sequence}`, {
        request_id: "request-stream-1",
        sequence,
        data: btoa(part),
        final: sequence === parts.length - 1,
      })),
    );
    const result = await parseCallResponse(responseFromChunks([`${lines[0]}\n${lines[1]}\n`]));
    expect(result).toEqual({ requestID: "request-stream-1", result: { content: "streamed" } });
  });

  it("fails closed for out-of-order chunks, post-final data, failures, malformed base64, and bounds", async () => {
    const chunk = (sequence: number, data: string, final: boolean) =>
      JSON.stringify(makeEnvelope("stream_chunk", `message-${sequence}`, {
        request_id: "request-1",
        sequence,
        data,
        final,
      }));
    await expect(parseCallResponse(responseFromChunks([`${chunk(1, btoa("{}"), true)}\n`]))).rejects.toThrow(
      "Invalid device response",
    );
    await expect(
      parseCallResponse(responseFromChunks([`${chunk(0, btoa("{}"), true)}\n${chunk(1, btoa("{}"), true)}\n`])),
    ).rejects.toThrow("Invalid device response");
    const malformedBase64 = JSON.stringify({
      version: 1,
      type: "stream_chunk",
      message_id: "malformed-message",
      payload: { request_id: "request-1", sequence: 0, data: "bad_base64", final: true },
    });
    await expect(parseCallResponse(responseFromChunks([`${malformedBase64}\n`]))).rejects.toThrow(
      "Invalid device response",
    );
    await expect(
      parseCallResponse(
        responseFromChunks([`${JSON.stringify(makeEnvelope("response", "m", {
          request_id: "r",
          failure: { code: "host_action_failed" },
        }))}\n`]),
      ),
    ).rejects.toThrow("Device action failed");
    await expect(
      parseCallResponse(responseFromChunks(["123456789"]), { maximumBytes: 8, maximumLineBytes: 8 }),
    ).rejects.toThrow("Device response is too large");
  });

  it("maps a grant 401 to locked state and passes AbortController cancellation to fetch", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBeInstanceOf(AbortSignal);
      return new Response('{"error":"device locked"}', {
        status: 401,
        headers: { "content-type": "application/json" },
      });
    });
    const controller = new AbortController();
    await expect(
      callDevice("device-1", "device_status", { action: "summary" }, controller.signal, fetchMock),
    ).rejects.toBeInstanceOf(DeviceLockedError);
    expect(fetchMock).toHaveBeenCalledOnce();
  });
});
