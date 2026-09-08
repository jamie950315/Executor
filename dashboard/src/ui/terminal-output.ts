import { Terminal } from "@xterm/headless";

const maximumCells = 500_000;

/** Read-only VT screen: no onData listener, terminal replies, or host input. */
export class TerminalOutput {
  private terminal: Terminal;
  private pendingWrite: Promise<void> = Promise.resolve();
  private disposed = false;
  cursor = 0;

  constructor(columns = 120, rows = 36) {
    const scrollback = boundedScrollback(columns, rows);
    this.terminal = new Terminal({ cols: columns, rows, scrollback, allowProposedApi: true });
  }

  async append(data: Uint8Array, startCursor: number, nextCursor: number, truncated: boolean): Promise<boolean> {
    if (!Number.isSafeInteger(startCursor) || startCursor < 0 || !Number.isSafeInteger(nextCursor) || nextCursor - startCursor !== data.length) {
      throw new Error("Invalid terminal output cursor");
    }
    const reset = truncated || startCursor !== this.cursor;
    // Reserve bytes before yielding, so rapid reattachment cannot fetch them twice.
    this.cursor = nextCursor;
    const write = this.pendingWrite.then(async () => {
      if (this.disposed) throw new Error("Terminal display is closed");
      if (reset) {
        const { cols, rows } = this.terminal;
        this.terminal.dispose();
        // reset() retains unfinished VT state; a gap requires a fresh parser.
        this.terminal = new Terminal({ cols, rows, scrollback: boundedScrollback(cols, rows), allowProposedApi: true });
      }
      if (data.length > 0) await new Promise<void>((resolve) => this.terminal.write(data, resolve));
    });
    this.pendingWrite = write;
    await write;
    return reset;
  }

  text(): string {
    const buffer = this.terminal.buffer.active;
    const lines: string[] = [];
    for (let index = 0; index < buffer.length; index += 1) {
      lines.push(buffer.getLine(index)?.translateToString(true) ?? "");
    }
    return lines.join("\n").replace(/\n+$/u, "");
  }

  resize(columns: number, rows: number): void {
    const scrollback = boundedScrollback(columns, rows);
    this.terminal.options.scrollback = Math.min(this.terminal.options.scrollback ?? 0, scrollback);
    this.terminal.resize(columns, rows);
    this.terminal.options.scrollback = scrollback;
  }

  dispose(): void { this.disposed = true; this.terminal.dispose(); }
}

function boundedScrollback(columns: number, rows: number): number {
  validateTerminalDimensions(columns, rows);
  return Math.min(2000, Math.max(0, Math.floor(maximumCells / columns) - rows));
}

export function validateTerminalDimensions(columns: number, rows: number): void {
  if (!Number.isSafeInteger(columns) || !Number.isSafeInteger(rows) || columns < 1 || rows < 1 || columns > 32767 || rows > 32767 || columns * rows > maximumCells) {
    throw new Error("Terminal display dimensions exceed the bounded viewport");
  }
}
