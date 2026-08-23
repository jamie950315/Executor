import { createHash, randomBytes as cryptoRandomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import {
  chmod,
  lstat,
  mkdir,
  readFile,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const deploymentStateVersion = 1;
export const deploymentOwner = "executor-unified-dashboard";
export const dashboardWorkerName = "executor-dashboard";
export const dashboardD1Name = "executor-dashboard";
export const accessApplicationName = "Executor Unified Dashboard";
export const accessPolicyName = "Executor owner email";

const protectedFileScript = resolve(dirname(fileURLToPath(import.meta.url)), "..", "protected-file.ps1");

export function assertSupportedNodeVersion(version = process.version) {
  const match = /^v?(\d+)\.(\d+)\.(\d+)$/u.exec(String(version).trim());
  if (match === null) {
    throw new Error("A supported Node version is required (20.19+, 22.13+, or 24+).");
  }
  const major = Number(match[1]);
  const minor = Number(match[2]);
  const supported =
    (major === 20 && minor >= 19) ||
    (major === 22 && minor >= 13) ||
    major >= 24;
  if (!supported) {
    throw new Error("A supported Node version is required (20.19+, 22.13+, or 24+).");
  }
}

export function assertWranglerV4(output) {
  const match = /(?:^|\s)(4\.\d+\.\d+)(?:\s|$)/u.exec(String(output).trim());
  if (match === null) {
    throw new Error("The Dashboard package must provide Wrangler v4.");
  }
  return match[1];
}

export function assertMigrationCompatible(recordedVersion, localVersion) {
  if (!Number.isInteger(recordedVersion) || recordedVersion < 0 || !Number.isInteger(localVersion) || localVersion < 1) {
    throw new Error("Dashboard deployment state has an invalid Dashboard migration version.");
  }
  if (recordedVersion > localVersion) {
    throw new Error("A newer Dashboard migration version is recorded; refusing to downgrade it.");
  }
}

export async function assertProtectedFile(path, options = {}) {
  let info;
  try {
    info = await lstat(path);
  } catch {
    throw new Error("The credential must be supplied in a regular protected file.");
  }
  if (!info.isFile() || info.isSymbolicLink()) {
    throw new Error("The credential must be supplied in a regular protected file.");
  }
  const platform = options.platform ?? process.platform;
  if (platform === "win32") {
    const verifyWindowsACL = options.verifyWindowsACL ?? verifyWindowsProtectedFile;
    if (!(await verifyWindowsACL(path))) {
      throw new Error("The credential file must have a Windows ACL restricted to its owner, Administrators, and SYSTEM.");
    }
    return;
  }
  if ((info.mode & 0o777) !== 0o600) {
    throw new Error("The credential file must have Unix mode 0600.");
  }
}

export function renderWranglerConfig(template, values) {
  const replacements = new Map([
    ["__EXECUTOR_ACCOUNT_ID__", values.accountID],
    ["__EXECUTOR_D1_DATABASE_ID__", values.databaseID],
    ["__EXECUTOR_DASHBOARD_HOSTNAME__", values.hostname],
    ["__EXECUTOR_ACCESS_AUD__", values.accessAUD],
    ["__EXECUTOR_ACCESS_TEAM_DOMAIN__", values.accessTeamDomain],
  ]);
  let rendered = String(template);
  for (const [placeholder, value] of replacements) {
    const quoted = JSON.stringify(placeholder);
    if (!rendered.includes(quoted) || typeof value !== "string" || value.trim() === "") {
      throw new Error("The checked-in Wrangler deployment template is invalid.");
    }
    rendered = rendered.replaceAll(quoted, JSON.stringify(value));
  }
  if (rendered.includes("__EXECUTOR_")) {
    throw new Error("The checked-in Wrangler deployment template is invalid.");
  }
  let parsed;
  try {
    parsed = JSON.parse(rendered);
  } catch {
    throw new Error("The checked-in Wrangler deployment template is invalid.");
  }
  if (
    parsed?.name !== dashboardWorkerName ||
    parsed?.account_id !== values.accountID ||
    parsed?.d1_databases?.[0]?.database_id !== values.databaseID ||
    parsed?.routes?.[0]?.pattern !== values.hostname ||
    parsed?.routes?.[0]?.custom_domain !== true ||
    parsed?.vars?.ACCESS_AUD !== values.accessAUD ||
    parsed?.vars?.ACCESS_TEAM_DOMAIN !== values.accessTeamDomain
  ) {
    throw new Error("The checked-in Wrangler deployment template is invalid.");
  }
  return `${JSON.stringify(parsed, null, 2)}\n`;
}

export async function loadDeploymentState(path, options) {
  try {
    await lstat(path);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return null;
    }
    throw new Error("Dashboard deployment state is unavailable.", { cause: error });
  }
  await assertProtectedFile(path, options);
  let state;
  try {
    state = JSON.parse(await readFile(path, "utf8"));
  } catch {
    throw new Error("Dashboard deployment state is invalid.");
  }
  if (!Number.isInteger(state?.schema_version)) {
    throw new Error("Dashboard deployment state is invalid.");
  }
  if (state.schema_version > deploymentStateVersion) {
    throw new Error("A newer deployment state is present; refusing to downgrade it.");
  }
  if (state.schema_version !== deploymentStateVersion || state.owner !== deploymentOwner) {
    throw new Error("Dashboard deployment state is incompatible.");
  }
  if (options?.accountID !== undefined && state.account_id !== options.accountID) {
    throw new Error("Dashboard deployment state belongs to a different Cloudflare account.");
  }
  if (options?.hostname !== undefined && state.hostname !== options.hostname) {
    throw new Error("Dashboard deployment state belongs to a different hostname.");
  }
  if (state.worker_name !== undefined && state.worker_name !== dashboardWorkerName) {
    throw new Error("Dashboard deployment state names an incompatible Worker.");
  }
  return state;
}

export async function saveDeploymentState(path, state, options = {}) {
  const parent = dirname(path);
  await mkdir(parent, { recursive: true, mode: 0o700 });
  if ((options.platform ?? process.platform) !== "win32") {
    await chmod(parent, 0o700);
  }
  const temporary = `${path}.tmp-${process.pid}-${Date.now()}`;
  const body = `${JSON.stringify(state, null, 2)}\n`;
  try {
    await writeFile(temporary, body, { mode: 0o600, flag: "wx" });
    if ((options.platform ?? process.platform) === "win32") {
      await protectWindowsFile(temporary);
    } else {
      await chmod(temporary, 0o600);
    }
    await rename(temporary, path);
    if ((options.platform ?? process.platform) === "win32") {
      await protectWindowsFile(path);
    } else {
      await chmod(path, 0o600);
    }
  } catch (error) {
    await rm(temporary, { force: true });
    throw error;
  }
}

export async function writeTemporaryWranglerConfig(dashboardRoot, body) {
  const suffix = cryptoRandomBytes(12).toString("hex");
  const configPath = resolve(dashboardRoot, `.wrangler.executor.generated-${process.pid}-${suffix}.jsonc`);
  await writeFile(configPath, body, { mode: 0o600, flag: "wx" });
  if (process.platform !== "win32") {
    await chmod(configPath, 0o600);
  }
  return {
    configPath,
    cleanup: async () => rm(configPath, { force: true }),
  };
}

export async function ensureEnrollmentToken(path, options = {}) {
  const platform = options.platform ?? process.platform;
  const rotate = options.rotate === true;
  let token;
  let created = false;
  let existing = false;
  try {
    await lstat(path);
    existing = true;
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw new Error("The enrollment token file is unavailable.", { cause: error });
    }
  }
  if (existing) {
    await assertProtectedFile(path, options);
    if (!rotate) {
      const info = await lstat(path);
      if (info.size < 1 || info.size > 512) {
        throw new Error("The enrollment token file is invalid.");
      }
      token = Buffer.from((await readFile(path, "utf8")).trim(), "utf8");
    }
  }
  if (token === undefined) {
    const bytes = Buffer.from((options.randomBytes ?? cryptoRandomBytes)(32));
    token = Buffer.from(bytes.toString("base64url"), "utf8");
    bytes.fill(0);
    await writeProtectedSecret(path, `${token.toString("utf8")}\n`, platform);
    created = true;
  }
  if (token.length < 32 || token.length > 512 || /\s/u.test(token.toString("utf8"))) {
    token.fill(0);
    throw new Error("The enrollment token file is invalid.");
  }
  const hash = createHash("sha256").update(token).digest("hex");
  token.fill(0);
  return { created, hash };
}

