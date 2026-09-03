import { createHash } from "node:crypto";
import { chmod, lstat, mkdir, mkdtemp, open as fsOpen, readFile, readdir, rename, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { describe, expect, test, vi } from "vitest";

import * as deploymentCore from "../../scripts/lib/deploy-core.mjs";

import {
  CloudflareClient,
  assertProtectedFile,
  assertMigrationCompatible,
  assertSupportedNodeVersion,
  assertWranglerV4,
  ensureAccessResources,
  ensureAccessTeamDomain,
  ensureD1Database,
  ensureEnrollmentToken,
  ensureWorkerOwnership,
  loadDeploymentState,
  renderWranglerConfig,
  removeProtectedFileIfOwned,
  saveDeploymentState,
  verifyAccessTeamDomain,
  writeTemporaryWranglerConfig,
} from "../../scripts/lib/deploy-core.mjs";
import {
  normalizeDeploymentOptions,
  parseDeploymentArguments,
} from "../../scripts/lib/deploy-cli.mjs";

const accountID = "0123456789abcdef0123456789abcdef";
const hostname = "dashboard.example.test";
const allowedEmail = "owner@example.test";
const isWindows = process.platform === "win32";
const unixTest = test.skipIf(isWindows);
const windowsTest = test.skipIf(!isWindows);

describe("deployment prerequisites", () => {
  test("accepts explicit non-secret deployment inputs and rejects token values", () => {
    expect(parseDeploymentArguments([
      "deploy",
      "--hostname", hostname,
      "--account-id", accountID,
      "--api-token-file", "/secure/cloudflare.token",
      "--allowed-email", allowedEmail,
      "--access-team-domain", "executor.cloudflareaccess.com",
    ])).toMatchObject({
      command: "deploy",
      hostname,
      accountID,
      apiTokenFile: "/secure/cloudflare.token",
      allowedEmail,
      accessTeamDomain: "executor.cloudflareaccess.com",
    });
    expect(() => parseDeploymentArguments(["deploy", "--api-token", "forbidden-token-value"])).toThrow(/unknown option/iu);
  });

  test("keeps generated state and enrollment material outside the repository", () => {
    const root = "/checkout/Executor/dashboard";
    const normalized = normalizeDeploymentOptions({
      command: "deploy",
      hostname,
      accountID,
      apiTokenFile: "/secure/cloudflare.token",
      allowedEmail,
    }, {
      dashboardRoot: root,
      platform: "linux",
      homeDirectory: "/home/owner",
      environment: {},
    });
    expect(normalized.stateFile).toBe(resolve("/home/owner/.local/state/executor/dashboard/deployment.json"));
    expect(normalized.enrollmentTokenFile).toBe(resolve("/home/owner/.local/state/executor/dashboard/enrollment.token"));
    expect(() => normalizeDeploymentOptions({
      command: "deploy",
      hostname,
      accountID,
      apiTokenFile: "/secure/cloudflare.token",
      allowedEmail,
      stateFile: "/checkout/Executor/dashboard/deployment.json",
    }, {
      dashboardRoot: root,
      platform: "linux",
      homeDirectory: "/home/owner",
      environment: {},
    })).toThrow(/outside the repository/iu);
  });

  windowsTest("treats a different Windows volume as outside the repository", () => {
    expect(() => normalizeDeploymentOptions({
      command: "deploy",
      hostname,
      accountID,
      apiTokenFile: "C:\\secure\\cloudflare.token",
      allowedEmail,
    }, {
      dashboardRoot: "D:\\checkout\\Executor\\dashboard",
      platform: "win32",
      homeDirectory: "C:\\Users\\owner",
      environment: { LOCALAPPDATA: "C:\\Users\\owner\\AppData\\Local" },
    })).not.toThrow();
  });

  test("requires an explicit Worker version for rollback", () => {
    const runtime = { dashboardRoot: "/checkout/Executor/dashboard", platform: "linux", homeDirectory: "/home/owner", environment: {} };
    const base = {
      command: "rollback", hostname, accountID, apiTokenFile: "/secure/cloudflare.token", allowedEmail,
    };
    expect(() => normalizeDeploymentOptions(base, runtime)).toThrow(/version.*required|explicit/iu);
    expect(() => normalizeDeploymentOptions({ ...base, versionID: "version-owned-123" }, runtime)).not.toThrow();
  });

  test("accepts an explicit Access team domain and rejects unsafe values", () => {
    const runtime = { dashboardRoot: "/checkout/Executor/dashboard", platform: "linux", homeDirectory: "/home/owner", environment: {} };
    const base = {
      command: "deploy", hostname, accountID, apiTokenFile: "/secure/cloudflare.token", allowedEmail,
    };
    expect(normalizeDeploymentOptions({ ...base, accessTeamDomain: "Executor.CloudflareAccess.com" }, runtime))
      .toMatchObject({ accessTeamDomain: "executor.cloudflareaccess.com" });
    expect(() => normalizeDeploymentOptions({ ...base, accessTeamDomain: "https://executor.cloudflareaccess.com" }, runtime))
      .toThrow(/Access team domain/iu);
  });

  test("requires state and enrollment material to share one dedicated Executor directory", () => {
    expect(() => normalizeDeploymentOptions({
      command: "deploy", hostname, accountID, apiTokenFile: "/secure/cloudflare.token", allowedEmail,
      stateFile: "/secure/executor-state/deployment.json",
      enrollmentTokenFile: "/secure/executor-token/enrollment.token",
    }, {
      dashboardRoot: "/checkout/Executor/dashboard", platform: "linux", homeDirectory: "/home/owner", environment: {},
    })).toThrow(/dedicated Executor directory/iu);
  });

  test.each(["v20.19.0", "v20.20.1", "v22.13.0", "v24.0.0", "v26.2.0"])(
    "accepts supported Node %s",
    (version) => expect(() => assertSupportedNodeVersion(version)).not.toThrow(),
  );

  test.each(["v18.20.0", "v20.18.9", "v21.7.0", "v22.12.9", "v23.11.0", "not-a-version"])(
    "rejects unsupported Node %s",
    (version) => expect(() => assertSupportedNodeVersion(version)).toThrow(/supported Node/iu),
  );

  test("requires the pinned Wrangler major version", () => {
    expect(assertWranglerV4("4.125.0")).toBe("4.125.0");
    expect(() => assertWranglerV4("3.114.0")).toThrow(/Wrangler v4/iu);
    expect(() => assertWranglerV4("unexpected output")).toThrow(/Wrangler v4/iu);
  });

  unixTest("accepts only a regular Unix mode-0600 token file", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-token-"));
    const tokenPath = join(directory, "cloudflare.token");
    await writeFile(tokenPath, "test-only-cloudflare-token\n", { mode: 0o644 });
    await expect(assertProtectedFile(tokenPath, { platform: "linux" })).rejects.toThrow(/0600/iu);

    await chmod(tokenPath, 0o600);
    await expect(assertProtectedFile(tokenPath, { platform: "linux" })).resolves.toBeUndefined();

    const linkPath = join(directory, "cloudflare-link.token");
    await symlink(tokenPath, linkPath);
    await expect(assertProtectedFile(linkPath, { platform: "linux" })).rejects.toThrow(/unsafe path ancestor|regular protected file/iu);
  });

  test("rejects a protected leaf reached through a symlinked ancestor", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-token-ancestor-"));
    const realDirectory = join(directory, "real");
    const linkedDirectory = join(directory, "linked");
    await mkdir(realDirectory, { mode: 0o700 });
    const tokenPath = join(realDirectory, "cloudflare.token");
    await writeFile(tokenPath, "test-only-cloudflare-token\n", { mode: 0o600 });
    await symlink(realDirectory, linkedDirectory, "dir");

    await expect(assertProtectedFile(join(linkedDirectory, "cloudflare.token"), { platform: "linux" }))
      .rejects.toThrow(/unsafe path ancestor/iu);
  });

  test("delegates Windows ACL verification and fails closed", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-windows-token-"));
    const tokenPath = join(directory, "cloudflare.token");
    await writeFile(tokenPath, "test-only-cloudflare-token\n", { mode: 0o600 });
    const verifyWindowsACL = vi.fn(async () => false);
    await expect(assertProtectedFile(tokenPath, { platform: "win32", verifyWindowsACL })).rejects.toThrow(/Windows ACL/iu);
    expect(verifyWindowsACL).toHaveBeenCalledWith(tokenPath);
  });

  unixTest("reads the protected API token exactly once from its held file identity before path replacement", async () => {
    expect(deploymentCore).toHaveProperty("readProtectedCredential");
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-held-token-"));
    const tokenPath = join(directory, "cloudflare.token");
    const movedPath = join(directory, "cloudflare.original.token");
    await writeFile(tokenPath, "test-only-api-token-A\n", { mode: 0o600 });
    let reads = 0;
    const openFile = async (...argumentsList) => {
      const handle = await fsOpen(...argumentsList);
      return {
        stat: (...arguments_) => handle.stat(...arguments_),
        readFile: (...arguments_) => {
          reads += 1;
          return handle.readFile(...arguments_);
        },
        close: () => handle.close(),
      };
    };

    const token = await deploymentCore.readProtectedCredential(tokenPath, {
      platform: "linux",
      openFile,
      afterIdentityVerified: async () => {
        await rename(tokenPath, movedPath);
        await writeFile(tokenPath, "test-only-api-token-B\n", { mode: 0o600 });
      },
    });

    expect(token.toString("utf8")).toBe("test-only-api-token-A");
    expect(reads).toBe(1);
    expect(await readFile(tokenPath, "utf8")).toBe("test-only-api-token-B\n");
    token.fill(0);
  });
});

