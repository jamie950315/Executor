#!/usr/bin/env node
import { randomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import {
  lstat,
  readFile,
  readdir,
} from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  CloudflareClient,
  assertMigrationCompatible,
  assertProtectedFile,
  assertSupportedNodeVersion,
  assertWranglerV4,
  dashboardD1Name,
  dashboardWorkerName,
  deploymentOwner,
  deploymentStateVersion,
  ensureAccessResources,
  ensureD1Database,
  ensureEnrollmentToken,
  ensureWorkerOwnership,
  loadDeploymentState,
  renderWranglerConfig,
  removeProtectedFileIfOwned,
  saveDeploymentState,
  writeTemporaryWranglerConfig,
} from "./lib/deploy-core.mjs";
import { normalizeDeploymentOptions, parseDeploymentArguments } from "./lib/deploy-cli.mjs";

const dashboardRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const wranglerPath = join(dashboardRoot, "node_modules", "wrangler", "bin", "wrangler.js");
const templatePath = join(dashboardRoot, "wrangler.deploy.template.jsonc");
const npmBinary = process.platform === "win32" ? "npm.cmd" : "npm";
const maximumCapturedOutput = 1024 * 1024;

let stage = "argument validation";

try {
  const parsed = parseDeploymentArguments(process.argv.slice(2));
  const options = normalizeDeploymentOptions(parsed, { dashboardRoot });
  await main(options);
} catch (error) {
  process.stderr.write(`Dashboard deployment stopped at ${stage}: ${safeMessage(error)}\n`);
  process.exitCode = 1;
}

async function main(options) {
  stage = "local prerequisites";
  assertSupportedNodeVersion();
  await assertProtectedFile(options.apiTokenFile, { platform: options.platform });
  await assertPackagePayload();
  await run(npmBinary, ["--version"], { cwd: dashboardRoot });

  stage = "lockfile installation";
  await run(npmBinary, ["ci"], { cwd: dashboardRoot });
  const wranglerVersion = assertWranglerV4((await run(process.execPath, [wranglerPath, "--version"], { cwd: dashboardRoot })).stdout);

  stage = "Dashboard package validation";
  await run(npmBinary, ["test"], { cwd: dashboardRoot });
  await run(npmBinary, ["run", "check"], { cwd: dashboardRoot });
  await run(npmBinary, ["run", "build"], { cwd: dashboardRoot });
  const template = await readFile(templatePath, "utf8");
  const localConfig = renderWranglerConfig(template, {
    accountID: options.accountID,
    databaseID: "00000000-0000-0000-0000-000000000001",
    hostname: options.hostname,
    accessAUD: "local-validation-audience",
    accessTeamDomain: "local-validation.cloudflareaccess.com",
  });
  const localTemporary = await writeTemporaryWranglerConfig(dashboardRoot, localConfig);
  try {
    await run(process.execPath, [wranglerPath, "deploy", "--dry-run", "--config", localTemporary.configPath, "--outdir", "dist/worker-validation"], {
      cwd: dashboardRoot,
    });
  } finally {
    await localTemporary.cleanup();
  }
  if (options.command === "validate") {
    process.stdout.write(`Dashboard source package validated with Wrangler ${wranglerVersion}; no Cloudflare changes were made.\n`);
    return;
  }

  stage = "credential loading";
  const apiToken = await readCredential(options.apiTokenFile);
  try {
    const wranglerEnvironment = {
      ...process.env,
      CI: "1",
      CLOUDFLARE_ACCOUNT_ID: options.accountID,
      CLOUDFLARE_API_TOKEN: apiToken.toString("utf8"),
    };
    stage = "Cloudflare authentication";
    await run(process.execPath, [wranglerPath, "whoami", "--account", options.accountID, "--json"], {
      cwd: dashboardRoot,
      environment: wranglerEnvironment,
    });
    const client = new CloudflareClient({ accountID: options.accountID, token: apiToken });
    await dispatchRemoteCommand(options, client, wranglerEnvironment, template);
  } finally {
    apiToken.fill(0);
  }
}