export async function removeProtectedFileIfOwned(path, ownedPath, options = {}) {
  const platform = options.platform ?? process.platform;
  const actual = platform === "win32" ? resolve(path).toLowerCase() : resolve(path);
  const expected = platform === "win32" ? resolve(ownedPath).toLowerCase() : resolve(ownedPath);
  if (actual !== expected) {
    throw new Error("Deployment state does not prove ownership of the enrollment token file.");
  }
  try {
    await lstat(path);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return false;
    }
    throw new Error("The enrollment token file is unavailable.", { cause: error });
  }
  await assertProtectedFile(path, options);
  await rm(path);
  return true;
}

async function writeProtectedSecret(path, body, platform) {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  if (platform !== "win32") {
    await chmod(dirname(path), 0o700);
  }
  const temporary = `${path}.tmp-${process.pid}-${Date.now()}`;
  try {
    if (platform === "win32") {
      await writeFile(temporary, "", { mode: 0o600, flag: "wx" });
      await protectWindowsFile(temporary);
      await writeFile(temporary, body, { flag: "r+" });
    } else {
      await writeFile(temporary, body, { mode: 0o600, flag: "wx" });
      await chmod(temporary, 0o600);
    }
    await rename(temporary, path);
    if (platform === "win32") {
      await protectWindowsFile(path);
    } else {
      await chmod(path, 0o600);
    }
  } finally {
    await rm(temporary, { force: true });
  }
}

