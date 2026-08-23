import { createHash, randomBytes as cryptoRandomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import {
  chmod,
  lstat,
  mkdir,
  open as fsOpen,
  readFile,
  readdir,
  readlink,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, isAbsolute, parse, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const deploymentStateVersion = 1;
export const deploymentOwner = "executor-unified-dashboard";
export const dashboardWorkerName = "executor-dashboard";
export const dashboardD1Name = "executor-dashboard";
export const accessApplicationName = "Executor Unified Dashboard";
export const accessPolicyName = "Executor owner email";
export const deviceAccessApplicationName = "Executor Unified Dashboard device ingress";
export const deviceAccessPolicyName = "Executor device ingress bypass";

const ownedDirectoryMarker = ".executor-owned";

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
  await assertSafePathAncestors(path, options);
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

export async function readProtectedCredential(path, options = {}) {
  const platform = options.platform ?? process.platform;
  const openFile = options.openFile ?? fsOpen;
  await assertSafePathAncestors(path, options);
  const noFollow = platform === "win32" ? 0 : (fsConstants.O_NOFOLLOW ?? 0);
  let handle;
  try {
    handle = await openFile(path, fsConstants.O_RDONLY | noFollow);
  } catch (error) {
    throw new Error("The credential must be supplied in a regular protected file.", { cause: error });
  }
  try {
    const opened = await handle.stat({ bigint: true });
    if (!opened.isFile() || opened.size < 1n || opened.size > 4096n) {
      throw new Error("The Cloudflare API token file is invalid.");
    }
    await assertProtectedFile(path, options);
    await assertSafePathAncestors(path, options);
    const current = await lstat(path, { bigint: true });
    if (!current.isFile() || current.isSymbolicLink() ||
        current.dev !== opened.dev || current.ino !== opened.ino ||
        (platform === "win32" && current.ino === 0n)) {
      throw new Error("The Cloudflare API token file identity changed during validation.");
    }
    await options.afterIdentityVerified?.();
    const data = await handle.readFile();
    if (data.length < 1 || data.length > 4096) {
      data.fill(0);
      throw new Error("The Cloudflare API token file is invalid.");
    }
    let start = 0;
    let end = data.length;
    while (start < end && (data[start] === 0x20 || data[start] === 0x0a || data[start] === 0x0d || data[start] === 0x09)) start += 1;
    while (end > start && (data[end - 1] === 0x20 || data[end - 1] === 0x0a || data[end - 1] === 0x0d || data[end - 1] === 0x09)) end -= 1;
    const token = Buffer.from(data.subarray(start, end));
    data.fill(0);
    if (token.length === 0 || token.some((byte) => byte <= 0x20 || byte === 0x7f)) {
      token.fill(0);
      throw new Error("The Cloudflare API token file is invalid.");
    }
    return token;
  } finally {
    await handle.close();
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
  await assertSafePathAncestors(path, options);
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
  await ensureOwnedPrivateDirectory(parent, options);
  await assertSafePathAncestors(path, options);
  try {
    await lstat(path);
    await assertProtectedFile(path, options);
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
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

export async function deployWorkerWithSecret(options) {
  const {
    runCommand,
    nodePath,
    wranglerPath,
    dashboardRoot,
    configPath,
    enrollmentHash,
    environment,
    marker,
    workerExists = true,
    secretDirectory,
    platform = process.platform,
  } = options;
  if (typeof runCommand !== "function" || typeof marker !== "string" || marker === "") {
    throw new Error("Worker deployment inputs are invalid.");
  }
  const argumentsList = [wranglerPath, "deploy", "--config", configPath, "--keep-vars", "--strict", "--message", `Executor deployment ${marker}`];
  let temporarySecrets;
  try {
    if (typeof enrollmentHash === "string") {
      if (!/^[0-9a-f]{64}$/u.test(enrollmentHash)) {
        throw new Error("Enrollment secret hash is invalid.");
      }
      if (workerExists) {
        await runCommand(nodePath, [wranglerPath, "secret", "put", "ENROLLMENT_TOKEN_HASH", "--config", configPath], {
          cwd: dashboardRoot,
          environment,
          input: `${enrollmentHash}\n`,
        });
      } else {
        temporarySecrets = await writeExternalWorkerSecretsFile(secretDirectory, dashboardRoot, enrollmentHash, { platform });
        argumentsList.push("--secrets-file", temporarySecrets.path);
      }
    }
    await runCommand(nodePath, argumentsList, { cwd: dashboardRoot, environment });
  } finally {
    await temporarySecrets?.cleanup();
  }
}

async function writeExternalWorkerSecretsFile(directory, dashboardRoot, hash, options = {}) {
  if (typeof directory !== "string" || directory === "") {
    throw new Error("A protected external Worker secret directory is required for first deployment.");
  }
  const root = resolve(dashboardRoot);
  const targetDirectory = resolve(directory);
  const relation = relative(root, targetDirectory);
  if (relation === "" || (!relation.startsWith("..") && !isAbsolute(relation))) {
    throw new Error("Worker secret material must remain outside the repository.");
  }
  await ensureOwnedPrivateDirectory(targetDirectory, options);
  await cleanupStaleWorkerSecrets(targetDirectory, options);
  const path = resolve(targetDirectory, `.executor-worker-secret-${process.pid}-${cryptoRandomBytes(12).toString("hex")}.json`);
  try {
    await writeFile(path, `${JSON.stringify({ ENROLLMENT_TOKEN_HASH: hash })}\n`, { mode: 0o600, flag: "wx" });
    if ((options.platform ?? process.platform) === "win32") {
      await protectWindowsFile(path);
    } else {
      await chmod(path, 0o600);
    }
  } catch (error) {
    await rm(path, { force: true });
    throw error;
  }
  return { path, cleanup: async () => rm(path, { force: true }) };
}

async function cleanupStaleWorkerSecrets(directory, options = {}) {
  const processExists = options.processExists ?? ((pid) => {
    try {
      process.kill(pid, 0);
      return true;
    } catch (error) {
      return error?.code === "EPERM";
    }
  });
  for (const name of await readdir(directory)) {
    const match = /^\.executor-worker-secret-([1-9][0-9]*)-[0-9a-f]{24}\.json$/u.exec(name);
    if (match === null || processExists(Number(match[1]))) continue;
    const path = resolve(directory, name);
    const info = await lstat(path);
    if (!info.isFile() || info.isSymbolicLink()) {
      throw new Error("A stale Worker secret path is unsafe.");
    }
    await assertProtectedFile(path, options);
    await rm(path);
  }
}

export async function ensureEnrollmentToken(path, options = {}) {
  const platform = options.platform ?? process.platform;
  const rotate = options.rotate === true;
  if (options.enabled === false && !rotate) {
    await assertSafePathAncestors(path, options);
    return { created: false, enabled: false, hash: null };
  }
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
  return { created, enabled: true, hash };
}

export async function prepareEnrollmentDeployment(state, path, options = {}) {
  if (state === null || typeof state !== "object") {
    throw new Error("Dashboard deployment state is invalid.");
  }
  const rotate = options.rotate === true;
  const enabled = rotate || state.enrollment_enabled !== false;
  const enrollment = await ensureEnrollmentToken(path, { ...options, rotate, enabled });
  state.enrollment_token_file = path;
  if (rotate || state.enrollment_enabled === undefined) {
    state.enrollment_enabled = enrollment.enabled;
  }
  return enrollment;
}

export async function prepareEnrollmentRotation(client, state, workerName, path, persist, options = {}) {
  const hadPendingWorker = state.worker_creation_pending === true;
  const pendingOperation = state.worker_pending_operation;
  const pendingRotationHash = state.enrollment_rotation_pending_hash;
  if (hadPendingWorker) {
    const workerPlan = await ensureWorkerOwnership(client, state, workerName, persist, { operation: "rotate-enrollment" });
    const isPendingRotation = pendingOperation === "rotate-enrollment" ||
      (pendingOperation === undefined && typeof pendingRotationHash === "string");
    if (isPendingRotation) {
      if (!/^[0-9a-f]{64}$/u.test(pendingRotationHash)) {
        throw new Error("Pending enrollment rotation state is incomplete; refusing to generate a replacement bearer.");
      }
      const enrollment = await prepareEnrollmentDeployment(state, path, { ...options, rotate: false, enabled: true });
      if (enrollment.hash !== pendingRotationHash) {
        throw new Error("The protected enrollment bearer does not match the pending Worker deployment.");
      }
      return { enrollment, workerPlan, resumed: true };
    }
    if (!workerPlan.skipDeploy) {
      throw new Error("A different pending Worker deployment must be recovered before enrollment rotation.");
    }
  }

  const enrollment = await prepareEnrollmentDeployment(state, path, { ...options, rotate: true });
  state.enrollment_rotation_pending_hash = enrollment.hash;
  await persist();
  const workerPlan = await ensureWorkerOwnership(client, state, workerName, persist, { operation: "rotate-enrollment" });
  return { enrollment, workerPlan, resumed: false };
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
  await ensureOwnedPrivateDirectory(dirname(path), { platform });
  await assertSafePathAncestors(path, { platform });
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

export async function assertSafePathAncestors(path, options = {}) {
  const platform = options.platform ?? process.platform;
  if (platform === "win32") {
    const verifyWindowsPathSafety = options.verifyWindowsPathSafety ??
      (options.verifyWindowsACL === undefined ? verifyWindowsSafePath : async () => true);
    if (!(await verifyWindowsPathSafety(path))) {
      throw new Error("The credential or state path has an unsafe path ancestor.");
    }
    return;
  }
  const absolute = resolve(path);
  const root = parse(absolute).root;
  const ancestors = [];
  for (let current = absolute; current !== root; current = dirname(current)) {
    ancestors.push(current);
  }
  ancestors.push(root);
  ancestors.reverse();
  for (let index = 0; index < ancestors.length; index += 1) {
    const candidate = ancestors[index];
    let info;
    try {
      info = await lstat(candidate);
    } catch (error) {
      if (error?.code === "ENOENT") continue;
      throw new Error("The credential or state path has an unsafe path ancestor.", { cause: error });
    }
    if (info.isSymbolicLink()) {
      if (index < ancestors.length - 1 && await isKnownDarwinSystemSymlink(candidate)) continue;
      throw new Error("The credential or state path has an unsafe path ancestor.");
    }
    if (index < ancestors.length - 1 && !info.isDirectory()) {
      throw new Error("The credential or state path has an unsafe path ancestor.");
    }
  }
}

async function isKnownDarwinSystemSymlink(path) {
  if (process.platform !== "darwin") return false;
  const expected = new Map([
    ["/etc", "private/etc"],
    ["/tmp", "private/tmp"],
    ["/var", "private/var"],
  ]).get(path);
  if (expected === undefined) return false;
  try {
    return await readlink(path) === expected;
  } catch {
    return false;
  }
}

async function ensureOwnedPrivateDirectory(path, options = {}) {
  const platform = options.platform ?? process.platform;
  await assertSafePathAncestors(path, options);
  let created = false;
  try {
    const info = await lstat(path);
    if (!info.isDirectory() || info.isSymbolicLink()) {
      throw new Error("The Executor state directory is unsafe.");
    }
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
    await mkdir(path, { recursive: true, mode: 0o700 });
    created = true;
  }
  await assertSafePathAncestors(path, options);
  const marker = resolve(path, ownedDirectoryMarker);
  if (created) {
    await writeFile(marker, `${deploymentOwner}\n`, { mode: 0o600, flag: "wx" });
    if (platform === "win32") {
      await protectWindowsPath(path, true);
      await protectWindowsFile(marker);
    } else {
      await chmod(path, 0o700);
      await chmod(marker, 0o600);
    }
    return;
  }
  await assertProtectedFile(marker, options);
  let owner;
  try {
    owner = (await readFile(marker, "utf8")).trim();
  } catch {
    throw new Error("The Executor state directory ownership cannot be proven.");
  }
  if (owner !== deploymentOwner) {
    throw new Error("The Executor state directory ownership cannot be proven.");
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
    const envelope = await this.request(`accounts/${this.accountID}/workers/scripts`);
    if (!Array.isArray(envelope.result)) {
      throw new Error("Cloudflare API returned an invalid Worker list response.");
    }
    return envelope.result;
  }

  async listWorkerDeployments(workerName) {
    const envelope = await this.request(`accounts/${this.accountID}/workers/scripts/${encodeURIComponent(workerName)}/deployments`);
    if (envelope.result === null || typeof envelope.result !== "object" || !Array.isArray(envelope.result.deployments)) {
      throw new Error("Cloudflare API returned invalid Worker deployment metadata.");
    }
    return envelope.result.deployments;
  }

  async queryD1(databaseID, sql) {
    const envelope = await this.request(`accounts/${this.accountID}/d1/database/${encodeURIComponent(databaseID)}/query`, {
      method: "POST",
      body: { sql },
    });
    if (!Array.isArray(envelope.result) || envelope.result.length !== 1 ||
        envelope.result[0]?.success !== true || !Array.isArray(envelope.result[0]?.results)) {
      throw new Error("Cloudflare API returned invalid D1 query metadata.");
    }
    return envelope.result[0].results;
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
  if (state.d1_creation_pending === true) {
    if (state.d1_database_name !== name) {
      throw new Error("Pending D1 creation names an incompatible database.");
    }
    const pendingUUID = state.d1_pending_create_response_uuid;
    if (typeof pendingUUID !== "string" || pendingUUID === "") {
      throw new Error("Pending D1 creation has no persisted create-response UUID; refusing automatic adoption. Verify ownership in Cloudflare and recover the protected deployment state manually.");
    }
    const created = exact.find((database) => database?.uuid === pendingUUID);
    if (created === undefined) {
      throw new Error("Pending D1 creation does not match the exact create-response UUID; refusing automatic adoption. Verify ownership in Cloudflare and recover the protected deployment state manually.");
    }
    state.d1_database_id = pendingUUID;
    state.d1_owned_by_executor = true;
    state.d1_creation_pending = false;
    delete state.d1_pending_create_response_uuid;
    await persist();
    return pendingUUID;
  }
  if (exact.length === 1) {
    state.d1_database_name = name;
    state.d1_database_id = exact[0].uuid;
    state.d1_owned_by_executor = false;
    state.d1_creation_pending = false;
    await persist();
    return exact[0].uuid;
  }
  state.d1_database_name = name;
  state.d1_owned_by_executor = false;
  state.d1_creation_pending = true;
  await persist();
  const created = await client.createD1Database(name);
  if (created?.name !== name || typeof created?.uuid !== "string" || created.uuid === "") {
    throw new Error("Cloudflare returned invalid D1 creation metadata.");
  }
  state.d1_pending_create_response_uuid = created.uuid;
  await persist();
  state.d1_database_id = created.uuid;
  state.d1_owned_by_executor = true;
  state.d1_creation_pending = false;
  delete state.d1_pending_create_response_uuid;
  await persist();
  return created.uuid;
}

export async function ensureWorkerOwnership(client, state, workerName, persist, options = {}) {
  const workers = await client.listWorkerScripts();
  const exists = workers.some((worker) => worker?.id === workerName || worker?.name === workerName);
  if (state.worker_name !== undefined && state.worker_name !== workerName) {
    throw new Error("Deployment state names an incompatible Worker.");
  }
  if (state.worker_creation_pending === true) {
    const marker = state.worker_pending_marker;
    if (typeof marker !== "string" || marker === "") {
      throw new Error("Pending Worker ownership cannot be proven.");
    }
    if (!exists) {
      if (state.worker_pending_previous_deployment_id !== undefined) {
        throw new Error("The state-owned Worker is missing or has been replaced.");
      }
      return { marker, skipDeploy: false, workerExists: false };
    }
    const current = await currentWorkerDeployment(client, workerName);
    if (current.id === state.worker_pending_previous_deployment_id) {
      return { marker, skipDeploy: false, workerExists: true };
    }
    if (deploymentMessage(current) !== `Executor deployment ${marker}`) {
      throw new Error("Pending Worker ownership cannot be proven from the deployment marker.");
    }
    recordDeploymentIdentity(state, current);
    await persist();
    return { marker, skipDeploy: true, workerExists: true };
  }
  if (state.worker_owned_by_executor === true) {
    if (!exists) {
      throw new Error("The state-owned Worker is missing or has been replaced.");
    }
    const current = await assertWorkerRemoteIdentity(client, state, workerName);
    const marker = options.marker ?? cryptoRandomBytes(16).toString("hex");
    state.worker_creation_pending = true;
    state.worker_pending_marker = marker;
    state.worker_pending_previous_deployment_id = current.id;
    if (typeof options.operation === "string" && options.operation !== "") {
      state.worker_pending_operation = options.operation;
    }
    await persist();
    return { marker, skipDeploy: false, workerExists: true };
  }
  if (exists) {
    throw new Error("A Worker with the requested name exists but is not owned by Executor state.");
  }
  const marker = options.marker ?? cryptoRandomBytes(16).toString("hex");
  state.worker_name = workerName;
  state.worker_creation_pending = true;
  state.worker_pending_marker = marker;
  if (typeof options.operation === "string" && options.operation !== "") {
    state.worker_pending_operation = options.operation;
  }
  await persist();
  return { marker, skipDeploy: false, workerExists: false };
}

export async function assertWorkerRemoteIdentity(client, state, workerName) {
  if (state.worker_owned_by_executor !== true || state.worker_name !== workerName ||
      typeof state.worker_deployment_id !== "string" || !Array.isArray(state.worker_version_ids)) {
    throw new Error("Deployment state does not prove Executor ownership of the Worker.");
  }
  const current = await currentWorkerDeployment(client, workerName);
  const currentVersions = deploymentVersionIDs(current);
  if (current.id !== state.worker_deployment_id ||
      JSON.stringify(currentVersions) !== JSON.stringify(state.worker_version_ids)) {
    throw new Error("The current Worker remote identity has drifted or been replaced.");
  }
  return current;
}

export function assertOwnedRollbackVersion(state, versionID) {
  if (state?.worker_owned_by_executor !== true || typeof versionID !== "string" || versionID === "" ||
      !Array.isArray(state.worker_owned_version_ids) || !state.worker_owned_version_ids.includes(versionID)) {
    throw new Error("Rollback requires an explicitly recorded Executor-owned Worker version.");
  }
}

export async function recordWorkerDeployment(client, state, workerName, persist) {
  if (state.worker_creation_pending !== true || typeof state.worker_pending_marker !== "string") {
    throw new Error("Pending Worker deployment state is unavailable.");
  }
  const current = await currentWorkerDeployment(client, workerName);
  if (deploymentMessage(current) !== `Executor deployment ${state.worker_pending_marker}`) {
    throw new Error("Cloudflare Worker deployment marker does not prove ownership.");
  }
  recordDeploymentIdentity(state, current);
  await persist();
}

function recordDeploymentIdentity(state, deployment) {
  const versionIDs = deploymentVersionIDs(deployment);
  state.worker_owned_by_executor = true;
  state.worker_deployment_id = deployment.id;
  state.worker_version_ids = versionIDs;
  state.worker_owned_deployment_ids = uniqueValues([...(state.worker_owned_deployment_ids ?? []), deployment.id]);
  state.worker_owned_version_ids = uniqueValues([...(state.worker_owned_version_ids ?? []), ...versionIDs]);
  state.worker_creation_pending = false;
  delete state.worker_pending_marker;
  delete state.worker_pending_previous_deployment_id;
  delete state.worker_pending_operation;
}

async function currentWorkerDeployment(client, workerName) {
  const deployments = await client.listWorkerDeployments(workerName);
  if (!Array.isArray(deployments) || deployments.length === 0) {
    throw new Error("Cloudflare returned no current Worker deployment identity.");
  }
  const current = deployments[0];
  if (typeof current?.id !== "string" || current.id === "" || typeof current?.created_on !== "string" ||
      typeof current?.source !== "string" || current.strategy !== "percentage" || deploymentVersionIDs(current).length === 0) {
    throw new Error("Cloudflare returned invalid Worker deployment metadata.");
  }
  return current;
}

function deploymentVersionIDs(deployment) {
  if (!Array.isArray(deployment?.versions)) return [];
  const values = deployment.versions.map((version) => version?.version_id);
  return values.every((value) => typeof value === "string" && value !== "") ? values : [];
}

function deploymentMessage(deployment) {
  return deployment?.annotations?.["workers/message"];
}

function uniqueValues(values) {
  return [...new Set(values)];
}

export async function assertRemoteMigrationsCompatible(client, state, localNames) {
  if (typeof state?.d1_database_id !== "string" || state.d1_database_id === "" ||
      !Array.isArray(localNames) || localNames.length === 0) {
    throw new Error("D1 migration compatibility inputs are invalid.");
  }
  const tables = await client.queryD1(
    state.d1_database_id,
    "SELECT name FROM sqlite_schema WHERE type = 'table' AND name = 'd1_migrations'",
  );
  if (tables.length === 0) {
    if (state.d1_owned_by_executor === true) return [];
    throw new Error("The existing D1 database has no Wrangler migration table.");
  }
  if (tables.length !== 1 || tables[0]?.name !== "d1_migrations") {
    throw new Error("Cloudflare returned invalid D1 migration table metadata.");
  }
  const rows = await client.queryD1(state.d1_database_id, "SELECT name FROM d1_migrations ORDER BY id ASC");
  const remoteNames = rows.map((row) => row?.name);
  if (!remoteNames.every((name) => typeof name === "string" && name !== "")) {
    throw new Error("Cloudflare returned invalid D1 migration metadata.");
  }
  if (remoteNames.length > localNames.length) {
    throw new Error("The remote D1 database contains a newer or unknown migration.");
  }
  for (let index = 0; index < remoteNames.length; index += 1) {
    if (remoteNames[index] !== localNames[index]) {
      throw new Error("The remote D1 migration order is divergent.");
    }
  }
  return remoteNames;
}

export async function ensureAccessResources(client, state, values, persist) {
  const applications = await client.listAccessApplications();
  const deviceDomain = `${values.hostname}/api/device/*`;
  const dashboardPathPrefix = `${values.hostname}/`;
  const ownedApplicationIDs = new Set([
    state.access_application_id,
    state.device_access_application_id,
  ].filter((value) => typeof value === "string"));
  const conflictingDeviceApplication = applications.find((application) => {
    const domain = typeof application?.domain === "string" ? application.domain.toLowerCase() : "";
    return !ownedApplicationIDs.has(application?.id) && domain !== deviceDomain && domain.startsWith(dashboardPathPrefix);
  });
  if (conflictingDeviceApplication !== undefined) {
    throw new Error("A conflicting unowned Access application overrides Dashboard or device path protection.");
  }
  const owner = await ensureAccessResource(client, applications, state, {
    applicationName: accessApplicationName,
    applicationDomain: values.hostname,
    applicationIDKey: "access_application_id",
    applicationAUDKey: "access_application_aud",
    applicationPendingKey: "access_application_creation_pending",
    policyName: accessPolicyName,
    policyIDKey: "access_policy_id",
    policyPendingKey: "access_policy_creation_pending",
    policyPayload: {
      name: accessPolicyName,
      decision: "allow",
      precedence: 1,
      include: [{ email: { email: values.allowedEmail } }],
    },
  }, persist);
  await ensureAccessResource(client, applications, state, {
    applicationName: deviceAccessApplicationName,
    applicationDomain: deviceDomain,
    applicationIDKey: "device_access_application_id",
    applicationAUDKey: "device_access_application_aud",
    applicationPendingKey: "device_access_application_creation_pending",
    policyName: deviceAccessPolicyName,
    policyIDKey: "device_access_policy_id",
    policyPendingKey: "device_access_policy_creation_pending",
    policyPayload: {
      name: deviceAccessPolicyName,
      decision: "bypass",
      precedence: 1,
      include: [{ everyone: {} }],
    },
  }, persist);
  state.access_owned_by_executor = true;
  state.allowed_email = values.allowedEmail;
  await persist();
  return { applicationID: owner.id, audience: owner.aud };
}

async function ensureAccessResource(client, applications, state, spec, persist) {
  const applicationPayload = {
    name: spec.applicationName,
    type: "self_hosted",
    domain: spec.applicationDomain,
    session_duration: "24h",
    app_launcher_visible: false,
  };
  let application;
  const ownedApplicationID = state[spec.applicationIDKey];
  const collisions = applications.filter((candidate) =>
    candidate?.domain === spec.applicationDomain || candidate?.name === spec.applicationName);
  if (ownedApplicationID !== undefined) {
    if (collisions.length !== 1 || collisions[0]?.id !== ownedApplicationID) {
      throw new Error("An extra or unowned Access application is effective for the protected path.");
    }
    application = applications.find((candidate) => candidate?.id === ownedApplicationID);
    if (!validOwnedApplication(application, spec.applicationName, spec.applicationDomain)) {
      throw new Error("The state-owned Access application is missing or has been replaced.");
    }
    application = await client.updateAccessApplication(application.id, applicationPayload);
  } else if (state[spec.applicationPendingKey] === true) {
    const pending = applications.filter((candidate) => validOwnedApplication(candidate, spec.applicationName, spec.applicationDomain));
    if (pending.length !== 1) {
      throw new Error("Pending Access application ownership cannot be proven.");
    }
    application = pending[0];
  } else {
    if (collisions.length !== 0) {
      throw new Error("An Access application for the hostname exists but is not owned by Executor state.");
    }
    state[spec.applicationPendingKey] = true;
    await persist();
    application = await client.createAccessApplication(applicationPayload);
  }
  if (!validOwnedApplication(application, spec.applicationName, spec.applicationDomain) || typeof application.aud !== "string" || application.aud === "") {
    throw new Error("Cloudflare returned invalid Access application metadata.");
  }
  state[spec.applicationIDKey] = application.id;
  state[spec.applicationAUDKey] = application.aud;
  state[spec.applicationPendingKey] = false;
  await persist();

  const policies = await client.listAccessPolicies(application.id);
  let policy;
  const ownedPolicyID = state[spec.policyIDKey];
  if (ownedPolicyID !== undefined) {
    if (policies.length !== 1 || policies[0]?.id !== ownedPolicyID) {
      throw new Error("An extra or unowned Access policy is effective for the protected path.");
    }
    policy = policies[0];
    if (!validPolicyIdentity(policy, spec.policyName)) {
      throw new Error("The state-owned Access policy is missing or has been replaced.");
    }
    policy = await client.updateAccessPolicy(application.id, policy.id, spec.policyPayload);
  } else if (state[spec.policyPendingKey] === true) {
    const pending = policies.filter((candidate) => validPolicyIdentity(candidate, spec.policyName));
    if (pending.length !== 1 || policies.length !== 1) {
      throw new Error("Pending Access policy ownership cannot be proven.");
    }
    policy = await client.updateAccessPolicy(application.id, pending[0].id, spec.policyPayload);
  } else {
    if (policies.length !== 0) {
      throw new Error("An extra or unowned Access policy is effective for the protected path.");
    }
    state[spec.policyPendingKey] = true;
    await persist();
    policy = await client.createAccessPolicy(application.id, spec.policyPayload);
  }
  if (!validExactPolicy(policy, spec.policyPayload)) {
    throw new Error("Cloudflare returned invalid Access policy metadata.");
  }
  state[spec.policyIDKey] = policy.id;
  state[spec.policyPendingKey] = false;
  await persist();
  return application;
}

function validOwnedApplication(application, name, domain) {
  return application !== undefined &&
    application?.name === name &&
    application?.domain === domain &&
    application?.type === "self_hosted" &&
    typeof application?.id === "string" &&
    application.id !== "";
}

function validPolicyIdentity(policy, name) {
  return typeof policy?.id === "string" && policy.id !== "" && policy?.name === name;
}

function validExactPolicy(policy, payload) {
  return validPolicyIdentity(policy, payload.name) && policy.decision === payload.decision &&
    Number(policy.precedence) === payload.precedence && JSON.stringify(policy.include) === JSON.stringify(payload.include) &&
    (policy.exclude === undefined || policy.exclude.length === 0) &&
    (policy.require === undefined || policy.require.length === 0);
}

async function verifyWindowsProtectedFile(path) {
  return runPowerShellProtection(path, false);
}

async function verifyWindowsSafePath(path) {
  return runPowerShellProtection(path, false, ["-AncestorsOnly"]);
}

async function protectWindowsFile(path) {
  if (!(await runPowerShellProtection(path, true))) {
    throw new Error("Unable to protect a Windows credential or state file.");
  }
}

async function protectWindowsPath(path, directory) {
  if (!(await runPowerShellProtection(path, true, directory ? ["-Directory"] : []))) {
    throw new Error("Unable to protect a Windows credential or state path.");
  }
}

async function runPowerShellProtection(path, initialize, extraArguments = []) {
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
    argumentsList.push(...extraArguments);
    const child = spawn("powershell.exe", argumentsList, { stdio: "ignore", windowsHide: true });
    child.once("error", () => resolvePromise(false));
    child.once("exit", (code) => resolvePromise(code === 0));
  });
}
