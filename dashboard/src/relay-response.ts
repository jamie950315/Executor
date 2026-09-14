import { jsonResponse } from "./http";
import { MAXIMUM_RELAY_RESPONSE_BYTES } from "./limits";

export type RelayHTTPFailure = "relay_empty_body" | "relay_stream_failed" | "relay_response_too_large" | "relay_cancelled";

export interface RelayHTTPDiagnostic {
  request_id: string;
  stage: "first_byte" | "complete" | "failed" | "cancelled";
  code?: RelayHTTPFailure;
  bytes_received: number;
  elapsed_ms: number;
}

/** Commit success headers only after a nonempty relay chunk is available.
 * Buffer one chunk, preserve backpressure, and never replay a host operation.
 */
export async function relayHTTPResponse(
  source: ReadableStream<Uint8Array>,
  requestID: string,
  signal?: AbortSignal,
  record: (event: RelayHTTPDiagnostic) => void = (event) => console.info("executor_relay_http", event),
): Promise<Response> {
  const reader = source.getReader();
  const startedAt = Date.now();
  let totalBytes = 0;
  let ended = false;
  let cancelled = false;
  let cancellation: Promise<void> | undefined;
  let output: ReadableStreamDefaultController<Uint8Array> | undefined;

  const report = (stage: RelayHTTPDiagnostic["stage"], code?: RelayHTTPFailure): void => {
    try {
      record({ request_id: requestID, stage, ...(code === undefined ? {} : { code }), bytes_received: totalBytes, elapsed_ms: Math.max(0, Date.now() - startedAt) });
    } catch {
      // Diagnostics must never discard a completed response or replay the request.
    }
  };
  const finish = (): void => {
    if (ended) return;
    ended = true;
    signal?.removeEventListener("abort", abort);
    reader.releaseLock();
  };
  const cancelSource = (): Promise<void> => {
    cancellation ??= reader.cancel().catch(() => undefined);
    return cancellation;
  };
  function abort(): void {
    if (ended || cancelled) return;
    cancelled = true;
    report("cancelled", "relay_cancelled");
    output?.error(new Error("relay cancelled"));
    void cancelSource().then(finish);
  }
  const next = async (): Promise<ReadableStreamReadResult<Uint8Array>> => {
    while (true) {
      const part = await reader.read();
      if (cancelled) throw new Error("relay cancelled");
      if (part.done) return part;
      if (part.value.byteLength === 0) continue;
      totalBytes += part.value.byteLength;
      return part;
    }
  };
  const failure = async (code: RelayHTTPFailure): Promise<Response> => {
    if (!cancelled) report("failed", code);
    await cancelSource();
    finish();
    return jsonResponse({
      error: "Device response could not be confirmed",
      code,
      request_id: requestID,
      outcome: "unconfirmed",
    }, code === "relay_cancelled" ? 499 : 502, new Headers({
      "cache-control": "no-store",
      "x-executor-request-id": requestID,
      "x-executor-relay-error": code,
    }));
  };

  signal?.addEventListener("abort", abort, { once: true });
  if (signal?.aborted) abort();
  let first: ReadableStreamReadResult<Uint8Array>;
  try {
    first = await next();
  } catch {
    return failure(cancelled ? "relay_cancelled" : "relay_stream_failed");
  }
  if (first.done) return failure("relay_empty_body");
  if (totalBytes > MAXIMUM_RELAY_RESPONSE_BYTES) return failure("relay_response_too_large");
  report("first_byte");

  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      output = controller;
      if (cancelled) controller.error(new Error("relay cancelled"));
      else controller.enqueue(first.value!);
    },
    async pull(controller) {
      try {
        const part = await next();
        if (ended || cancelled) return;
        if (part.done) {
          report("complete");
          finish();
          controller.close();
        } else if (totalBytes > MAXIMUM_RELAY_RESPONSE_BYTES) {
          report("failed", "relay_response_too_large");
          controller.error(new Error("relay response too large"));
          await cancelSource();
          finish();
        } else {
          controller.enqueue(part.value);
        }
      } catch {
        if (!ended && !cancelled) {
          report("failed", "relay_stream_failed");
          controller.error(new Error("relay response interrupted; outcome unconfirmed"));
          await cancelSource();
          finish();
        }
      }
    },
    async cancel() {
      if (ended) return;
      cancelled = true;
      report("cancelled", "relay_cancelled");
      await cancelSource();
      finish();
    },
  }, { highWaterMark: 0 });
  return new Response(body, {
    headers: {
      "cache-control": "no-store",
      "content-type": "application/x-ndjson; charset=utf-8",
      "x-executor-request-id": requestID,
    },
  });
}