async function dispatchRemoteCommand(options, client, wranglerEnvironment, template) {
  let state = await loadDeploymentState(options.stateFile, {
    accountID: options.accountID,
    hostname: options.hostname,
    platform: options.platform,
  });
  if (state === null) {
    if (options.command !== "deploy") {
      throw new Error("No Executor-owned Dashboard deployment state exists for this command.");
    }
    state = {
      schema_version: deploymentStateVersion,
      owner: deploymentOwner,
      account_id: options.accountID,
      hostname: options.hostname,
      allowed_email: options.allowedEmail,
      enrollment_token_file: options.enrollmentTokenFile,
      last_completed_stage: "authenticated",
    };
    await persistState(options, state);
  }
  if (state.enrollment_token_file !== options.enrollmentTokenFile) {
    throw new Error("Deployment state names a different enrollment token file.");
  }
  const localMigrationVersion = await migrationVersion();
  assertMigrationCompatible(state.dashboard_migration_version ?? 0, localMigrationVersion);

  stage = "Cloudflare Access organization lookup";
  const organization = await client.getOrganization();
  if (!validTeamDomain(organization?.auth_domain)) {
    throw new Error("Cloudflare Zero Trust organization state has no valid Access team domain.");
  }
  state.access_team_domain = organization.auth_domain.toLowerCase();
  await persistState(options, state);

  if (options.command === "deploy") {
    await deploy(options, client, wranglerEnvironment, template, state, localMigrationVersion);
    return;
  }

  const temporary = await renderTemporaryConfig(options, template, state);
  try {
    if (options.command === "rotate-enrollment") {
      stage = "enrollment rotation";
      const enrollment = await ensureEnrollmentToken(options.enrollmentTokenFile, { platform: options.platform, rotate: true });
      await putEnrollmentHash(temporary.configPath, enrollment.hash, wranglerEnvironment);
      state.enrollment_enabled = true;
      state.last_completed_stage = "enrollment-rotated";
      await persistState(options, state);
      process.stdout.write(`Enrollment bearer rotated. Protected file: ${options.enrollmentTokenFile}\n`);
      return;
    }
    if (options.command === "disable-enrollment") {
      stage = "enrollment disable";
      const unavailableBearer = randomBytes(32);
      const unavailableHash = (await import("node:crypto")).createHash("sha256").update(unavailableBearer).digest("hex");
      unavailableBearer.fill(0);
      await putEnrollmentHash(temporary.configPath, unavailableHash, wranglerEnvironment);
      await removeProtectedFileIfOwned(options.enrollmentTokenFile, state.enrollment_token_file, { platform: options.platform });
      state.enrollment_enabled = false;
      state.last_completed_stage = "enrollment-disabled";
      await persistState(options, state);
      process.stdout.write("Dashboard enrollment disabled; existing enrolled devices remain configured.\n");
      return;
    }
    if (options.command === "rollback") {
      if (state.worker_owned_by_executor !== true || state.worker_name !== dashboardWorkerName) {
        throw new Error("Deployment state does not prove ownership of the Worker; rollback refused.");
      }
      stage = "Worker rollback";
      const args = [wranglerPath, "rollback"];
      if (options.versionID !== undefined) {
        args.push(options.versionID);
      }
      args.push("--name", dashboardWorkerName, "--config", temporary.configPath, "--yes", "--message", "Executor owner-requested rollback");
      await run(process.execPath, args, { cwd: dashboardRoot, environment: wranglerEnvironment });
      state.last_completed_stage = "worker-rollback";
      await persistState(options, state);
      process.stdout.write(`Executor-owned Dashboard Worker rolled back for ${options.hostname}.\n`);
    }
  } finally {
    await temporary.cleanup();
  }
}

async function deploy(options, client, wranglerEnvironment, template, state, localMigrationVersion) {
  const persist = async () => persistState(options, state);
  stage = "D1 reconciliation";
  await ensureD1Database(client, state, dashboardD1Name, persist);
  state.last_completed_stage = "d1-ready";
  await persist();

  stage = "Worker ownership check";
  await ensureWorkerOwnership(client, state, dashboardWorkerName, persist);

  stage = "Cloudflare Access reconciliation";
  await ensureAccessResources(client, state, { hostname: options.hostname, allowedEmail: options.allowedEmail }, persist);
  state.last_completed_stage = "access-ready";
  await persist();

  stage = "enrollment bearer preparation";
  const enrollment = await ensureEnrollmentToken(options.enrollmentTokenFile, { platform: options.platform });
  state.enrollment_token_file = options.enrollmentTokenFile;
  state.enrollment_enabled = true;
  await persist();

  const temporary = await renderTemporaryConfig(options, template, state);
  try {
    stage = "D1 migrations";
    await run(process.execPath, [wranglerPath, "d1", "migrations", "apply", "DB", "--remote", "--config", temporary.configPath], {
      cwd: dashboardRoot,
      environment: wranglerEnvironment,
    });
    state.dashboard_migration_version = localMigrationVersion;
    state.last_completed_stage = "migrations-applied";
    await persist();

    stage = "Worker and Static Assets deployment";
    await run(process.execPath, [wranglerPath, "deploy", "--config", temporary.configPath, "--keep-vars", "--strict"], {
      cwd: dashboardRoot,
      environment: wranglerEnvironment,
    });
    state.worker_creation_pending = false;
    state.last_completed_stage = "worker-deployed";
    await persist();

    stage = "enrollment hash secret";
    await putEnrollmentHash(temporary.configPath, enrollment.hash, wranglerEnvironment);
    state.last_completed_stage = "complete";
    await persist();
  } finally {
    await temporary.cleanup();
  }
  process.stdout.write([
    `Unified Dashboard deployed: https://${options.hostname}`,
    `Worker: ${dashboardWorkerName}`,
    `D1 database: ${state.d1_database_name} (${state.d1_database_id})`,
    `Access application: ${state.access_application_id}`,
    `Enrollment bearer file (protected; value not shown): ${options.enrollmentTokenFile}`,
    `Deployment state: ${options.stateFile}`,
    "No existing per-device MCP hostname, OAuth configuration, or Tunnel was changed.",
    "",
  ].join("\n"));
}

