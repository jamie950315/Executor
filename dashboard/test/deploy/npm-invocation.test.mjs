import { describe, expect, test, vi } from "vitest";
import * as cli from "../../scripts/lib/deploy-cli.mjs";

describe("shell-free Windows npm invocation", () => {
  const execPath = "C:\\Program Files\\nodejs\\node.exe";
  const npmCLI = "C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npm-cli.js";
  const exists = (...paths) => vi.fn(async (path) => paths.includes(path));

  test("resolves the npm JavaScript entrypoint beside the first PATH shim", async () => {
    const result = await cli.resolveNpmInvocation({ platform: "win32", execPath, environment: { Path: "C:\\missing;C:\\Program Files\\nodejs" }, isFile: exists("C:\\Program Files\\nodejs\\npm.cmd", npmCLI) });
    expect(result).toEqual({ command: execPath, args: [npmCLI] });
  });

  test("respects a user-prefix npm installation instead of assuming Node's installation directory", async () => {
    const prefix = "C:\\Users\\Owner & Admin\\AppData\\Roaming\\npm";
    const customCLI = `${prefix}\\node_modules\\npm\\bin\\npm-cli.js`;
    const result = await cli.resolveNpmInvocation({ platform: "win32", execPath, environment: { PATH: `${prefix};C:\\Program Files\\nodejs` }, isFile: exists(`${prefix}\\npm.cmd`, customCLI, "C:\\Program Files\\nodejs\\npm.cmd", npmCLI) });
    expect(result).toEqual({ command: execPath, args: [customCLI] });
  });

  test("uses npm's explicit JavaScript entrypoint for version-managed installations", async () => {
    const customCLI = "D:\\tools\\npm\\bin\\npm-cli.js";
    expect(await cli.resolveNpmInvocation({ platform: "win32", execPath, environment: { npm_execpath: customCLI }, isFile: exists(customCLI) })).toEqual({ command: execPath, args: [customCLI] });
  });

  test("reports a broken selected npm installation instead of silently choosing another", async () => {
    await expect(cli.resolveNpmInvocation({ platform: "win32", execPath, environment: { Path: "C:\\broken;C:\\Program Files\\nodejs" }, isFile: exists("C:\\broken\\npm.cmd", "C:\\Program Files\\nodejs\\npm.cmd", npmCLI) })).rejects.toThrow(/npm JavaScript entrypoint/iu);
  });

  test("leaves Unix executable invocation unchanged", async () => {
    expect(await cli.resolveNpmInvocation({ platform: "darwin", environment: {} })).toEqual({ command: "npm", args: [] });
  });
});
