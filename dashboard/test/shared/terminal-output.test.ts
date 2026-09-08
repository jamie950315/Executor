import { describe, expect, it } from "vitest";
import { TerminalOutput } from "../../src/ui/terminal-output";

const bytes = (value: string) => new TextEncoder().encode(value);

describe("terminal output rendering", () => {
  it("reserves the enqueued byte cursor before a quick session reattachment can read it", async () => {
    const output = new TerminalOutput();
    try {
      const first = output.append(bytes("FIRST"), 0, 5, false);
      expect(output.cursor).toBe(5);
      const second = output.append(bytes("SECOND"), output.cursor, 11, false);
      await Promise.all([first, second]);
      expect(output.text()).toBe("FIRSTSECOND");
      expect(output.cursor).toBe(11);
    } finally { output.dispose(); }
  });

  it("decodes multibyte UTF-8 split across response boundaries", async () => {
    const output = new TerminalOutput();
    try {
      const value = bytes("中文🙂");
      await output.append(value.slice(0, 2), 0, 2, false);
      expect(output.text()).toBe("");
      await output.append(value.slice(2, 7), 2, 7, false);
      await output.append(value.slice(7), 7, value.length, false);
      expect(output.text()).toBe("中文🙂");
    } finally { output.dispose(); }
  });

  it("parses split ANSI sequences without exposing control text", async () => {
    const output = new TerminalOutput();
    try {
      await output.append(bytes("\x1b[3"), 0, 3, false);
      const remainder = bytes("1mRED\x1b[0m\r\nplain");
      await output.append(remainder, 3, 3 + remainder.length, false);
      expect(output.text()).toBe("RED\nplain");
    } finally { output.dispose(); }
  });

  it("applies carriage return and erase-line instead of appending stale progress", async () => {
    const output = new TerminalOutput();
    try {
      const value = bytes("progress 10%\rprogress 100%\r\x1b[2Kdone");
      await output.append(value, 0, value.length, false);
      expect(output.text()).toBe("done");
    } finally { output.dispose(); }
  });

  it("renders PowerShell ConPTY cursor redraw and ignores private input mode", async () => {
    const output = new TerminalOutput();
    try {
      const value = bytes("\x1b[?9001h\x1b[?1004h\x1b[2J\x1b[HPS C:\\> wrte\x1b[1;11Hite\x1b[K\r\nOK\x1b[3;1HPS C:\\> ");
      await output.append(value, 0, value.length, false);
      expect(output.text()).toBe("PS C:\\> write\nOK\nPS C:\\> ");
    } finally { output.dispose(); }
  });

  it("resets incomplete UTF-8 and escape state when the host drops output", async () => {
    const output = new TerminalOutput();
    try {
      await output.append(new Uint8Array([0xe4, 0xb8]), 0, 2, false);
      expect(await output.append(bytes("FRESH"), 20, 25, true)).toBe(true);
      expect(output.text()).toBe("FRESH");
      await output.append(bytes("\x1b["), 25, 27, false);
      expect(await output.append(bytes("SAFE"), 40, 44, false)).toBe(true);
      expect(output.text()).toBe("SAFE");
    } finally { output.dispose(); }
  });

  it("rejects inconsistent cursors rather than corrupting the display", async () => {
    const output = new TerminalOutput();
    try {
      await expect(output.append(bytes("hello"), 0, 4, false)).rejects.toThrow(/cursor/iu);
      expect(output.cursor).toBe(0);
    } finally { output.dispose(); }
  });

  it("bounds retained scrollback and rejects unbounded viewport allocations", async () => {
    expect(() => new TerminalOutput(32767, 32767)).toThrow(/bounded viewport/iu);
    const output = new TerminalOutput(80, 24);
    try {
      const value = bytes(Array.from({ length: 2500 }, (_, index) => `line-${index}\r\n`).join(""));
      await output.append(value, 0, value.length, false);
      expect(output.text()).not.toContain("line-0\n");
      expect(output.text()).toContain("line-2499");
      expect(output.text().split("\n").length).toBeLessThanOrEqual(2024);
    } finally { output.dispose(); }
  });

  it("resizes the VT screen without resetting the stream cursor", async () => {
    const output = new TerminalOutput(80, 24);
    try {
      await output.append(bytes("prompt"), 0, 6, false);
      output.resize(100, 30);
      expect(output.cursor).toBe(6);
      expect(output.text()).toContain("prompt");
      expect(() => output.resize(32767, 32767)).toThrow(/bounded viewport/iu);
      expect(output.text()).toContain("prompt");
    } finally { output.dispose(); }
  });
});
