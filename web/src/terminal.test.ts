import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { expect, test } from "vitest";

test("terminal parses text and binary output and disposes input/resize subscriptions", async () => {
  const terminal = new Terminal({ convertEol: true, cols: 80, rows: 24 });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  const input: string[] = [];
  const sizes: { cols: number; rows: number }[] = [];
  const data = terminal.onData(value => input.push(value));
  const resize = terminal.onResize(({ cols, rows }) => sizes.push({ cols, rows }));
  try {
    await new Promise<void>(resolve => terminal.write("\u001b[31mready\u001b[0m\n", resolve));
    await new Promise<void>(resolve => terminal.write(new TextEncoder().encode("ação"), resolve));
    expect(terminal.buffer.active.getLine(0)?.translateToString(true)).toBe("ready");
    expect(terminal.buffer.active.getLine(1)?.translateToString(true)).toBe("ação");
    terminal.input("hello\r", true);
    terminal.resize(100, 30);
    expect(input).toEqual(["hello\r"]);
    expect(sizes).toEqual([{ cols: 100, rows: 30 }]);
    data.dispose();
    resize.dispose();
    terminal.input("ignored", true);
    terminal.resize(90, 25);
    expect(input).toEqual(["hello\r"]);
    expect(sizes).toHaveLength(1);
  } finally {
    data.dispose();
    resize.dispose();
    terminal.dispose();
  }
});
