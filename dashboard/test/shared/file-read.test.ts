import { describe, expect, it, vi } from "vitest";
import { FILE_PAGE_BYTES, readCompleteFile } from "../../src/ui/file-read";
import type { DeviceCall } from "../../src/ui/panels/types";

function page(content: string, offset: number, eof: boolean) {
  const size = atob(content).length;
  return { content, encoding: "base64", size, offsetBytes: offset, returnedBytes: size, nextOffsetBytes: offset + size, truncated: !eof, eof };
}

describe("file byte pagination", () => {
  it("joins binary pages with independent base64 padding", async () => {
    const call = vi.fn<DeviceCall>()
      .mockResolvedValueOnce({ requestID: "one", result: page("AP8=", 0, false) })
      .mockResolvedValueOnce({ requestID: "two", result: page("QQ==", 2, true) });
    const result = await readCompleteFile(call, "/fixture", "base64");
    expect(result.result).toEqual({ content: "AP9B", encoding: "base64", size: 3 });
    expect(call.mock.calls[1]?.[1]).toMatchObject({ offset: 2, limit: FILE_PAGE_BYTES, encoding: "base64" });
  });

  it("preserves a UTF-8 BOM and characters split across pages", async () => {
    const bytes = new TextEncoder().encode("\uFEFF中文");
    const call: DeviceCall = async (_method, args) => {
      const { offset } = args as { offset: number };
      const byte = bytes[offset];
      if (byte === undefined) throw new Error("Unexpected fixture offset");
      return { requestID: "read", result: page(btoa(String.fromCharCode(byte)), offset, offset + 1 === bytes.length) };
    };
    expect((await readCompleteFile(call, "/fixture", "utf8")).result.content).toBe("\uFEFF中文");
  });

  it("accepts legacy whole-file base64 and text responses", async () => {
    const binary: DeviceCall = async () => ({ requestID: "read", result: { content: "aGk=", encoding: "base64", size: 2 } });
    const text: DeviceCall = async () => ({ requestID: "read", result: { content: "中文" } });
    expect((await readCompleteFile(binary, "/fixture", "utf8")).result.content).toBe("hi");
    expect((await readCompleteFile(text, "/fixture", "utf8")).result.size).toBe(6);
  });

  it.each([
    { offsetBytes: 1 }, { nextOffsetBytes: 0 }, { returnedBytes: 0 }, { size: 5 },
    { truncated: "false" }, { eof: false }, { encoding: "utf8" }, { encoding: "hex" },
    { content: "YQ==\n" }, { content: "YR==" }, { content: "YQ" },
  ])("rejects invalid metadata or bytes %j without retry", async change => {
    const call = vi.fn<DeviceCall>(async () => ({ requestID: "read", result: { ...page("YQ==", 0, true), ...change } }));
    await expect(readCompleteFile(call, "/fixture", "utf8")).rejects.toThrow();
    expect(call).toHaveBeenCalledTimes(1);
  });

  it("rejects partial metadata and zero-progress continuation", async () => {
    for (const result of [{ content: "YQ==", encoding: "base64", eof: true }, page("", 0, false)]) {
      const call: DeviceCall = async () => ({ requestID: "read", result });
      await expect(readCompleteFile(call, "/fixture", "base64")).rejects.toThrow("pagination");
    }
  });

  it("rejects oversized pages before returning editable text", async () => {
    const call: DeviceCall = async () => ({ requestID: "read", result: page(btoa("a".repeat(FILE_PAGE_BYTES + 1)), 0, true) });
    await expect(readCompleteFile(call, "/fixture", "utf8")).rejects.toThrow("limit");
  });

  it("honors cancellation even when a call resolves after abort", async () => {
    const controller = new AbortController();
    const call = vi.fn<DeviceCall>(async () => {
      controller.abort();
      return { requestID: "read", result: page("YQ==", 0, false) };
    });
    await expect(readCompleteFile(call, "/fixture", "utf8", controller.signal)).rejects.toThrow();
    expect(call).toHaveBeenCalledTimes(1);
  });

  it("rejects disappearing metadata or failed later pages without returning a partial file", async () => {
    const call = vi.fn<DeviceCall>().mockResolvedValueOnce({ requestID: "one", result: page("YQ==", 0, false) })
      .mockResolvedValueOnce({ requestID: "two", result: { content: "Yg==", encoding: "base64" } });
    await expect(readCompleteFile(call, "/fixture", "utf8")).rejects.toThrow("disappeared");
    const failed = vi.fn<DeviceCall>().mockResolvedValueOnce({ requestID: "one", result: page("YQ==", 0, false) })
      .mockRejectedValueOnce(new Error("page failure"));
    await expect(readCompleteFile(failed, "/fixture", "utf8")).rejects.toThrow("page failure");
    expect(failed).toHaveBeenCalledTimes(2);
  });
});
