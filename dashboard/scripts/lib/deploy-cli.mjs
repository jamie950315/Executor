import { homedir } from "node:os";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";

const commands = new Set(["deploy", "validate", "rotate-enrollment", "disable-enrollment", "rollback"]);

const optionMap = new Map([
  ["--hostname", "hostname"],
  ["--account-id", "accountID"],
  ["--api-token-file", "apiTokenFile"],
  ["--allowed-email", "allowedEmail"],
  ["--state-file", "stateFile"],
  ["--enrollment-token-file", "enrollmentTokenFile"],
  ["--version-id", "versionID"],
]);

export function parseDeploymentArguments(argumentsList) {
  const args = [...argumentsList];
  const command = args.shift();
  if (!commands.has(command)) {
    throw new Error("Expected a Dashboard deployment command: deploy, validate, rotate-enrollment, disable-enrollment, or rollback.");
  }
  const result = { command };
  while (args.length > 0) {
    const option = args.shift();
    const property = optionMap.get(option);
    if (property === undefined) {
      throw new Error(`Unknown option: ${String(option)}`);
    }
    const value = args.shift();
    if (value === undefined || value.startsWith("--")) {
      throw new Error(`Missing value for ${option}.`);
    }
    if (result[property] !== undefined) {
      throw new Error(`Duplicate option: ${option}.`);
    }
    result[property] = value;
  }
  return result;
}

export function normalizeDeploymentOptions(input, runtime = {}) {
  const environment = runtime.environment ?? process.env;
  const platform = runtime.platform ?? process.platform;
  const homeDirectory = runtime.homeDirectory ?? homedir();
  const dashboardRoot = resolve(runtime.dashboardRoot ?? process.cwd());
  const repositoryRoot = dirname(dashboardRoot);
  const command = input.command;
  if (!commands.has(command)) {
    throw new Error("Invalid Dashboard deployment command.");
  }
  const hostname = (input.hostname ?? environment.EXECUTOR_DASHBOARD_HOSTNAME ?? "").trim().toLowerCase();
  const accountID = (input.accountID ?? environment.CLOUDFLARE_ACCOUNT_ID ?? "").trim();
  const apiTokenFile = input.apiTokenFile ?? environment.CLOUDFLARE_API_TOKEN_FILE ?? "";
  const allowedEmail = (input.allowedEmail ?? environment.EXECUTOR_DASHBOARD_ALLOWED_EMAIL ?? "").trim().toLowerCase();
  if (!validHostname(hostname)) {
    throw new Error("A valid Dashboard hostname is required without a scheme, port, path, or wildcard.");
  }
  if (!/^[0-9a-f]{32}$/iu.test(accountID)) {
    throw new Error("A valid 32-character Cloudflare account ID is required.");
  }
  if (!isAbsolute(apiTokenFile)) {
    throw new Error("The Cloudflare API token file path must be absolute.");
  }
  if (!validEmail(allowedEmail)) {
    throw new Error("One valid Cloudflare Access email is required.");
  }

  const defaultStateDirectory = platform === "win32"
    ? join(environment.LOCALAPPDATA || join(homeDirectory, "AppData", "Local"), "Executor")
    : join(environment.XDG_STATE_HOME || join(homeDirectory, ".local", "state"), "executor");
  const stateFile = resolve(input.stateFile ?? environment.EXECUTOR_DASHBOARD_STATE_FILE ?? join(defaultStateDirectory, "dashboard-deployment.json"));
  const enrollmentTokenFile = resolve(input.enrollmentTokenFile ?? environment.EXECUTOR_DASHBOARD_ENROLLMENT_TOKEN_FILE ?? join(defaultStateDirectory, "dashboard-enrollment.token"));
  for (const path of [resolve(apiTokenFile), stateFile, enrollmentTokenFile]) {
    if (inside(repositoryRoot, path, platform)) {
      throw new Error("Credential and deployment state files must remain outside the repository.");
    }
  }
  if (stateFile === enrollmentTokenFile || resolve(apiTokenFile) === enrollmentTokenFile || resolve(apiTokenFile) === stateFile) {
    throw new Error("Cloudflare token, enrollment token, and deployment state must use separate files.");
  }
  if (command === "rollback" && input.versionID !== undefined && !/^[A-Za-z0-9_-]{8,128}$/u.test(input.versionID)) {
    throw new Error("The Worker rollback version ID is invalid.");
  }
  return {
    command,
    hostname,
    accountID,
    apiTokenFile: resolve(apiTokenFile),
    allowedEmail,
    stateFile,
    enrollmentTokenFile,
    versionID: input.versionID,
    dashboardRoot,
    repositoryRoot,
    platform,
  };
}

function validHostname(value) {
  return value.length <= 253 &&
    value.includes(".") &&
    !value.includes("..") &&
    /^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/u.test(value) &&
    value.split(".").every((label) => label.length >= 1 && label.length <= 63 && !label.startsWith("-") && !label.endsWith("-"));
}

function validEmail(value) {
  return value.length <= 254 && /^[^\s@]+@[^\s@]+\.[^\s@]+$/u.test(value);
}

function inside(parent, child, platform) {
  let parentPath = resolve(parent);
  let childPath = resolve(child);
  if (platform === "win32") {
    parentPath = parentPath.toLowerCase();
    childPath = childPath.toLowerCase();
  }
  const difference = relative(parentPath, childPath);
  return difference === "" || (!difference.startsWith(`..${sep}`) && difference !== "..");
}