describe("temporary Wrangler config", () => {
  test("renders account, D1, Access, and a Workers Custom Domain without retaining placeholders", () => {
    const template = JSON.stringify({
      name: "executor-dashboard",
      account_id: "__EXECUTOR_ACCOUNT_ID__",
      d1_databases: [{ binding: "DB", database_name: "executor-dashboard", database_id: "__EXECUTOR_D1_DATABASE_ID__" }],
      routes: [{ pattern: "__EXECUTOR_DASHBOARD_HOSTNAME__", custom_domain: true }],
      vars: { ACCESS_AUD: "__EXECUTOR_ACCESS_AUD__", ACCESS_TEAM_DOMAIN: "__EXECUTOR_ACCESS_TEAM_DOMAIN__" },
    });
    const rendered = renderWranglerConfig(template, {
      accountID,
      databaseID: "11111111-2222-3333-4444-555555555555",
      hostname,
      accessAUD: "access-audience",
      accessTeamDomain: "example.cloudflareaccess.com",
    });
    const parsed = JSON.parse(rendered);
    expect(parsed.account_id).toBe(accountID);
    expect(parsed.d1_databases[0].database_id).toBe("11111111-2222-3333-4444-555555555555");
    expect(parsed.routes).toEqual([{ pattern: hostname, custom_domain: true }]);
    expect(parsed.vars).toEqual({ ACCESS_AUD: "access-audience", ACCESS_TEAM_DOMAIN: "example.cloudflareaccess.com" });
    expect(rendered).not.toContain("__EXECUTOR_");
  });

  test("refuses a template that is not an Executor deployment template", () => {
    expect(() => renderWranglerConfig("{}", {
      accountID,
      databaseID: "11111111-2222-3333-4444-555555555555",
      hostname,
      accessAUD: "access-audience",
      accessTeamDomain: "example.cloudflareaccess.com",
    })).toThrow(/deployment template/iu);
  });

  test("places temporary config beside Dashboard sources so relative Wrangler paths resolve", async () => {
    const dashboardRoot = await mkdtemp(join(tmpdir(), "executor-dashboard-root-"));
    const temporary = await writeTemporaryWranglerConfig(dashboardRoot, "{}\n");
    expect(temporary.configPath.startsWith(resolve(dashboardRoot, ".wrangler.executor.generated-"))).toBe(true);
    if (!isWindows) expect((await lstat(temporary.configPath)).mode & 0o777).toBe(0o600);
    await temporary.cleanup();
    await expect(lstat(temporary.configPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  test("pipes Worker enrollment hash through stdin and leaves no repository secret file on deployment failure", async () => {
    expect(deploymentCore).toHaveProperty("deployWorkerWithSecret");
    const dashboardRoot = await mkdtemp(join(tmpdir(), "executor-dashboard-worker-secret-"));
    const configPath = join(dashboardRoot, "wrangler.generated.jsonc");
    await writeFile(configPath, "{}\n", { mode: 0o600 });
    const hash = "a".repeat(64);
    const calls = [];
    const runCommand = vi.fn(async (_command, argumentsList, options) => {
      calls.push({ argumentsList, input: options.input });
      if (argumentsList.includes("deploy")) throw new Error("simulated terminated deployment");
      return { stdout: "", stderr: "" };
    });

    await expect(deploymentCore.deployWorkerWithSecret({
      runCommand,
      nodePath: "/test/node",
      wranglerPath: "/test/wrangler.js",
      dashboardRoot,
      configPath,
      enrollmentHash: hash,
      environment: {},
      marker: "pending-marker",
    })).rejects.toThrow(/terminated deployment/iu);

    expect(calls[0]).toMatchObject({
      argumentsList: expect.arrayContaining(["secret", "put", "ENROLLMENT_TOKEN_HASH", "--config", configPath]),
      input: `${hash}\n`,
    });
    const entries = await readdir(dashboardRoot, { recursive: true });
    expect(entries.filter((entry) => String(entry).includes(".executor-secrets-"))).toEqual([]);
  });

  test("cleans the protected external first-deploy secret file after a terminated deployment", async () => {
    const dashboardRoot = await mkdtemp(join(tmpdir(), "executor-dashboard-first-worker-root-"));
    const secretRoot = await mkdtemp(join(tmpdir(), "executor-dashboard-first-worker-state-"));
    const secretDirectory = join(secretRoot, "dashboard");
    const configPath = join(dashboardRoot, "wrangler.generated.jsonc");
    await writeFile(configPath, "{}\n", { mode: 0o600 });
    let externalSecretPath = "";
    const runCommand = vi.fn(async (_command, argumentsList) => {
      const index = argumentsList.indexOf("--secrets-file");
      if (index >= 0) externalSecretPath = argumentsList[index + 1];
      throw new Error("simulated killed first deployment");
    });

    await expect(deploymentCore.deployWorkerWithSecret({
      runCommand,
      nodePath: "/test/node",
      wranglerPath: "/test/wrangler.js",
      dashboardRoot,
      configPath,
      enrollmentHash: "b".repeat(64),
      environment: {},
      marker: "first-marker",
      workerExists: false,
      secretDirectory,
      platform: "linux",
    })).rejects.toThrow(/killed first deployment/iu);

    expect(externalSecretPath.startsWith(resolve(secretDirectory, ".executor-worker-secret-"))).toBe(true);
    await expect(lstat(externalSecretPath)).rejects.toMatchObject({ code: "ENOENT" });
    expect((await readdir(secretDirectory)).filter((name) => name.includes("worker-secret"))).toEqual([]);
    expect((await readdir(dashboardRoot, { recursive: true })).filter((name) => String(name).includes("secret"))).toEqual([]);
  });
});

describe("protected deployment state and enrollment material", () => {
  unixTest("round-trips versioned mode-0600 state and rejects newer or mismatched state", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-state-"));
    const statePath = join(directory, "dashboard", "deployment.json");
    const state = {
      schema_version: 1,
      owner: "executor-unified-dashboard",
      account_id: accountID,
      hostname,
      worker_name: "executor-dashboard",
      last_completed_stage: "preflight",
    };
    await saveDeploymentState(statePath, state, { platform: "linux" });
    expect((await lstat(statePath)).mode & 0o777).toBe(0o600);
    await expect(loadDeploymentState(statePath, { accountID, hostname, platform: "linux" })).resolves.toMatchObject(state);

    await writeFile(statePath, `${JSON.stringify({ ...state, schema_version: 2 })}\n`, { mode: 0o600 });
    await expect(loadDeploymentState(statePath, { accountID, hostname, platform: "linux" })).rejects.toThrow(/newer deployment state/iu);
    await writeFile(statePath, `${JSON.stringify(state)}\n`, { mode: 0o600 });
    await expect(loadDeploymentState(statePath, { accountID: "fedcba9876543210fedcba9876543210", hostname, platform: "linux" })).rejects.toThrow(/different Cloudflare account/iu);
  });

  unixTest("refuses state writes through symlink ancestors and never chmods a shared parent", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-state-ancestor-"));
    const sharedParent = join(directory, "secure");
    const redirected = join(directory, "redirected");
    await mkdir(sharedParent, { mode: 0o755 });
    await mkdir(redirected, { mode: 0o755 });
    await symlink(redirected, join(sharedParent, "executor"), "dir");

    await expect(saveDeploymentState(join(sharedParent, "executor", "deployment.json"), {
      schema_version: 1,
      owner: "executor-unified-dashboard",
      account_id: accountID,
      hostname,
    }, { platform: "linux" })).rejects.toThrow(/unsafe path ancestor/iu);
    expect((await lstat(sharedParent)).mode & 0o777).toBe(0o755);
    await expect(lstat(join(redirected, "deployment.json"))).rejects.toMatchObject({ code: "ENOENT" });
  });

  test("rejects a newer recorded D1 migration version instead of downgrading", () => {
    expect(() => assertMigrationCompatible(2, 2)).not.toThrow();
    expect(() => assertMigrationCompatible(1, 2)).not.toThrow();
    expect(() => assertMigrationCompatible(3, 2)).toThrow(/newer Dashboard migration/iu);
    expect(() => assertMigrationCompatible(-1, 2)).toThrow(/invalid Dashboard migration/iu);
  });

  unixTest("creates an enrollment bearer once, hashes it, and rotates only explicitly", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-enrollment-"));
    const tokenPath = join(directory, "dashboard", "enrollment.token");
    const first = await ensureEnrollmentToken(tokenPath, { platform: "linux", randomBytes: () => Buffer.alloc(32, 1) });
    expect(first.created).toBe(true);
    expect(first.hash).toMatch(/^[0-9a-f]{64}$/u);
    expect((await lstat(tokenPath)).mode & 0o777).toBe(0o600);
    const firstValue = await readFile(tokenPath, "utf8");

    const reused = await ensureEnrollmentToken(tokenPath, { platform: "linux", randomBytes: () => Buffer.alloc(32, 2) });
    expect(reused.created).toBe(false);
    expect(await readFile(tokenPath, "utf8")).toBe(firstValue);

    const rotated = await ensureEnrollmentToken(tokenPath, { platform: "linux", rotate: true, randomBytes: () => Buffer.alloc(32, 3) });
    expect(rotated.created).toBe(true);
    expect(await readFile(tokenPath, "utf8")).not.toBe(firstValue);
    expect(rotated.hash).not.toBe(first.hash);
  });

  test("preserves disabled enrollment without recreating or hashing a bearer", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-disabled-enrollment-"));
    const tokenPath = join(directory, "dashboard", "enrollment.token");
    const disabled = await ensureEnrollmentToken(tokenPath, {
      platform: "linux",
      enabled: false,
      randomBytes: () => {
        throw new Error("disabled enrollment generated a bearer");
      },
    });

    expect(disabled).toEqual({ created: false, enabled: false, hash: null });
    await expect(lstat(tokenPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  test("enables enrollment exactly once on a first deployment", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentDeployment");
    const state = {};
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-first-enrollment-")), "dashboard", "enrollment.token");
    const result = await deploymentCore.prepareEnrollmentDeployment(state, tokenPath, {
      platform: "linux", randomBytes: () => Buffer.alloc(32, 11),
    });
    expect(result).toMatchObject({ created: true, enabled: true });
    expect(state).toMatchObject({ enrollment_enabled: true, enrollment_token_file: tokenPath });
  });

  unixTest("reuses the same bearer on an enabled upgrade", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentDeployment");
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-enabled-upgrade-")), "dashboard", "enrollment.token");
    const state = {};
    await deploymentCore.prepareEnrollmentDeployment(state, tokenPath, { platform: "linux", randomBytes: () => Buffer.alloc(32, 12) });
    const original = await readFile(tokenPath, "utf8");
    const result = await deploymentCore.prepareEnrollmentDeployment(state, tokenPath, {
      platform: "linux",
      randomBytes: () => {
        throw new Error("enabled upgrade replaced the bearer");
      },
    });
    expect(result.created).toBe(false);
    expect(await readFile(tokenPath, "utf8")).toBe(original);
  });

  test("keeps a disabled upgrade disabled without a bearer or secret hash", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentDeployment");
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-disabled-upgrade-")), "dashboard", "enrollment.token");
    const state = { enrollment_enabled: false, enrollment_token_file: tokenPath };
    const result = await deploymentCore.prepareEnrollmentDeployment(state, tokenPath, {
      platform: "linux",
      randomBytes: () => {
        throw new Error("disabled upgrade generated a bearer");
      },
    });
    expect(result).toEqual({ created: false, enabled: false, hash: null });
    expect(state.enrollment_enabled).toBe(false);
    await expect(lstat(tokenPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  test("only explicit rotation re-enables disabled enrollment", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentDeployment");
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-rotate-enrollment-")), "dashboard", "enrollment.token");
    const state = { enrollment_enabled: false, enrollment_token_file: tokenPath };
    const result = await deploymentCore.prepareEnrollmentDeployment(state, tokenPath, {
      platform: "linux", rotate: true, randomBytes: () => Buffer.alloc(32, 13),
    });
    expect(result).toMatchObject({ created: true, enabled: true });
    expect(state.enrollment_enabled).toBe(true);
  });

  unixTest("reuses a bearer left by a partial first-deployment retry", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentDeployment");
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-partial-enrollment-")), "dashboard", "enrollment.token");
    const partialState = {};
    await ensureEnrollmentToken(tokenPath, { platform: "linux", randomBytes: () => Buffer.alloc(32, 14) });
    const original = await readFile(tokenPath, "utf8");
    const result = await deploymentCore.prepareEnrollmentDeployment(partialState, tokenPath, {
      platform: "linux",
      randomBytes: () => {
        throw new Error("partial retry replaced the bearer");
      },
    });
    expect(result.created).toBe(false);
    expect(await readFile(tokenPath, "utf8")).toBe(original);
    expect(partialState.enrollment_enabled).toBe(true);
    await rm(tokenPath);
  });

  test("does not replace an existing unsafe enrollment path", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-unsafe-enrollment-"));
    const targetPath = join(directory, "target.token");
    const tokenPath = join(directory, "enrollment.token");
    await writeFile(targetPath, "do-not-replace\n", { mode: 0o600 });
    await symlink(targetPath, tokenPath);
    await expect(ensureEnrollmentToken(tokenPath, { platform: "linux" })).rejects.toThrow(/unsafe path ancestor|regular protected file/iu);
    expect(await readFile(targetPath, "utf8")).toBe("do-not-replace\n");
  });

  unixTest("disables only the exact state-owned protected enrollment file", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-disable-enrollment-"));
    const tokenPath = join(directory, "enrollment.token");
    await writeFile(tokenPath, "test-only-enrollment-token\n", { mode: 0o600 });
    await expect(removeProtectedFileIfOwned(tokenPath, join(directory, "different.token"), { platform: "linux" })).rejects.toThrow(/does not prove ownership/iu);
    expect(await readFile(tokenPath, "utf8")).toContain("test-only");

    const linkPath = join(directory, "enrollment-link.token");
    await symlink(tokenPath, linkPath);
    await expect(removeProtectedFileIfOwned(linkPath, linkPath, { platform: "linux" })).rejects.toThrow(/unsafe path ancestor|regular protected file/iu);
    expect(await readFile(tokenPath, "utf8")).toContain("test-only");

    await expect(removeProtectedFileIfOwned(tokenPath, tokenPath, { platform: "linux" })).resolves.toBe(true);
    await expect(lstat(tokenPath)).rejects.toMatchObject({ code: "ENOENT" });
    await expect(removeProtectedFileIfOwned(tokenPath, tokenPath, { platform: "linux" })).resolves.toBe(false);
  });
});

describe("Cloudflare API boundaries and ownership", () => {
  test("uses an explicit Access team domain without requiring organization-read permission", async () => {
    const state = {};
    const persist = vi.fn(async () => {});
    const client = { getOrganization: vi.fn(async () => { throw new Error("organization read forbidden"); }) };

    await expect(ensureAccessTeamDomain(client, state, "executor.cloudflareaccess.com", persist))
      .resolves.toBe("executor.cloudflareaccess.com");
    expect(client.getOrganization).not.toHaveBeenCalled();
    expect(state.access_team_domain).toBe("executor.cloudflareaccess.com");
    expect(persist).toHaveBeenCalledOnce();
  });

  test("verifies the explicit team domain against the live Access redirect", async () => {
    const redirect = vi.fn(async () => new Response(null, {
      status: 302,
      headers: { location: "https://purple-silence-448c.cloudflareaccess.com/cdn-cgi/access/login/dashboard.example.test?kid=test" },
    }));
    await expect(verifyAccessTeamDomain(hostname, "purple-silence-448c.cloudflareaccess.com", redirect))
      .resolves.toBe("purple-silence-448c.cloudflareaccess.com");
    await expect(verifyAccessTeamDomain(hostname, "executor.cloudflareaccess.com", redirect))
      .rejects.toThrow(/does not match|redirect/iu);
    expect(redirect).toHaveBeenCalledWith(`https://${hostname}/`, expect.objectContaining({ redirect: "manual" }));
  });

  test("reuses persisted Access team domain and otherwise reads the organization", async () => {
    const persisted = { access_team_domain: "executor.cloudflareaccess.com" };
    const persistedClient = { getOrganization: vi.fn() };
    await expect(ensureAccessTeamDomain(persistedClient, persisted, undefined, async () => {}))
      .resolves.toBe("executor.cloudflareaccess.com");
    expect(persistedClient.getOrganization).not.toHaveBeenCalled();

    const state = {};
    const persist = vi.fn(async () => {});
    const client = { getOrganization: vi.fn(async () => ({ auth_domain: "team.cloudflareaccess.com" })) };
    await expect(ensureAccessTeamDomain(client, state, undefined, persist))
      .resolves.toBe("team.cloudflareaccess.com");
    expect(state.access_team_domain).toBe("team.cloudflareaccess.com");
  });

  test("models the official single-page Worker list envelope and paginates D1 lists", async () => {
    const requests = [];
    const client = new CloudflareClient({
      accountID,
      token: "test-only-api-token",
      fetchImpl: async (url, init) => {
        requests.push({ url: String(url), init });
        if (String(url).includes("/workers/scripts")) {
          return new Response(JSON.stringify({
            success: true,
            errors: [],
            messages: [],
            result: [{ id: "executor-dashboard", created_on: "2026-08-23T00:00:00Z", modified_on: "2026-08-23T00:00:00Z" }],
          }), { status: 200 });
        }
        if (String(url).includes("page=2")) {
          return new Response(JSON.stringify({ success: true, result: [{ id: "worker-2" }], result_info: { total_pages: 2 } }), { status: 200 });
        }
        return new Response(JSON.stringify({ success: true, result: [{ id: "worker-1" }], result_info: { total_pages: 2 } }), { status: 200 });
      },
    });
    await expect(client.listWorkerScripts()).resolves.toEqual([
      { id: "executor-dashboard", created_on: "2026-08-23T00:00:00Z", modified_on: "2026-08-23T00:00:00Z" },
    ]);
    expect(requests).toHaveLength(1);
    expect(requests[0].url).toBe(`https://api.cloudflare.com/client/v4/accounts/${accountID}/workers/scripts`);

    requests.length = 0;
    await expect(client.listD1Databases()).resolves.toEqual([{ id: "worker-1" }, { id: "worker-2" }]);
    expect(requests).toHaveLength(2);

    const failing = new CloudflareClient({
      accountID,
      token: "test-only-api-token",
      fetchImpl: async () => new Response(JSON.stringify({ success: false, errors: [{ message: "Bearer test-only-api-token rejected" }] }), { status: 403 }),
    });
    await expect(failing.getOrganization()).rejects.not.toThrow(/test-only-api-token/u);
    await expect(failing.getOrganization()).rejects.toThrow(/Cloudflare API request failed/iu);
  });

  test("uses pinned D1 query and Worker deployment API envelopes", async () => {
    const requests = [];
    const client = new CloudflareClient({
      accountID,
      token: "test-only-api-token",
      fetchImpl: async (url, init) => {
        requests.push({ url: String(url), init });
        if (String(url).endsWith("/query")) {
          return new Response(JSON.stringify({
            success: true,
            errors: [],
            messages: [],
            result: [{
              success: true,
              results: [{ name: "0001_control_plane.sql" }],
              meta: { served_by: "v3-prod", duration: 0.2, changes: 0, rows_read: 1, rows_written: 0 },
            }],
          }), { status: 200 });
        }
        return new Response(JSON.stringify({
          success: true,
          errors: [],
          messages: [],
          result: {
            deployments: [{
              id: "deployment-1",
              created_on: "2026-08-23T00:00:00Z",
              source: "api",
              strategy: "percentage",
              versions: [{ percentage: 100, version_id: "version-1" }],
              annotations: { "workers/message": "Executor deployment marker" },
            }],
          },
        }), { status: 200 });
      },
    });

    await expect(client.queryD1("db-owned", "SELECT name FROM d1_migrations ORDER BY id ASC"))
      .resolves.toEqual([{ name: "0001_control_plane.sql" }]);
    expect(requests[0].url).toBe(`https://api.cloudflare.com/client/v4/accounts/${accountID}/d1/database/db-owned/query`);
    expect(JSON.parse(requests[0].init.body)).toEqual({ sql: "SELECT name FROM d1_migrations ORDER BY id ASC" });
    await expect(client.listWorkerDeployments("executor-dashboard")).resolves.toEqual([
      expect.objectContaining({ id: "deployment-1", versions: [{ percentage: 100, version_id: "version-1" }] }),
    ]);
    expect(requests[1].url).toBe(`https://api.cloudflare.com/client/v4/accounts/${accountID}/workers/scripts/executor-dashboard/deployments`);
  });

  test("queries the exact state-owned D1 migration table and rejects remote divergence", async () => {
    expect(deploymentCore).toHaveProperty("assertRemoteMigrationsCompatible");
    const assertRemoteMigrationsCompatible = deploymentCore.assertRemoteMigrationsCompatible;
    const local = ["0001_control_plane.sql", "0002_device_tombstones.sql"];
    const makeClient = (tables, migrations) => ({
      queryD1: vi.fn(async (_databaseID, sql) => {
        if (sql.includes("sqlite_schema")) return [{ name: tables ? "d1_migrations" : undefined }].filter((row) => row.name);
        return migrations.map((name) => ({ name }));
      }),
    });
    const state = { d1_database_id: "db-owned", d1_owned_by_executor: true };

    await expect(assertRemoteMigrationsCompatible(makeClient(false, []), state, local)).resolves.toEqual([]);
    await expect(assertRemoteMigrationsCompatible(makeClient(true, [local[0]]), state, local)).resolves.toEqual([local[0]]);
    await expect(assertRemoteMigrationsCompatible(makeClient(true, [...local, "0003_future.sql"]), state, local)).rejects.toThrow(/newer|unknown/iu);
    await expect(assertRemoteMigrationsCompatible(makeClient(true, [local[1], local[0]]), state, local)).rejects.toThrow(/divergent/iu);
    await expect(assertRemoteMigrationsCompatible(makeClient(true, [local[1]]), state, local)).rejects.toThrow(/divergent/iu);

    const unowned = { d1_database_id: "db-existing", d1_owned_by_executor: false };
    await expect(assertRemoteMigrationsCompatible(makeClient(false, []), unowned, local)).rejects.toThrow(/migration table/iu);
  });

  test("reuses one exact D1 database and rejects ambiguous duplicates", async () => {
    const persist = vi.fn(async () => {});
    const client = {
      listD1Databases: vi.fn(async () => [{ name: "executor-dashboard", uuid: "db-1" }]),
      createD1Database: vi.fn(),
    };
    const state = {};
    await expect(ensureD1Database(client, state, "executor-dashboard", persist)).resolves.toBe("db-1");
    expect(state).toMatchObject({ d1_database_id: "db-1", d1_database_name: "executor-dashboard", d1_owned_by_executor: false });
    expect(client.createD1Database).not.toHaveBeenCalled();

    client.listD1Databases.mockResolvedValueOnce([
      { name: "executor-dashboard", uuid: "db-1" },
      { name: "executor-dashboard", uuid: "db-2" },
    ]);
    await expect(ensureD1Database(client, {}, "executor-dashboard", persist)).rejects.toThrow(/multiple exact D1/iu);
  });

  test("records creation intent before creating D1", async () => {
    const calls = [];
    const state = {};
    const client = {
      listD1Databases: vi.fn(async () => []),
      createD1Database: vi.fn(async () => {
        calls.push("create");
        expect(state.d1_creation_pending).toBe(true);
        return { name: "executor-dashboard", uuid: "db-created" };
      }),
    };
    const persist = vi.fn(async () => calls.push("persist"));
    await expect(ensureD1Database(client, state, "executor-dashboard", persist)).resolves.toBe("db-created");
    expect(calls[0]).toBe("persist");
    expect(state).toMatchObject({ d1_database_id: "db-created", d1_owned_by_executor: true, d1_creation_pending: false });
  });

  test("fails closed when D1 creation was interrupted before an exact create-response UUID was persisted", async () => {
    const state = { d1_database_name: "executor-dashboard", d1_creation_pending: true };
    const client = {
      listD1Databases: vi.fn(async () => []),
      createD1Database: vi.fn(),
    };

    await expect(ensureD1Database(client, state, "executor-dashboard", async () => {}))
      .rejects.toThrow(/create-response UUID|manual recovery/iu);
    expect(client.createD1Database).not.toHaveBeenCalled();
    expect(state.d1_database_id).toBeUndefined();
    expect(state.d1_owned_by_executor).not.toBe(true);
  });

  test("finalizes an interrupted D1 creation only from the persisted exact create-response UUID", async () => {
    const state = {
      d1_database_name: "executor-dashboard",
      d1_creation_pending: true,
      d1_pending_create_response_uuid: "db-created",
    };
    const client = {
      listD1Databases: vi.fn(async () => [{ name: "executor-dashboard", uuid: "db-created" }]),
      createD1Database: vi.fn(),
    };

    await expect(ensureD1Database(client, state, "executor-dashboard", async () => {})).resolves.toBe("db-created");
    expect(client.createD1Database).not.toHaveBeenCalled();
    expect(state).toMatchObject({
      d1_database_id: "db-created",
      d1_owned_by_executor: true,
      d1_creation_pending: false,
    });
    expect(state.d1_pending_create_response_uuid).toBeUndefined();
  });

  test("never adopts a foreign same-name D1 while exact creation ownership is pending", async () => {
    const state = {
      d1_database_name: "executor-dashboard",
      d1_creation_pending: true,
      d1_pending_create_response_uuid: "db-created",
    };
    const client = {
      listD1Databases: vi.fn(async () => [{ name: "executor-dashboard", uuid: "db-foreign" }]),
      createD1Database: vi.fn(),
    };

    await expect(ensureD1Database(client, state, "executor-dashboard", async () => {}))
      .rejects.toThrow(/exact create-response UUID|manual recovery/iu);
    expect(client.createD1Database).not.toHaveBeenCalled();
    expect(state.d1_database_id).toBeUndefined();
  });

  test("creates exact owner and device-path Access applications with isolated policies", async () => {
    const state = {};
    const persist = vi.fn(async () => {});
    const client = {
      listAccessApplications: vi.fn(async () => []),
      createAccessApplication: vi.fn(async (payload) => ({ ...payload, id: payload.domain.includes("/api/device/") ? "app-device" : "app-owner", aud: payload.domain.includes("/api/device/") ? "aud-device" : "aud-owner" })),
      updateAccessApplication: vi.fn(async (id, payload) => ({ ...payload, id, aud: id === "app-device" ? "aud-device" : "aud-owner" })),
      listAccessPolicies: vi.fn(async () => []),
      createAccessPolicy: vi.fn(async (appID, payload) => ({ ...payload, id: appID === "app-device" ? "policy-device" : "policy-owner" })),
      updateAccessPolicy: vi.fn(async (_appID, policyID, payload) => ({ ...payload, id: policyID })),
    };
    await expect(ensureAccessResources(client, state, { hostname, allowedEmail }, persist)).resolves.toEqual({
      applicationID: "app-owner",
      audience: "aud-owner",
    });
    expect(state).toMatchObject({
      access_application_id: "app-owner",
      access_application_aud: "aud-owner",
      access_policy_id: "policy-owner",
      device_access_application_id: "app-device",
      device_access_policy_id: "policy-device",
      access_owned_by_executor: true,
    });
    expect(client.createAccessApplication).toHaveBeenCalledWith(expect.objectContaining({ type: "self_hosted", domain: hostname }));
    expect(client.createAccessApplication).toHaveBeenCalledWith(expect.objectContaining({ type: "self_hosted", domain: `${hostname}/api/device/*` }));
    expect(client.createAccessPolicy).toHaveBeenCalledWith("app-owner", expect.objectContaining({
      decision: "allow",
      include: [{ email: { email: allowedEmail } }],
    }));
    expect(client.createAccessPolicy).toHaveBeenCalledWith("app-device", expect.objectContaining({
      decision: "bypass",
      include: [{ everyone: {} }],
    }));

    client.listAccessApplications.mockResolvedValueOnce([
      { id: "app-owner", aud: "aud-owner", name: "Executor Unified Dashboard", domain: hostname, type: "self_hosted" },
      { id: "app-device", aud: "aud-device", name: "Executor Unified Dashboard device ingress", domain: `${hostname}/api/device/*`, type: "self_hosted" },
    ]);
    client.listAccessPolicies
      .mockResolvedValueOnce([{ id: "policy-owner", name: "Executor owner email", decision: "allow", include: [{ email: { email: allowedEmail } }] }])
      .mockResolvedValueOnce([{ id: "policy-device", name: "Executor device ingress bypass", decision: "bypass", include: [{ everyone: {} }] }]);
    await ensureAccessResources(client, state, { hostname, allowedEmail }, persist);
    expect(client.updateAccessApplication).toHaveBeenCalledWith("app-owner", expect.objectContaining({ domain: hostname }));
    expect(client.updateAccessPolicy).toHaveBeenCalledWith("app-owner", "policy-owner", expect.objectContaining({ include: [{ email: { email: allowedEmail } }] }));
  });

  test.each([
    { name: "allow everyone", policy: { id: "foreign", name: "foreign", decision: "allow", include: [{ everyone: {} }] } },
    { name: "extra bypass", policy: { id: "foreign", name: "foreign", decision: "bypass", include: [{ everyone: {} }] } },
    { name: "extra service auth", policy: { id: "foreign", name: "foreign", decision: "non_identity", include: [{ service_token: { token_id: "foreign" } }] } },
    { name: "duplicate allow", policy: { id: "duplicate", name: "Executor owner email", decision: "allow", include: [{ email: { email: allowedEmail } }] } },
  ])("rejects $name alongside the exact owned Access policy", async ({ policy }) => {
    const state = {
      access_application_id: "app-owner",
      access_application_aud: "aud-owner",
      access_policy_id: "policy-owner",
      access_owned_by_executor: true,
    };
    const client = {
      listAccessApplications: vi.fn(async () => [{ id: "app-owner", aud: "aud-owner", name: "Executor Unified Dashboard", domain: hostname, type: "self_hosted" }]),
      updateAccessApplication: vi.fn(async (_id, payload) => ({ ...payload, id: "app-owner", aud: "aud-owner" })),
      listAccessPolicies: vi.fn(async () => [
        { id: "policy-owner", name: "Executor owner email", decision: "allow", include: [{ email: { email: allowedEmail } }] },
        policy,
      ]),
    };
    await expect(ensureAccessResources(client, state, { hostname, allowedEmail }, async () => {})).rejects.toThrow(/extra|unowned|exact/iu);
  });

  test("does not claim Worker ownership before creation and verifies stable remote deployment identity", async () => {
    const state = {};
    const persist = vi.fn(async () => {});
    const absentClient = {
      listWorkerScripts: vi.fn(async () => []),
      listWorkerDeployments: vi.fn(async () => []),
    };
    const beforeCreate = await ensureWorkerOwnership(absentClient, state, "executor-dashboard", persist, { marker: "pending-marker" });
    expect(beforeCreate).toEqual({ marker: "pending-marker", skipDeploy: false, workerExists: false });
    expect(state.worker_owned_by_executor).not.toBe(true);
    expect(state.worker_creation_pending).toBe(true);

    expect(deploymentCore).toHaveProperty("recordWorkerDeployment");
    const recordWorkerDeployment = deploymentCore.recordWorkerDeployment;
    const createdClient = {
      listWorkerDeployments: vi.fn(async () => [{
        id: "deployment-1",
        created_on: "2026-08-23T00:00:00Z",
        source: "api",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "version-1" }],
        annotations: { "workers/message": "Executor deployment pending-marker" },
      }]),
    };
    await expect(recordWorkerDeployment(createdClient, state, "executor-dashboard", persist)).resolves.toBeUndefined();
    expect(state).toMatchObject({
      worker_owned_by_executor: true,
      worker_deployment_id: "deployment-1",
      worker_version_ids: ["version-1"],
      worker_creation_pending: false,
    });

    const replacedClient = {
      listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]),
      listWorkerDeployments: vi.fn(async () => [{
        id: "manual-replacement",
        created_on: "2026-08-23T01:00:00Z",
        source: "api",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "manual-version" }],
        annotations: {},
      }]),
    };
    await expect(ensureWorkerOwnership(replacedClient, state, "executor-dashboard", persist, { marker: "upgrade-marker" }))
      .rejects.toThrow(/remote identity|drift|replaced/iu);
  });

  test("recovers only an interrupted Worker deploy carrying the exact pending marker", async () => {
    const state = {
      worker_name: "executor-dashboard",
      worker_creation_pending: true,
      worker_pending_marker: "expected-marker",
    };
    const exactClient = {
      listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]),
      listWorkerDeployments: vi.fn(async () => [{
        id: "deployment-1",
        created_on: "2026-08-23T00:00:00Z",
        source: "api",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "version-1" }],
        annotations: { "workers/message": "Executor deployment expected-marker" },
      }]),
    };
    await expect(ensureWorkerOwnership(exactClient, state, "executor-dashboard", async () => {}, { marker: "unused" }))
      .resolves.toEqual({ marker: "expected-marker", skipDeploy: true, workerExists: true });
    expect(state.worker_owned_by_executor).toBe(true);

    const wrongState = { worker_name: "executor-dashboard", worker_creation_pending: true, worker_pending_marker: "expected-marker" };
    const wrongClient = {
      ...exactClient,
      listWorkerDeployments: vi.fn(async () => [{
        id: "deployment-foreign",
        created_on: "2026-08-23T00:00:00Z",
        source: "api",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "version-foreign" }],
        annotations: { "workers/message": "manual replacement" },
      }]),
    };
    await expect(ensureWorkerOwnership(wrongClient, wrongState, "executor-dashboard", async () => {}, { marker: "unused" }))
      .rejects.toThrow(/pending.*proven|marker/iu);
  });

  test("keeps generated Worker messages within Cloudflare's annotation limit", async () => {
    const state = {};
    const client = { listWorkerScripts: vi.fn(async () => []) };
    const plan = await ensureWorkerOwnership(client, state, "executor-dashboard", async () => {});
    expect(`Executor deployment ${plan.marker}`).toHaveLength(50);
  });

  test("recovers the exact deterministic Cloudflare truncation of a legacy pending marker", async () => {
    const marker = "0762bc0b2bf33551af3d89f4a1faab45";
    const state = {
      worker_name: "executor-dashboard",
      worker_creation_pending: true,
      worker_pending_marker: marker,
    };
    const client = {
      listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]),
      listWorkerDeployments: vi.fn(async () => [{
        id: "deployment-legacy",
        created_on: "2026-08-23T08:42:45Z",
        source: "wrangler",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "version-legacy" }],
        annotations: { "workers/message": "Executor deployment 0762bc0b2bf33551af3d89f4a1f..." },
      }]),
    };

    await expect(ensureWorkerOwnership(client, state, "executor-dashboard", async () => {}))
      .resolves.toEqual({ marker, skipDeploy: true, workerExists: true });
    expect(state.worker_deployment_id).toBe("deployment-legacy");
  });

  unixTest("recovers an accepted pending enrollment rotation before considering a new bearer", async () => {
    expect(deploymentCore).toHaveProperty("prepareEnrollmentRotation");
    const tokenPath = join(await mkdtemp(join(tmpdir(), "executor-dashboard-pending-rotation-")), "dashboard", "enrollment.token");
    await ensureEnrollmentToken(tokenPath, { platform: "linux", randomBytes: () => Buffer.alloc(32, 21) });
    const bearerA = await readFile(tokenPath, "utf8");
    const hashA = createHash("sha256").update(bearerA.trim()).digest("hex");
    const state = {
      worker_name: "executor-dashboard",
      worker_owned_by_executor: true,
      worker_deployment_id: "deployment-before",
      worker_version_ids: ["version-before"],
      worker_owned_deployment_ids: ["deployment-before"],
      worker_owned_version_ids: ["version-before"],
      worker_creation_pending: true,
      worker_pending_marker: "rotate-marker",
      worker_pending_previous_deployment_id: "deployment-before",
      worker_pending_operation: "rotate-enrollment",
      enrollment_rotation_pending_hash: hashA,
      enrollment_enabled: true,
      enrollment_token_file: tokenPath,
    };
    const client = {
      listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]),
      listWorkerDeployments: vi.fn(async () => [{
        id: "deployment-after",
        created_on: "2026-08-23T02:00:00Z",
        source: "api",
        strategy: "percentage",
        versions: [{ percentage: 100, version_id: "version-after" }],
        annotations: { "workers/message": "Executor deployment rotate-marker" },
      }]),
    };
    const randomBytes = vi.fn(() => {
      throw new Error("retry generated bearer B");
    });

    const result = await deploymentCore.prepareEnrollmentRotation(
      client,
      state,
      "executor-dashboard",
      tokenPath,
      async () => {},
      { platform: "linux", randomBytes },
    );

    expect(result).toMatchObject({ resumed: true, workerPlan: { skipDeploy: true }, enrollment: { hash: hashA } });
    expect(randomBytes).not.toHaveBeenCalled();
    expect(await readFile(tokenPath, "utf8")).toBe(bearerA);
    expect(state.worker_deployment_id).toBe("deployment-after");
  });

  test("records a successful Worker upgrade while retaining explicit owned history", async () => {
    const state = {
      worker_name: "executor-dashboard",
      worker_owned_by_executor: true,
      worker_deployment_id: "deployment-1",
      worker_version_ids: ["version-1"],
      worker_owned_deployment_ids: ["deployment-1"],
      worker_owned_version_ids: ["version-1"],
    };
    const current = {
      id: "deployment-1", created_on: "2026-08-23T00:00:00Z", source: "api", strategy: "percentage",
      versions: [{ percentage: 100, version_id: "version-1" }], annotations: { "workers/message": "Executor deployment first" },
    };
    const client = {
      listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]),
      listWorkerDeployments: vi.fn(async () => [current]),
    };
    await expect(ensureWorkerOwnership(client, state, "executor-dashboard", async () => {}, { marker: "upgrade-marker" }))
      .resolves.toEqual({ marker: "upgrade-marker", skipDeploy: false, workerExists: true });
    client.listWorkerDeployments.mockResolvedValueOnce([{
      id: "deployment-2", created_on: "2026-08-23T01:00:00Z", source: "api", strategy: "percentage",
      versions: [{ percentage: 100, version_id: "version-2" }], annotations: { "workers/message": "Executor deployment upgrade-marker" },
    }]);
    await deploymentCore.recordWorkerDeployment(client, state, "executor-dashboard", async () => {});
    expect(state).toMatchObject({
      worker_deployment_id: "deployment-2",
      worker_version_ids: ["version-2"],
      worker_owned_deployment_ids: ["deployment-1", "deployment-2"],
      worker_owned_version_ids: ["version-1", "version-2"],
    });
  });

  test("allows rollback only to an explicitly recorded Executor-owned Worker version", () => {
    expect(deploymentCore).toHaveProperty("assertOwnedRollbackVersion");
    const state = { worker_owned_by_executor: true, worker_owned_version_ids: ["version-1", "version-2"] };
    expect(() => deploymentCore.assertOwnedRollbackVersion(state, "version-1")).not.toThrow();
    expect(() => deploymentCore.assertOwnedRollbackVersion(state, undefined)).toThrow(/explicitly recorded/iu);
    expect(() => deploymentCore.assertOwnedRollbackVersion(state, "manual-version")).toThrow(/explicitly recorded/iu);
  });

  test("does not adopt an unowned Access app or replace an unowned Worker", async () => {
    const appClient = {
      listAccessApplications: vi.fn(async () => [{ id: "foreign-app", name: "Other app", domain: hostname, type: "self_hosted" }]),
    };
    await expect(ensureAccessResources(appClient, {}, { hostname, allowedEmail }, async () => {})).rejects.toThrow(/not owned by Executor/iu);

    const workerClient = { listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]) };
    await expect(ensureWorkerOwnership(workerClient, {}, "executor-dashboard", async () => {})).rejects.toThrow(/not owned by Executor/iu);
  });

  test("rejects an unowned more-specific Access application inside device ingress", async () => {
    const client = {
      listAccessApplications: vi.fn(async () => [{
        id: "foreign-connect-app", name: "Foreign device connect override",
        domain: `${hostname}/api/device/connect/*`, type: "self_hosted", aud: "foreign-aud",
      }]),
      createAccessApplication: vi.fn(async (payload) => ({ ...payload, id: payload.domain.includes("/api/device/") ? "app-device" : "app-owner", aud: "aud" })),
      listAccessPolicies: vi.fn(async () => []),
      createAccessPolicy: vi.fn(async (_applicationID, payload) => ({ ...payload, id: "policy" })),
    };
    await expect(ensureAccessResources(client, {}, { hostname, allowedEmail }, async () => {}))
      .rejects.toThrow(/conflicting.*device|device.*conflict/iu);
  });
});
