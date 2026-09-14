import type { CallResult } from "./api";
import type { DeviceCall } from "./panels/types";

export const FILE_PAGE_BYTES = 65536;
export const MAXIMUM_BROWSER_FILE_BYTES = 32 * 1024 * 1024;
const pageFields = ["offsetBytes", "returnedBytes", "nextOffsetBytes", "truncated", "eof"] as const;

// Collect exact bytes before decoding text. A failed or canceled page produces
// no partial result for the editor to save. Reads observe a live file, not a snapshot.
export async function readCompleteFile(
  call: DeviceCall,
  path: string,
  encoding: "utf8" | "base64",
  signal?: AbortSignal,
): Promise<CallResult<{ content: string; encoding: string; size: number }>> {
  const chunks: Uint8Array[] = [];
  let offset = 0;
  let requestID: string;
  for (;;) {
    signal?.throwIfAborted();
    const response = await call("filesystem_read", {
      action: "read_file", path, privilege: "owner", encoding: "base64", offset, limit: FILE_PAGE_BYTES,
    }, signal);
    signal?.throwIfAborted();
    requestID = response.requestID;
    const value = response.result;
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid file response");
    const record = value as Record<string, unknown>;
    if (typeof record.content !== "string") throw new Error("Missing file content");
    const paged = pageFields.some(key => Object.hasOwn(record, key));
    if (offset > 0 && !paged) throw new Error("File pagination metadata disappeared");
    // Legacy UTF-8 fixtures/clients may omit encoding. Current base64 requests
    // explicitly return encoding, even on older Executor releases.
    const responseEncoding = record.encoding ?? encoding;
    if (responseEncoding !== "base64" && responseEncoding !== "utf8") throw new Error("Invalid file encoding");
    const maximum = paged ? FILE_PAGE_BYTES : MAXIMUM_BROWSER_FILE_BYTES;
    if (record.content.length > Math.ceil(maximum / 3) * 4) throw new Error("File response exceeds the browser memory limit");
    const data = responseEncoding === "base64" ? decodeBase64(record.content) : new TextEncoder().encode(record.content);
    if (data.length > maximum || offset + data.length > MAXIMUM_BROWSER_FILE_BYTES) throw new Error("File exceeds the 32 MiB browser memory limit");
    if (Object.hasOwn(record, "size") && record.size !== data.length) throw new Error("File byte count mismatch");
    if (paged) {
      if (responseEncoding !== "base64" || !pageFields.every(key => Object.hasOwn(record, key)) ||
          record.offsetBytes !== offset || record.returnedBytes !== data.length ||
          record.nextOffsetBytes !== offset + data.length ||
          typeof record.truncated !== "boolean" || typeof record.eof !== "boolean" ||
          record.truncated === record.eof || (record.truncated && data.length === 0)) {
        throw new Error("Invalid file pagination metadata");
      }
    }
    chunks.push(data);
    offset += data.length;
    if (!paged || record.eof) break;
    if (offset >= MAXIMUM_BROWSER_FILE_BYTES) throw new Error("File exceeds the 32 MiB browser memory limit");
  }
  signal?.throwIfAborted();
  const bytes = new Uint8Array(offset);
  let position = 0;
  for (const chunk of chunks) { bytes.set(chunk, position); position += chunk.length; }
  const content = encoding === "utf8"
    ? new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes)
    : encodeBase64(bytes);
  return { requestID, result: { content, encoding, size: bytes.length } };
}

function decodeBase64(content: string): Uint8Array {
  const binary = atob(content);
  if (btoa(binary) !== content) throw new Error("Invalid canonical base64 file content");
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}

function encodeBase64(bytes: Uint8Array): string {
  const parts: string[] = [];
  // Each full piece has a multiple of three bytes, so only the last is padded.
  for (let start = 0; start < bytes.length; start += 24576) {
    parts.push(btoa(String.fromCharCode(...bytes.subarray(start, start + 24576))));
  }
  return parts.join("");
}
