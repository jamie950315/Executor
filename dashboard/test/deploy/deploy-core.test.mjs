import { chmod, lstat, mkdtemp, readFile, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, test, vi } from "vitest";

import {
  CloudflareClient,
  assertProtectedFile,
  assertMigrationCompatible,
  assertSupportedNodeVersion,
  assertWranglerV4,
  ensureAccessResources,
  ensureD1Database,
  ensureEnrollmentToken,
  ensureWorkerOwnership,
  loadDeploymentState,
  renderWranglerConfig,
  removeProtectedFileIfOwned,
  saveDeploymentState,
  writeTemporaryWranglerConfig,
} from "../../scripts/lib/deploy-core.mjs";
import {
  normalizeDeploymentOptions,
  parseDeploymentArguments,
} from "../../scripts/lib/deploy-cli.mjs";

const accountID = "0123456789abcdef0123456789abcdef";
const hostname = "dashboard.example.test";
const allowedEmail = "owner@example.test";

describe("deployment prerequisites", () => {
  test("accepts explicit non-secret deployment inputs and rejects token values", () => {
    expect(parseDeploymentArguments([
      "deploy",
      "--hostname", hostname,
      "--account-id", accountID,
      "--api-token-file", "/secure/cloudflare.token",
      "--allowed-email", allowedEmail,
    ])).toMatchObject({
      command: "deploy",
      hostname,
      accountID,
      apiTokenFile: "/secure/cloudflare.token",
      allowedEmail,
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
    expect(normalized.stateFile).toBe("/home/owner/.local/state/executor/dashboard-deployment.json");
    expect(normalized.enrollmentTokenFile).toBe("/home/owner/.local/state/executor/dashboard-enrollment.token");
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

  test("accepts only a regular Unix mode-0600 token file", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-token-"));
    const tokenPath = join(directory, "cloudflare.token");
    await writeFile(tokenPath, "test-only-cloudflare-token\n", { mode: 0o644 });
    await expect(assertProtectedFile(tokenPath, { platform: "linux" })).rejects.toThrow(/0600/iu);

    await chmod(tokenPath, 0o600);
    await expect(assertProtectedFile(tokenPath, { platform: "linux" })).resolves.toBeUndefined();

    const linkPath = join(directory, "cloudflare-link.token");
    await symlink(tokenPath, linkPath);
    await expect(assertProtectedFile(linkPath, { platform: "linux" })).rejects.toThrow(/regular protected file/iu);
  });

  test("delegates Windows ACL verification and fails closed", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-windows-token-"));
    const tokenPath = join(directory, "cloudflare.token");
    await writeFile(tokenPath, "test-only-cloudflare-token\n", { mode: 0o600 });
    const verifyWindowsACL = vi.fn(async () => false);
    await expect(assertProtectedFile(tokenPath, { platform: "win32", verifyWindowsACL })).rejects.toThrow(/Windows ACL/iu);
    expect(verifyWindowsACL).toHaveBeenCalledWith(tokenPath);
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
    expect(temporary.configPath.startsWith(`${dashboardRoot}/.wrangler.executor.generated-`)).toBe(true);
    expect((await lstat(temporary.configPath)).mode & 0o777).toBe(0o600);
    await temporary.cleanup();
    await expect(lstat(temporary.configPath)).rejects.toMatchObject({ code: "ENOENT" });
  });
});

describe("protected deployment state and enrollment material", () => {
  test("round-trips versioned mode-0600 state and rejects newer or mismatched state", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-state-"));
    const statePath = join(directory, "deployment.json");
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

  test("rejects a newer recorded D1 migration version instead of downgrading", () => {
    expect(() => assertMigrationCompatible(2, 2)).not.toThrow();
    expect(() => assertMigrationCompatible(1, 2)).not.toThrow();
    expect(() => assertMigrationCompatible(3, 2)).toThrow(/newer Dashboard migration/iu);
    expect(() => assertMigrationCompatible(-1, 2)).toThrow(/invalid Dashboard migration/iu);
  });

  test("creates an enrollment bearer once, hashes it, and rotates only explicitly", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-enrollment-"));
    const tokenPath = join(directory, "enrollment.token");
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

  test("does not replace an existing unsafe enrollment path", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-unsafe-enrollment-"));
    const targetPath = join(directory, "target.token");
    const tokenPath = join(directory, "enrollment.token");
    await writeFile(targetPath, "do-not-replace\n", { mode: 0o600 });
    await symlink(targetPath, tokenPath);
    await expect(ensureEnrollmentToken(tokenPath, { platform: "linux" })).rejects.toThrow(/regular protected file/iu);
    expect(await readFile(targetPath, "utf8")).toBe("do-not-replace\n");
  });

  test("disables only the exact state-owned protected enrollment file", async () => {
    const directory = await mkdtemp(join(tmpdir(), "executor-dashboard-disable-enrollment-"));
    const tokenPath = join(directory, "enrollment.token");
    await writeFile(tokenPath, "test-only-enrollment-token\n", { mode: 0o600 });
    await expect(removeProtectedFileIfOwned(tokenPath, join(directory, "different.token"), { platform: "linux" })).rejects.toThrow(/does not prove ownership/iu);
    expect(await readFile(tokenPath, "utf8")).toContain("test-only");

    const linkPath = join(directory, "enrollment-link.token");
    await symlink(tokenPath, linkPath);
    await expect(removeProtectedFileIfOwned(linkPath, linkPath, { platform: "linux" })).rejects.toThrow(/regular protected file/iu);
    expect(await readFile(tokenPath, "utf8")).toContain("test-only");

    await expect(removeProtectedFileIfOwned(tokenPath, tokenPath, { platform: "linux" })).resolves.toBe(true);
    await expect(lstat(tokenPath)).rejects.toMatchObject({ code: "ENOENT" });
    await expect(removeProtectedFileIfOwned(tokenPath, tokenPath, { platform: "linux" })).resolves.toBe(false);
  });
});

describe("Cloudflare API boundaries and ownership", () => {
  test("parses paginated API envelopes without exposing upstream error messages", async () => {
    const requests = [];
    const client = new CloudflareClient({
      accountID,
      token: "test-only-api-token",
      fetchImpl: async (url, init) => {
        requests.push({ url: String(url), init });
        if (String(url).includes("page=2")) {
          return new Response(JSON.stringify({ success: true, result: [{ id: "worker-2" }], result_info: { total_pages: 2 } }), { status: 200 });
        }
        return new Response(JSON.stringify({ success: true, result: [{ id: "worker-1" }], result_info: { total_pages: 2 } }), { status: 200 });
      },
    });
    await expect(client.listWorkerScripts()).resolves.toEqual([{ id: "worker-1" }, { id: "worker-2" }]);
    expect(requests).toHaveLength(2);

    const failing = new CloudflareClient({
      accountID,
      token: "test-only-api-token",
      fetchImpl: async () => new Response(JSON.stringify({ success: false, errors: [{ message: "Bearer test-only-api-token rejected" }] }), { status: 403 }),
    });
    await expect(failing.getOrganization()).rejects.not.toThrow(/test-only-api-token/u);
    await expect(failing.getOrganization()).rejects.toThrow(/Cloudflare API request failed/iu);
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

  test("creates and then idempotently updates only state-owned Access resources", async () => {
    const state = {};
    const persist = vi.fn(async () => {});
    const client = {
      listAccessApplications: vi.fn(async () => []),
      createAccessApplication: vi.fn(async (payload) => ({ ...payload, id: "app-1", aud: "aud-1" })),
      updateAccessApplication: vi.fn(async (_id, payload) => ({ ...payload, id: "app-1", aud: "aud-1" })),
      listAccessPolicies: vi.fn(async () => []),
      createAccessPolicy: vi.fn(async (_appID, payload) => ({ ...payload, id: "policy-1" })),
      updateAccessPolicy: vi.fn(async (_appID, _policyID, payload) => ({ ...payload, id: "policy-1" })),
    };
    await expect(ensureAccessResources(client, state, { hostname, allowedEmail }, persist)).resolves.toEqual({
      applicationID: "app-1",
      audience: "aud-1",
    });
    expect(state).toMatchObject({
      access_application_id: "app-1",
      access_application_aud: "aud-1",
      access_policy_id: "policy-1",
      access_owned_by_executor: true,
    });
    expect(client.createAccessApplication).toHaveBeenCalledWith(expect.objectContaining({ type: "self_hosted", domain: hostname }));
    expect(client.createAccessPolicy).toHaveBeenCalledWith("app-1", expect.objectContaining({
      decision: "allow",
      include: [{ email: { email: allowedEmail } }],
    }));

    client.listAccessApplications.mockResolvedValueOnce([{ id: "app-1", aud: "aud-1", name: "Executor Unified Dashboard", domain: hostname, type: "self_hosted" }]);
    client.listAccessPolicies.mockResolvedValueOnce([{ id: "policy-1", name: "Executor owner email", decision: "allow", include: [{ email: { email: allowedEmail } }] }]);
    await ensureAccessResources(client, state, { hostname, allowedEmail }, persist);
    expect(client.updateAccessApplication).toHaveBeenCalledWith("app-1", expect.objectContaining({ domain: hostname }));
    expect(client.updateAccessPolicy).toHaveBeenCalledWith("app-1", "policy-1", expect.objectContaining({ include: [{ email: { email: allowedEmail } }] }));
  });

  test("does not adopt an unowned Access app or replace an unowned Worker", async () => {
    const appClient = {
      listAccessApplications: vi.fn(async () => [{ id: "foreign-app", name: "Other app", domain: hostname, type: "self_hosted" }]),
    };
    await expect(ensureAccessResources(appClient, {}, { hostname, allowedEmail }, async () => {})).rejects.toThrow(/not owned by Executor/iu);

    const workerClient = { listWorkerScripts: vi.fn(async () => [{ id: "executor-dashboard" }]) };
    await expect(ensureWorkerOwnership(workerClient, {}, "executor-dashboard", async () => {})).rejects.toThrow(/not owned by Executor/iu);
  });
});
