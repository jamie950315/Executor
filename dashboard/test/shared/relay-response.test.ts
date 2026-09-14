import { describe, expect, it, vi } from "vitest";
import { relayHTTPResponse, type RelayHTTPDiagnostic } from "../../src/relay-response";
import { MAXIMUM_RELAY_RESPONSE_BYTES } from "../../src/limits";

const requestID = "5a492b37-2a29-4c91-8b58-d2e43f6ecc3d";
const encode = (value: string): Uint8Array => new TextEncoder().encode(value);

function fixture() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const cancel = vi.fn();
  const source = new ReadableStream<Uint8Array>({ start(value) { controller = value; }, cancel }, { highWaterMark: 0 });
  const events: RelayHTTPDiagnostic[] = [];
  return { source, controller, cancel, events, record: (value: RelayHTTPDiagnostic) => { events.push(value); } };
}

async function settle(): Promise<void> { for (let i = 0; i < 5; i++) await Promise.resolve(); }

describe("relay HTTP first-byte boundary", () => {
  it.each([false, true])("rejects an empty closed stream, with zero-size chunk=%s", async (zeroChunk) => {
    const f = fixture();
    if (zeroChunk) f.controller.enqueue(new Uint8Array());
    f.controller.close();
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    expect(response.status).toBe(502);
    expect(response.headers.get("x-executor-relay-error")).toBe("relay_empty_body");
    expect(await response.json()).toMatchObject({ code: "relay_empty_body", request_id: requestID, outcome: "unconfirmed" });
    expect(f.source.locked).toBe(false);
    expect(f.events).toHaveLength(1);
  });

  it("returns fixed metadata for an error before the first byte", async () => {
    const f = fixture();
    f.controller.error(new Error("SENSITIVE_REMOTE_PAYLOAD"));
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: "Device response could not be confirmed", code: "relay_stream_failed", request_id: requestID, outcome: "unconfirmed" });
    expect(JSON.stringify(f.events)).not.toContain("SENSITIVE_REMOTE_PAYLOAD");
    expect(f.events).toContainEqual(expect.objectContaining({ stage: "failed", bytes_received: 0 }));
    expect(f.source.locked).toBe(false);
  });

  it("holds success headers until the first nonzero chunk", async () => {
    const f = fixture();
    let settled = false;
    const pending = relayHTTPResponse(f.source, requestID, undefined, f.record).then((response) => { settled = true; return response; });
    f.controller.enqueue(new Uint8Array());
    await settle();
    expect(settled).toBe(false);
    const bytes = encode('{"result":""}\n');
    f.controller.enqueue(bytes);
    const response = await pending;
    expect(response.status).toBe(200);
    expect(response.headers.get("x-executor-request-id")).toBe(requestID);
    f.controller.close();
    expect(new Uint8Array(await response.arrayBuffer())).toEqual(bytes);
    expect(f.events.map((event) => event.stage)).toEqual(["first_byte", "complete"]);
  });

  it("preserves arbitrary UTF-8 boundaries and all subsequent chunks", async () => {
    const f = fixture();
    const bytes = encode('{"content":"繁體中文🙂"}\n');
    const pending = relayHTTPResponse(f.source, requestID, undefined, f.record);
    for (let i = 0; i < bytes.byteLength; i += 2) f.controller.enqueue(bytes.slice(i, i + 2));
    f.controller.close();
    expect(new Uint8Array(await (await pending).arrayBuffer())).toEqual(bytes);
    expect(f.events.at(-1)).toMatchObject({ stage: "complete", bytes_received: bytes.byteLength });
    expect(f.source.locked).toBe(false);
  });

  it("preserves streaming and reports failure after a delivered first chunk", async () => {
    const f = fixture();
    f.controller.enqueue(encode("first\n"));
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    const reader = response.body!.getReader();
    expect((await reader.read()).value).toEqual(encode("first\n"));
    f.controller.error(new Error("SENSITIVE_TRANSPORT_DETAIL"));
    await expect(reader.read()).rejects.toThrow("outcome unconfirmed");
    reader.releaseLock();
    expect(f.events.at(-1)).toMatchObject({ stage: "failed", code: "relay_stream_failed", bytes_received: 6 });
    expect(JSON.stringify(f.events)).not.toContain("SENSITIVE_TRANSPORT_DETAIL");
  });

  it("does not eagerly drain the source while the HTTP consumer is idle", async () => {
    let pulls = 0;
    const source = new ReadableStream<Uint8Array>({ pull(controller) { pulls++; controller.enqueue(encode("chunk\n")); } }, { highWaterMark: 0 });
    const response = await relayHTTPResponse(source, requestID, undefined, () => undefined);
    await settle();
    expect(pulls).toBe(1);
    const reader = response.body!.getReader();
    await reader.read();
    await settle();
    expect(pulls).toBe(1);
    await reader.read();
    expect(pulls).toBe(2);
    await reader.cancel();
    reader.releaseLock();
    expect(source.locked).toBe(false);
  });

  it("propagates consumer cancellation exactly once", async () => {
    const f = fixture();
    f.controller.enqueue(encode("first\n"));
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    await response.body!.cancel("SENSITIVE_BROWSER_REASON");
    expect(f.cancel).toHaveBeenCalledTimes(1);
    expect(f.cancel).toHaveBeenCalledWith(undefined);
    expect(f.source.locked).toBe(false);
    expect(f.events.at(-1)).toMatchObject({ stage: "cancelled" });
    expect(JSON.stringify(f.events)).not.toContain("SENSITIVE_BROWSER_REASON");
  });

  it.each(["already", "waiting"])("handles a request abort while %s before first byte", async (when) => {
    const f = fixture();
    const abort = new AbortController();
    if (when === "already") abort.abort();
    const pending = relayHTTPResponse(f.source, requestID, abort.signal, f.record);
    if (when === "waiting") { await settle(); abort.abort(); }
    const response = await pending;
    expect(response.status).toBe(499);
    expect(await response.json()).toMatchObject({ code: "relay_cancelled", outcome: "unconfirmed" });
    expect(f.cancel).toHaveBeenCalledTimes(1);
    expect(f.source.locked).toBe(false);
  });

  it("cancels an ongoing read when the request aborts after headers", async () => {
    const f = fixture();
    const abort = new AbortController();
    f.controller.enqueue(encode("first\n"));
    const response = await relayHTTPResponse(f.source, requestID, abort.signal, f.record);
    const reader = response.body!.getReader();
    await reader.read();
    const rejected = expect(reader.read()).rejects.toThrow("relay cancelled");
    abort.abort();
    await rejected;
    await settle();
    reader.releaseLock();
    expect(f.cancel).toHaveBeenCalledTimes(1);
    expect(f.source.locked).toBe(false);
  });

  it("enforces the total byte bound before returning headers", async () => {
    const f = fixture();
    f.controller.enqueue(new Uint8Array(MAXIMUM_RELAY_RESPONSE_BYTES + 1));
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    expect(response.status).toBe(502);
    expect(await response.json()).toMatchObject({ code: "relay_response_too_large" });
    expect(f.cancel).toHaveBeenCalledTimes(1);
  });

  it("enforces the cumulative byte bound after headers and releases the source", async () => {
    const f = fixture();
    f.controller.enqueue(encode("first"));
    const response = await relayHTTPResponse(f.source, requestID, undefined, f.record);
    const reader = response.body!.getReader();
    await reader.read();
    f.controller.enqueue(new Uint8Array(MAXIMUM_RELAY_RESPONSE_BYTES));
    await expect(reader.read()).rejects.toThrow("relay response too large");
    await settle();
    reader.releaseLock();
    expect(f.cancel).toHaveBeenCalledTimes(1);
    expect(f.source.locked).toBe(false);
    expect(f.events.at(-1)).toMatchObject({ stage: "failed", code: "relay_response_too_large" });
  });

  it.each(["success", "failure"])("isolates broken diagnostic sinks from %s results", async (outcome) => {
    const f = fixture();
    if (outcome === "success") f.controller.enqueue(encode("{}\n"));
    f.controller.close();
    const response = await relayHTTPResponse(f.source, requestID, undefined, () => { throw new Error("sink failed"); });
    expect(response.status).toBe(outcome === "success" ? 200 : 502);
    await response.arrayBuffer();
  });
});