export class CloudflareClient {
  constructor({ accountID, token, fetchImpl = globalThis.fetch }) {
    this.accountID = accountID;
    this.token = token;
    this.fetchImpl = fetchImpl;
  }

  async request(path, { method = "GET", body } = {}) {
    const response = await this.fetchImpl(new URL(path, "https://api.cloudflare.com/client/v4/"), {
      method,
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Content-Type": "application/json",
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let envelope;
    try {
      envelope = await response.json();
    } catch {
      throw new Error(`Cloudflare API request failed with HTTP ${response.status}.`);
    }
    if (!response.ok || envelope?.success !== true || !("result" in envelope)) {
      throw new Error(`Cloudflare API request failed with HTTP ${response.status}.`);
    }
    return envelope;
  }

  async listAll(path) {
    const items = [];
    for (let page = 1; page <= 100; page += 1) {
      const separator = path.includes("?") ? "&" : "?";
      const envelope = await this.request(`${path}${separator}per_page=100&page=${page}`);
      if (!Array.isArray(envelope.result)) {
        throw new Error("Cloudflare API returned an invalid list response.");
      }
      items.push(...envelope.result);
      const totalPages = Number(envelope.result_info?.total_pages ?? 1);
      if (page >= totalPages) {
        return items;
      }
    }
    throw new Error("Cloudflare API pagination exceeded the safety limit.");
  }

  async getOrganization() {
    return (await this.request(`accounts/${this.accountID}/access/organizations`)).result;
  }

  async listD1Databases() {
    return this.listAll(`accounts/${this.accountID}/d1/database`);
  }

  async createD1Database(name) {
    return (await this.request(`accounts/${this.accountID}/d1/database`, { method: "POST", body: { name } })).result;
  }

  async listWorkerScripts() {
    return this.listAll(`accounts/${this.accountID}/workers/scripts`);
  }

  async listAccessApplications() {
    return this.listAll(`accounts/${this.accountID}/access/apps`);
  }

  async createAccessApplication(body) {
    return (await this.request(`accounts/${this.accountID}/access/apps`, { method: "POST", body })).result;
  }

  async updateAccessApplication(id, body) {
    return (await this.request(`accounts/${this.accountID}/access/apps/${encodeURIComponent(id)}`, { method: "PUT", body })).result;
  }

  async listAccessPolicies(applicationID) {
    return this.listAll(`accounts/${this.accountID}/access/apps/${encodeURIComponent(applicationID)}/policies`);
  }

  async createAccessPolicy(applicationID, body) {
    return (await this.request(`accounts/${this.accountID}/access/apps/${encodeURIComponent(applicationID)}/policies`, { method: "POST", body })).result;
  }

  async updateAccessPolicy(applicationID, policyID, body) {
    return (await this.request(`accounts/${this.accountID}/access/apps/${encodeURIComponent(applicationID)}/policies/${encodeURIComponent(policyID)}`, { method: "PUT", body })).result;
  }
}

export async function ensureD1Database(client, state, name, persist) {
  const databases = await client.listD1Databases();
  const exact = databases.filter((database) => database?.name === name);
  if (exact.length > 1) {
    throw new Error("Cloudflare returned multiple exact D1 database matches; refusing an ambiguous deployment.");
  }
  if (state.d1_database_id !== undefined) {
    if (state.d1_database_name !== name) {
      throw new Error("Deployment state names an incompatible D1 database.");
    }
    const owned = exact.find((database) => database.uuid === state.d1_database_id);
    if (owned === undefined) {
      throw new Error("The state-owned D1 database is missing or has been replaced.");
    }
    return owned.uuid;
  }
  if (exact.length === 1) {
    state.d1_database_name = name;
    state.d1_database_id = exact[0].uuid;
    state.d1_owned_by_executor = state.d1_creation_pending === true;
    state.d1_creation_pending = false;
    await persist();
    return exact[0].uuid;
  }
  state.d1_database_name = name;
  state.d1_owned_by_executor = true;
  state.d1_creation_pending = true;
  await persist();
  const created = await client.createD1Database(name);
  if (created?.name !== name || typeof created?.uuid !== "string" || created.uuid === "") {
    throw new Error("Cloudflare returned invalid D1 creation metadata.");
  }
  state.d1_database_id = created.uuid;
  state.d1_creation_pending = false;
  await persist();
  return created.uuid;
}

export async function ensureWorkerOwnership(client, state, workerName, persist) {
  const workers = await client.listWorkerScripts();
  const exists = workers.some((worker) => worker?.id === workerName || worker?.name === workerName);
  if (state.worker_name !== undefined) {
    if (state.worker_name !== workerName || state.worker_owned_by_executor !== true) {
      throw new Error("Deployment state does not prove Executor ownership of the Worker.");
    }
    return;
  }
  if (exists) {
    throw new Error("A Worker with the requested name exists but is not owned by Executor state.");
  }
  state.worker_name = workerName;
  state.worker_owned_by_executor = true;
  state.worker_creation_pending = true;
  await persist();
}

export async function ensureAccessResources(client, state, values, persist) {
  const applicationPayload = {
    name: accessApplicationName,
    type: "self_hosted",
    domain: values.hostname,
    session_duration: "24h",
    app_launcher_visible: false,
  };
  const applications = await client.listAccessApplications();
  let application;
  if (state.access_application_id !== undefined) {
    application = applications.find((candidate) => candidate?.id === state.access_application_id);
    if (!validOwnedApplication(application, values.hostname)) {
      throw new Error("The state-owned Access application is missing or has been replaced.");
    }
    application = await client.updateAccessApplication(application.id, applicationPayload);
  } else if (state.access_application_creation_pending === true) {
    const pending = applications.filter((candidate) => validOwnedApplication(candidate, values.hostname));
    if (pending.length !== 1) {
      throw new Error("Pending Access application ownership cannot be proven.");
    }
    application = pending[0];
  } else {
    const collision = applications.some((candidate) => candidate?.domain === values.hostname || candidate?.name === accessApplicationName);
    if (collision) {
      throw new Error("An Access application for the hostname exists but is not owned by Executor state.");
    }
    state.access_owned_by_executor = true;
    state.access_application_creation_pending = true;
    await persist();
    application = await client.createAccessApplication(applicationPayload);
  }
  if (!validOwnedApplication(application, values.hostname) || typeof application.aud !== "string" || application.aud === "") {
    throw new Error("Cloudflare returned invalid Access application metadata.");
  }
  state.access_application_id = application.id;
  state.access_application_aud = application.aud;
  state.access_application_creation_pending = false;
  state.access_owned_by_executor = true;
  await persist();

  const policyPayload = {
    name: accessPolicyName,
    decision: "allow",
    precedence: 1,
    include: [{ email: { email: values.allowedEmail } }],
  };
  const policies = await client.listAccessPolicies(application.id);
  let policy;
  if (state.access_policy_id !== undefined) {
    policy = policies.find((candidate) => candidate?.id === state.access_policy_id);
    if (policy === undefined || policy.name !== accessPolicyName) {
      throw new Error("The state-owned Access policy is missing or has been replaced.");
    }
    policy = await client.updateAccessPolicy(application.id, policy.id, policyPayload);
  } else if (state.access_policy_creation_pending === true) {
    const pending = policies.filter((candidate) => candidate?.name === accessPolicyName);
    if (pending.length !== 1) {
      throw new Error("Pending Access policy ownership cannot be proven.");
    }
    policy = await client.updateAccessPolicy(application.id, pending[0].id, policyPayload);
  } else {
    if (policies.some((candidate) => candidate?.name === accessPolicyName)) {
      throw new Error("An Access policy with the Executor name exists but is not owned by Executor state.");
    }
    state.access_policy_creation_pending = true;
    await persist();
    policy = await client.createAccessPolicy(application.id, policyPayload);
  }
  if (typeof policy?.id !== "string" || policy.id === "") {
    throw new Error("Cloudflare returned invalid Access policy metadata.");
  }
  state.access_policy_id = policy.id;
  state.access_policy_creation_pending = false;
  state.allowed_email = values.allowedEmail;
  await persist();
  return { applicationID: application.id, audience: application.aud };
}

function validOwnedApplication(application, hostname) {
  return application !== undefined &&
    application?.name === accessApplicationName &&
    application?.domain === hostname &&
    application?.type === "self_hosted" &&
    typeof application?.id === "string" &&
    application.id !== "";
}

async function verifyWindowsProtectedFile(path) {
  return runPowerShellProtection(path, false);
}

async function protectWindowsFile(path) {
  if (!(await runPowerShellProtection(path, true))) {
    throw new Error("Unable to protect a Windows credential or state file.");
  }
}

async function runPowerShellProtection(path, initialize) {
  return new Promise((resolvePromise) => {
    const argumentsList = [
      "-NoProfile",
      "-NonInteractive",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      protectedFileScript,
      "-Path",
      path,
    ];
    if (initialize) {
      argumentsList.push("-Initialize");
    }
    const child = spawn("powershell.exe", argumentsList, { stdio: "ignore", windowsHide: true });
    child.once("error", () => resolvePromise(false));
    child.once("exit", (code) => resolvePromise(code === 0));
  });
}
