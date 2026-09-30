import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TerminalPanel } from "../../src/ui/panels/TerminalPanel";
import { FilesPanel } from "../../src/ui/panels/FilesPanel";
import type { DeviceCall } from "../../src/ui/panels/types";
import { TerminalOutput } from "../../src/ui/terminal-output";

afterEach(() => vi.useRealTimers());

describe("panel error and request boundaries", () => {
  it("restores a parsed screen when switching away before its write callback completes", async () => {
    const original = TerminalOutput.prototype.append;
    let held = false;
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    const spy = vi.spyOn(TerminalOutput.prototype, "append").mockImplementation(async function (this: TerminalOutput, ...args) {
      const result = await original.apply(this, args);
      if (!held) { held = true; await gate; }
      return result;
    });
    const call: DeviceCall = async (method, args) => {
      if (method === "terminal_sessions") return { requestID: "list", result: ["first", "second"].map(ID => ({ Session: { ID, Dir: "/" }, Running: true })) };
      const { sessionId, cursor } = args as { sessionId: string; cursor: number };
      const text = sessionId === "first" ? "FIRST" : "SECOND";
      return { requestID: "read", result: { Data: btoa(text.slice(cursor)), StartCursor: cursor, NextCursor: text.length, Running: true } };
    };
    try {
      render(<TerminalPanel call={call} />);
      fireEvent.click(await screen.findByRole("button", { name: /first/u }));
      await waitFor(() => expect(held).toBe(true));
      fireEvent.click(screen.getByRole("button", { name: /second/u }));
      await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("SECOND"));
      fireEvent.click(screen.getByRole("button", { name: /first/u }));
      await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("FIRST"));
    } finally { release(); spy.mockRestore(); }
  });
  it.each([{ StartCursor: "invalid" }, { StartCursor: -1 }, { Truncated: "false" }])("rejects malformed present output metadata %j", async (metadata) => {
    const call: DeviceCall = async method => method === "terminal_sessions"
      ? { requestID: "list", result: [{ Session: { ID: "bad-output", Dir: "/" }, Running: true }] }
      : { requestID: "read", result: { Data: btoa("TEXT"), NextCursor: 4, Running: true, ...metadata } };
    render(<TerminalPanel call={call} />);
    fireEvent.click(await screen.findByRole("button", { name: /bad-output/u }));
    expect(await screen.findByText("Terminal output unavailable")).toBeVisible();
    expect(screen.getByRole("log")).not.toHaveTextContent("TEXT");
  });

  it("keeps each session's VT and UTF-8 stream state when switching sessions", async () => {
    let firstReads = 0;
    const call: DeviceCall = async (method, args) => {
      if (method === "terminal_sessions") return { requestID: "list", result: ["first", "second"].map(ID => ({ Session: { ID, Dir: "/" }, Running: true })) };
      const { sessionId } = args as { sessionId: string };
      if (sessionId === "second") return { requestID: "second", result: { Data: btoa("SECOND"), StartCursor: 0, NextCursor: 6, Running: true } };
      firstReads += 1;
      return { requestID: "first", result: firstReads === 1
        ? { Data: btoa("\xe4\xb8"), StartCursor: 0, NextCursor: 2, Running: true }
        : { Data: btoa("\xad\x1b[31m!\x1b[0m"), StartCursor: 2, NextCursor: 13, Running: true } };
    };
    render(<TerminalPanel call={call} />);
    fireEvent.click(await screen.findByRole("button", { name: /first/u }));
    await act(() => new Promise(resolve => setTimeout(resolve, 20)));
    fireEvent.click(screen.getByRole("button", { name: /second/u }));
    await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("SECOND"));
    fireEvent.click(screen.getByRole("button", { name: /first/u }));
    await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("中!"));
    expect(screen.getByRole("log")).not.toHaveTextContent("SECOND");
    expect(screen.getByRole("log").textContent).not.toContain("\x1b");
  });

  it("cancels an old preview when the operation path changes without discarding edited text", async () => {
    let finishRead!: (value: Awaited<ReturnType<DeviceCall>>) => void;
    let readSignal: AbortSignal | undefined;
    const call: DeviceCall = async (_method, args, signal) => {
      if ((args as { action: string }).action === "read_directory") return { requestID: "list", result: [] };
      readSignal = signal;
      return new Promise((resolve) => { finishRead = resolve; });
    };
    render(<FilesPanel call={call} />);
    await screen.findByText("Directory loaded");
    fireEvent.change(screen.getByLabelText("Operation path"), { target: { value: "/first" } });
    fireEvent.change(screen.getByLabelText("File contents"), { target: { value: "unsaved edits" } });
    fireEvent.click(screen.getByRole("button", { name: "Read UTF-8" }));
    fireEvent.change(screen.getByLabelText("Operation path"), { target: { value: "/save-as" } });
    expect(readSignal?.aborted).toBe(true);
    expect(screen.getByLabelText("File contents")).toHaveValue("unsaved edits");
    await act(async () => finishRead({ requestID: "first", result: { content: "old preview" } }));
    expect(screen.getByLabelText("Operation path")).toHaveValue("/save-as");
    expect(screen.getByLabelText("File contents")).toHaveValue("unsaved edits");
  });

  it("does not replace a newer file selection with an older read response", async () => {
    let finishFirst!: (value: Awaited<ReturnType<DeviceCall>>) => void;
    const call: DeviceCall = async (_method, args) => {
      const input = args as { action: string; path: string };
      if (input.action === "read_directory") return { requestID: "list", result: ["first", "second"].map((name) => ({ Name: name, Path: `/${name}`, IsDir: false })) };
      if (input.path === "/first") return new Promise((resolve) => { finishFirst = resolve; });
      return { requestID: "second", result: { content: "newer file" } };
    };
    render(<FilesPanel call={call} />);
    fireEvent.click(await screen.findByText("first", { selector: "strong" }));
    fireEvent.click(screen.getByText("second", { selector: "strong" }));
    await waitFor(() => expect(screen.getByLabelText("File contents")).toHaveValue("newer file"));
    await act(async () => finishFirst({ requestID: "first", result: { content: "older file" } }));
    expect(screen.getByLabelText("Operation path")).toHaveValue("/second");
    expect(screen.getByLabelText("File contents")).toHaveValue("newer file");
  });

  it("does not cancel slow output on the next polling tick", async () => {
    let outputSignal: AbortSignal | undefined;
    const call = vi.fn<DeviceCall>(async (method, _args, signal) => {
      if (method === "terminal_sessions") return { requestID: "list", result: [{ Session: { ID: "slow", Dir: "/" }, Running: true }] };
      outputSignal = signal;
      return new Promise(() => {});
    });
    render(<TerminalPanel call={call} />);
    const session = await screen.findByRole("button", { name: /slow/u });
    vi.useFakeTimers();
    fireEvent.click(session);
    expect(outputSignal).toBeDefined();
    const first = outputSignal;
    await act(() => vi.advanceTimersByTimeAsync(2_100));
    expect(first?.aborted).toBe(false);
    expect(call.mock.calls.filter(([method]) => method === "terminal_output")).toHaveLength(1);
  });

  it("drains completed terminal pages and stops polling after the final page", async () => {
    let reads = 0;
    const call = vi.fn<DeviceCall>(async (method, args) => {
      if (method === "terminal_sessions") return { requestID: "list", result: [{ Session: { ID: "finished", Dir: "/" }, Running: false }] };
      reads += 1;
      const cursor = (args as { cursor: number }).cursor;
      return { requestID: "read", result: { Data: btoa(reads === 1 ? "FIRST " : "FINAL"), StartCursor: cursor, NextCursor: cursor + (reads === 1 ? 6 : 5), Running: false, hasMore: reads === 1 } };
    });
    render(<TerminalPanel call={call} />);
    const session = await screen.findByRole("button", { name: /finished/u });
    vi.useFakeTimers();
    fireEvent.click(session);
    await act(() => vi.advanceTimersByTimeAsync(1_100));
    expect(screen.getByRole("log")).toHaveTextContent("FIRST FINAL");
    expect(reads).toBe(2);
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    fireEvent(document, new Event("visibilitychange"));
    expect(reads).toBe(2);
  });

  it("rejects a malformed session list instead of reporting zero sessions", async () => {
    const call: DeviceCall = async () => ({ requestID: "list", result: { error: "bad" } });
    render(<TerminalPanel call={call} />);
    expect(await screen.findByText("Terminal sessions unavailable")).toBeVisible();
  });

  it("ignores a session list from the previous privilege", async () => {
    let resolveOwner!: (value: Awaited<ReturnType<DeviceCall>>) => void;
    const call: DeviceCall = async (_method, args) => (args as { privilege: string }).privilege === "owner"
      ? new Promise((resolve) => { resolveOwner = resolve; })
      : { requestID: "admin", result: [{ Session: { ID: "admin-session", Dir: "/" }, Running: true }] };
    render(<TerminalPanel call={call} />);
    fireEvent.click(screen.getByRole("button", { name: "Admin" }));
    await screen.findByRole("button", { name: /admin-session/u });
    await act(async () => resolveOwner({ requestID: "owner", result: [{ Session: { ID: "owner-session", Dir: "/" }, Running: true }] }));
    expect(screen.queryByRole("button", { name: /owner-session/u })).not.toBeInTheDocument();
  });

  it("does not attach a late owner creation or refresh its list after switching to admin", async () => {
    let finishCreate!: (value: Awaited<ReturnType<DeviceCall>>) => void;
    const call = vi.fn<DeviceCall>(async (method, args) => {
      const input = args as { action: string; privilege: string };
      if (method === "terminal" && input.action === "create") return new Promise(resolve => { finishCreate = resolve; });
      return { requestID: "list", result: input.privilege === "admin" ? [{ Session: { ID: "admin-current", Dir: "/" }, Running: true }] : [] };
    });
    render(<TerminalPanel call={call} />);
    await screen.findByText("0 persistent owner sessions");
    fireEvent.click(screen.getByRole("button", { name: "New session" }));
    fireEvent.click(screen.getByRole("button", { name: "Admin" }));
    await screen.findByRole("button", { name: /admin-current/u });
    await act(async () => finishCreate({ requestID: "create", result: { ID: "owner-late" } }));
    expect(screen.getByRole("button", { name: /admin-current/u })).toBeInTheDocument();
    expect(screen.getByRole("log")).toHaveTextContent("Select or create a persistent session.");
    expect(call.mock.calls.filter(([method]) => method === "terminal_output")).toHaveLength(0);
    expect(call.mock.calls.filter(([method, args]) => method === "terminal_sessions" && (args as { privilege: string }).privilege === "owner")).toHaveLength(1);
  });

  it("dispatches one upload when the upload button is activated twice during a pending read", async () => {
    const finishReads: Array<(value: ArrayBuffer) => void> = [];
    const bytes = new Uint8Array(4 * 1024 * 1024 + 3);
    const file = new File([bytes], "same.txt", { type: "application/octet-stream" });
    Object.defineProperty(file, "arrayBuffer", { value: vi.fn(() => new Promise<ArrayBuffer>(resolve => { finishReads.push(resolve); })) });
    let uploadedBytes = 0;
    const call = vi.fn<DeviceCall>(async (method, args) => {
      if (method === "filesystem_read") return { requestID: "list", result: [] };
      const input = args as { action: string; content: string };
      if (input.action === "write_file") uploadedBytes = 0;
      uploadedBytes += atob(input.content).length;
      return { requestID: "write", result: { size: uploadedBytes } };
    });
    render(<FilesPanel call={call} />);
    await screen.findByText("Directory loaded");
    fireEvent.change(screen.getByLabelText("Choose file to upload"), { target: { files: [file] } });
    const upload = screen.getByRole("button", { name: "Upload" });
    fireEvent.click(upload);fireEvent.click(upload);
    await act(async () => finishReads.forEach(finish => finish(bytes.buffer)));
    expect(uploadedBytes).toBe(bytes.length);
    expect(file.arrayBuffer).toHaveBeenCalledTimes(1);
    expect(call.mock.calls.filter(([method]) => method === "filesystem_write")).toHaveLength(2);
  });

  it("rejects malformed directory entries rather than showing a successful empty listing", async () => {
    const call: DeviceCall = async () => ({ requestID: "list", result: [{ Name: "broken" }] });
    render(<FilesPanel call={call} />);
    expect(await screen.findByText("Directory unavailable")).toBeVisible();
  });

  it("does not offer an empty overwrite after a failed file preview", async () => {
    const call = vi.fn<DeviceCall>(async (_method, args) => {
      if ((args as { action: string }).action === "read_directory") return { requestID: "list", result: [{ Name: "binary", Path: "/binary", IsDir: false }] };
      throw new Error("cannot decode");
    });
    render(<FilesPanel call={call} />);
    fireEvent.click(await screen.findByText("binary", { selector: "strong" }));
    await waitFor(() => expect(screen.getByText(/Text preview unavailable/u)).toBeVisible());
    expect(screen.getByRole("button", { name: "Save UTF-8" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Download binary" })).toBeEnabled();
  });
});