async function putEnrollmentHash(configPath, hash, environment) {
  await run(process.execPath, [wranglerPath, "secret", "put", "ENROLLMENT_TOKEN_HASH", "--config", configPath], {
    cwd: dashboardRoot,
    environment,
    input: `${hash}\n`,
  });
}

async function renderTemporaryConfig(options, template, state) {
  if (
    typeof state.d1_database_id !== "string" ||
    typeof state.access_application_aud !== "string" ||
    !validTeamDomain(state.access_team_domain)
  ) {
    throw new Error("Deployment state is incomplete for this operation.");
  }
  const rendered = renderWranglerConfig(template, {
    accountID: options.accountID,
    databaseID: state.d1_database_id,
    hostname: options.hostname,
    accessAUD: state.access_application_aud,
    accessTeamDomain: state.access_team_domain,
  });
  return writeTemporaryWranglerConfig(dashboardRoot, rendered);
}

async function persistState(options, state) {
  state.updated_at = new Date().toISOString();
  await saveDeploymentState(options.stateFile, state, { platform: options.platform });
}

async function migrationVersion() {
  const names = (await readdir(join(dashboardRoot, "migrations")))
    .filter((name) => /^\d{4}_[a-z0-9_]+\.sql$/u.test(name))
    .sort();
  if (names.length === 0 || names.some((name, index) => Number(name.slice(0, 4)) !== index + 1)) {
    throw new Error("Dashboard migration files are missing or non-sequential.");
  }
  return names.length;
}

async function assertPackagePayload() {
  for (const path of [
    join(dashboardRoot, "package.json"),
    join(dashboardRoot, "package-lock.json"),
    templatePath,
    join(dashboardRoot, "src", "index.ts"),
    join(dashboardRoot, "migrations", "0001_control_plane.sql"),
  ]) {
    try {
      const info = await lstat(path);
      if (!info.isFile()) {
        throw new Error("not a file");
      }
    } catch {
      throw new Error("The Dashboard source deployment payload is incomplete.");
    }
  }
}

async function readCredential(path) {
  const info = await lstat(path);
  if (info.size < 1 || info.size > 4096) {
    throw new Error("The Cloudflare API token file is invalid.");
  }
  const data = await readFile(path);
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
}

function validTeamDomain(value) {
  return typeof value === "string" && value.length <= 253 && /^[A-Za-z0-9.-]+$/u.test(value) && !value.includes("..");
}

function safeMessage(error) {
  if (!(error instanceof Error) || error.message.length === 0 || error.message.length > 512) {
    return "operation failed safely";
  }
  return error.message.replace(/[\r\n]/gu, " ");
}

function run(command, args, options = {}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.environment ?? process.env,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
    const stdout = [];
    const stderr = [];
    let outputBytes = 0;
    const capture = (target) => (chunk) => {
      outputBytes += chunk.length;
      if (outputBytes <= maximumCapturedOutput) target.push(Buffer.from(chunk));
    };
    child.stdout.on("data", capture(stdout));
    child.stderr.on("data", capture(stderr));
    child.once("error", () => rejectPromise(new Error(`Required command is unavailable: ${command}.`)));
    child.once("exit", (code) => {
      if (code !== 0) {
        rejectPromise(new Error(`Required command failed with exit code ${String(code)}.`));
        return;
      }
      resolvePromise({ stdout: Buffer.concat(stdout).toString("utf8"), stderr: Buffer.concat(stderr).toString("utf8") });
    });
    child.stdin.end(options.input);
  });
}
