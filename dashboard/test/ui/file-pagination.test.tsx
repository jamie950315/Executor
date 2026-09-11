import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { FilesPanel } from "../../src/ui/panels/FilesPanel";
import type { DeviceCall } from "../../src/ui/panels/types";

function page(bytes: Uint8Array, offset: number, limit: number, encoding: string) {
  const data = bytes.slice(offset, offset + limit);
  const next = offset + data.length;
  return {
    content: encoding === "base64" ? btoa(Array.from(data, byte => String.fromCharCode(byte)).join("")) : new TextDecoder("utf-8", { fatal: true }).decode(data),
    encoding, size: data.length, offsetBytes: offset, returnedBytes: data.length,
    nextOffsetBytes: next, truncated: next < bytes.length, eof: next >= bytes.length,
  };
}

describe("complete file previews", () => {
  it("reads all pages and preserves Chinese characters crossing byte boundaries before saving", async () => {
    const text = "a".repeat(65535) + "中文-tail";
    const bytes = new TextEncoder().encode(text);
    let saved: unknown;
    const call = vi.fn<DeviceCall>(async (_method, args) => {
      const input = args as { action: string; offset?: number; limit?: number; encoding?: string; content?: unknown };
      if (input.action === "read_directory") return { requestID: "list", result: [{ Name: "large", Path: "/large", IsDir: false }] };
      if (input.action === "write_file") { saved = input.content; return { requestID: "save", result: { ok: true, size: bytes.length } }; }
      return { requestID: "read", result: page(bytes, input.offset ?? 0, Math.min(input.limit ?? 65536, 65536), input.encoding ?? "utf8") };
    });
    render(<FilesPanel call={call} />);
    fireEvent.click(await screen.findByText("large", { selector: "strong" }));
    await waitFor(() => expect((screen.getByLabelText("File contents") as HTMLTextAreaElement).value.length).toBe(text.length));
    expect((screen.getByLabelText("File contents") as HTMLTextAreaElement).value.endsWith("中文-tail")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Save UTF-8" }));
    await waitFor(() => expect(typeof saved).toBe("string"));
    expect(saved === text).toBe(true);
    expect(call.mock.calls.filter(([, args]) => (args as { action: string }).action === "read_file")).toHaveLength(2);
  });

  it("keeps save disabled when a later page fails", async () => {
    const bytes = new TextEncoder().encode("a".repeat(65537));
    const call: DeviceCall = async (_method, args) => {
      const input = args as { action: string; offset?: number; encoding?: string };
      if (input.action === "read_directory") return { requestID: "list", result: [{ Name: "large", Path: "/large", IsDir: false }] };
      if ((input.offset ?? 0) > 0) throw new Error("second page failed");
      return { requestID: "read", result: page(bytes, 0, 65536, input.encoding ?? "utf8") };
    };
    render(<FilesPanel call={call} />);
    fireEvent.click(await screen.findByText("large", { selector: "strong" }));
    expect(await screen.findByText(/Text preview unavailable/u)).toBeVisible();
    expect(screen.getByRole("button", { name: "Save UTF-8" })).toBeDisabled();
  });
});
