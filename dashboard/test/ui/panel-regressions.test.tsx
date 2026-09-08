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
