// Isolated real-HTTP regression harness. Binds only loopback; uses ephemeral test
// identities/D1 state and optionally reads its own fixture through installed Beta.
import assert from "node:assert/strict";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { setTimeout as delay } from "node:timers/promises";
import { build } from "esbuild";
import { convertV4MiniflareOptions, Log, LogLevel, Miniflare } from "miniflare";
import { exportJWK, generateKeyPair, SignJWT } from "jose";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const option = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const source = resolve(option("--source") ?? root);
const baseline = args.includes("--baseline");
const iterations = Number(option("--iterations") ?? "100");
assert(Number.isInteger(iterations) && iterations > 0 && iterations <= 1000);
const betaBinary = option("--beta-binary");
const betaConfig = option("--beta-config");
assert(Boolean(betaBinary) === Boolean(betaConfig), "Specify both Beta binary and config");
const work = await mkdtemp(join(tmpdir(), "executor-http-regression-"));
const fixture = join(work, "fixture.txt");
const empty = join(work, "empty.txt");
const data = Buffer.from("繁體中文🙂 relay HTTP test\n".repeat(6000));
await writeFile(fixture, data, { mode: 0o600 });
await writeFile(empty, "", { mode: 0o600 });
const digest = (value) => createHash("sha256").update(value).digest("hex");
const deviceID = `http-fixture-${randomUUID()}`;
const subject = "isolated-http-test-user";
const enrollmentToken = randomBytes(32).toString("base64url");
const accessKey = await generateKeyPair("RS256", { extractable: true });
const deviceKey = await generateKeyPair("ES256", { extractable: true });
const publicKey = await exportJWK(deviceKey.publicKey);
const accessPublicKey = { ...await exportJWK(accessKey.publicKey), alg: "RS256", kid: "http-test" };
let runtime;
let beta;
const peers = [];
const pendingRPC = new Map();
let rpcID = 0;

try {
  const bundle = join(work, "worker.mjs");
  await build({ entryPoints: [join(source, "src/index.ts")], outfile: bundle, bundle: true, format: "esm", platform: "browser", external: ["cloudflare:workers"], nodePaths: [join(root, "node_modules")], logLevel: "silent" });
  const helpersPath = join(work, "helpers.mjs");
  await build({ stdin: { contents: 'export {canonicalDeviceChallenge,canonicalDeviceRefresh,encodeBase64URL} from "./src/shared/crypto"; export {makeEnvelope} from "./src/shared/wire"; export {parseCallResponse} from "./src/ui/api";', resolveDir: source, loader: "ts" }, outfile: helpersPath, bundle: true, format: "esm", platform: "node", nodePaths: [join(root, "node_modules")], logLevel: "silent" });
  const { canonicalDeviceChallenge, canonicalDeviceRefresh, encodeBase64URL, makeEnvelope, parseCallResponse } = await import(pathToFileURL(helpersPath).href);
  runtime = new Miniflare(convertV4MiniflareOptions({
    name: "executor-http-fixture", cf: false, modules: true, script: await readFile(bundle, "utf8"), host: "127.0.0.1", port: 0,
    compatibilityDate: "2026-08-22", compatibilityFlags: ["nodejs_compat"],
    d1Databases: ["DB"], durableObjects: { DEVICE_RELAY: { className: "DeviceRelay", useSQLite: true } },
    bindings: { ACCESS_AUD: "isolated-http-audience", ACCESS_TEAM_DOMAIN: "isolated-http.cloudflareaccess.com", ENROLLMENT_TOKEN_HASH: digest(enrollmentToken) },
    outboundService: async (request) => new URL(request.url).href === "https://isolated-http.cloudflareaccess.com/cdn-cgi/access/certs"
      ? new Response(JSON.stringify({ keys: [accessPublicKey] }), { headers: { "content-type": "application/json" } })
      : new Response("test egress rejected", { status: 502 }),
    log: new Log(LogLevel.ERROR),
  }));
  const origin = (await runtime.ready).origin;
  console.log("HTTP_FIXTURE_STAGE=runtime_ready");
  const database = await runtime.getD1Database("DB");
  for (const file of (await readdir(join(source, "migrations"))).filter((name) => name.endsWith(".sql")).sort()) {
    const sql = await readFile(join(source, "migrations", file), "utf8");
    for (const statement of sql.split(";").map((value) => value.trim()).filter(Boolean)) await database.prepare(statement).run();
  }
  const accessToken = await new SignJWT({}).setProtectedHeader({ alg: "RS256", kid: "http-test" }).setIssuer("https://isolated-http.cloudflareaccess.com").setAudience("isolated-http-audience").setSubject(subject).setIssuedAt().setNotBefore(Math.floor(Date.now() / 1000) - 1).setExpirationTime("10m").sign(accessKey.privateKey);
  const browserID = randomBytes(32).toString("base64url");
  const grant = await new SignJWT({ version: 1, device_id: deviceID, access_subject: subject, browser_id: browserID, generation: 1, issued_at: Math.floor(Date.now() / 1000), expires_at: Math.floor(Date.now() / 1000) + 600, jti: randomUUID() }).setProtectedHeader({ alg: "ES256", typ: "executor-device-grant+jwt", version: 1 }).sign(deviceKey.privateKey);
  const cookie = `__Host-executor-browser=${browserID}; __Secure-executor-grant-${digest(deviceID).slice(0, 16)}=${grant}`;
  const authHeaders = { "cf-access-jwt-assertion": accessToken, cookie, origin, "content-type": "application/json" };
  const post = (input) => fetch(`${origin}/api/devices/${deviceID}/call`, { method: "POST", headers: authHeaders, body: JSON.stringify({ method: "filesystem_read", arguments: input }), signal: AbortSignal.timeout(15_000) });
  const enrolled = await fetch(`${origin}/api/device/enroll`, { method: "POST", headers: { authorization: `Bearer ${enrollmentToken}`, origin, "content-type": "application/json" }, body: JSON.stringify({ device_id: deviceID, name: "Isolated HTTP fixture", platform: "test", arch: "test", version: "test", mcp_url: "https://fixture.invalid/mcp", public_jwk: publicKey, generation: 1 }) });
  assert.equal(enrolled.status, 201, "fixture enrollment");
  console.log("HTTP_FIXTURE_STAGE=enrolled");
  await enrolled.arrayBuffer();

  if (betaBinary) {
    assert(betaBinary.includes("Executor Beta/") && betaConfig.includes("Executor Beta/state/"), "Explicit Beta-only paths required");
    beta = spawn(betaBinary, ["stdio", "--config", betaConfig], { stdio: ["pipe", "pipe", "pipe"] });
    beta.stderr.on("data", () => {});
    const lines = createInterface({ input: beta.stdout });
    lines.on("line", (line) => {
      let value;
      try { value = JSON.parse(line); } catch { return; }
      const waiting = pendingRPC.get(value.id);
      if (waiting) { pendingRPC.delete(value.id); clearTimeout(waiting.timer); waiting.resolve(value); }
    });
    beta.on("error", () => { for (const waiting of pendingRPC.values()) waiting.reject(new Error("Beta subprocess unavailable")); });
    await rpc("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "isolated-http-read-test", version: "1" } });
  }

  async function rpc(method, params) {
    const id = ++rpcID;
    const response = new Promise((resolveValue, reject) => {
      const timer = setTimeout(() => { pendingRPC.delete(id); reject(new Error("Beta fixture RPC timeout")); }, 10_000);
      pendingRPC.set(id, { resolve: resolveValue, reject, timer });
    });
    beta.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
    const value = await response;
    assert(!value.error && !value.result?.isError, "Beta fixture RPC failed");
    return value.result;
  }

  async function resultFor(input) {
    assert([fixture, empty].includes(input.path), "Only this run's fixture files are readable");
    if (beta) {
      const result = await rpc("tools/call", { name: "filesystem_read", arguments: input });
      return result.structuredContent;
    }
    const bytes = input.path === fixture ? data : Buffer.alloc(0);
    const start = input.offset ?? 0;
    const part = bytes.subarray(start, start + (input.limit ?? 65536));
    return { content: part.toString("base64"), encoding: "base64", returnedBytes: part.length, nextOffsetBytes: start + part.length, eof: start + part.length === bytes.length };
  }

  async function connect() {
    const socket = new WebSocket(`${origin.replace(/^http/u, "ws")}/api/device/connect/${deviceID}`);
    const queue = [];
    const waiters = [];
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      const waiting = waiters.shift();
      if (waiting) { clearTimeout(waiting.timer); waiting.resolve(message); } else queue.push(message);
    });
    const peer = {
      socket,
      next: () => queue.length ? Promise.resolve(queue.shift()) : new Promise((resolveValue, reject) => {
        const waiting = { resolve: resolveValue, timer: setTimeout(() => { const i = waiters.indexOf(waiting); if (i >= 0) waiters.splice(i, 1); reject(new Error("fixture WebSocket timeout")); }, 10_000) };
        waiters.push(waiting);
      }),
      send: (value) => socket.send(JSON.stringify(value)),
    };
    peers.push(peer);
    await new Promise((resolveValue, reject) => { socket.addEventListener("open", resolveValue, { once: true }); socket.addEventListener("error", () => reject(new Error("fixture connection failed")), { once: true }); });
    console.log("HTTP_FIXTURE_STAGE=websocket_open");
    const offer = await peer.next();
    console.log("HTTP_FIXTURE_STAGE=version_offer");
    peer.send(makeEnvelope("version_negotiation", offer.message_id, { supported_versions: [1] }));
    const challenge = await peer.next();
    console.log("HTTP_FIXTURE_STAGE=challenge");
    const sign = async (text) => encodeBase64URL(new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, deviceKey.privateKey, new TextEncoder().encode(text))));
    peer.send({ version: 1, type: "device_challenge_response", nonce: challenge.nonce, issued_at: challenge.issued_at, signature: await sign(canonicalDeviceChallenge(deviceID, challenge.nonce, challenge.issued_at)) });
    assert.equal((await peer.next()).type, "device_authenticated");
    console.log("HTTP_FIXTURE_STAGE=authenticated");
    const refresh = { device_id: deviceID, generation: 1, name: "Isolated HTTP fixture", platform: "test", arch: "test", executor_version: "test", mcp_url: "https://fixture.invalid/mcp", issued_at: Math.floor(Date.now() / 1000) };
    peer.send({ version: 1, type: "device_refresh", ...refresh, signature: await sign(canonicalDeviceRefresh(refresh)) });
    assert.equal((await peer.next()).type, "device_refreshed");
    console.log("HTTP_FIXTURE_STAGE=ready");
    return peer;
  }

  let peer = await connect();
  const input = { action: "read_file", path: fixture, offset: 0, limit: 65536, encoding: "base64", privilege: "owner" };
  let headersArrived = false;
  const delayed = post(input).then((response) => { headersArrived = true; console.log("HTTP_FIXTURE_INITIAL_STATUS=" + response.status); return response; });
  const held = await peer.next();
  assert.equal(held.type, "request");
  await delay(75);
  const earlyHeaders = headersArrived;
  peer.send(makeEnvelope("response", "held-response", { request_id: held.payload.request_id, result: await resultFor(input) }));
  await (await delayed).arrayBuffer();
  if (!baseline) assert.equal(earlyHeaders, false, "HTTP success-header commitment boundary");

  const failing = post(input);
  const failedRequest = await peer.next();
  await delay(75);
  peer.socket.close(1000, "controlled pre-first-byte loss");
  const failedResponse = await failing;
  let body = "";
  let bodyReadFailed = false;
  try { body = new TextDecoder().decode(await failedResponse.arrayBuffer()); } catch { bodyReadFailed = true; }
  const failure = { status: failedResponse.status, bytes: Buffer.byteLength(body), bodyReadFailed };
  assert.equal(failure.status, baseline ? 200 : 502);
  console.log("HTTP_FIXTURE_FAILURE=" + JSON.stringify(failure));
  if (baseline) assert.equal(failure.bytes, 0);
  else assert.equal(bodyReadFailed, false);
  if (!baseline) assert.equal(JSON.parse(body).request_id, failedRequest.payload.request_id);

  peer = await connect();
  let pageRequests = 0;
  for (let run = 0; run < iterations; run++) {
    const parts = [];
    let offset = 0;
    while (true) {
      const pageInput = { ...input, offset };
      const requestPromise = post(pageInput);
      const request = await peer.next();
      assert.equal(request.type, "request");
      const result = await resultFor(pageInput);
      peer.send(makeEnvelope("response", `page-${pageRequests++}`, { request_id: request.payload.request_id, result }));
      const response = await requestPromise;
      assert.equal(response.status, 200);
      const parsed = await parseCallResponse(response);
      const page = Buffer.from(parsed.result.content, "base64");
      parts.push(page);
      offset = parsed.result.nextOffsetBytes;
      if (parsed.result.eof) break;
      assert(page.length > 0 && offset <= data.length, "pagination progress");
    }
    assert.equal(digest(Buffer.concat(parts)), digest(data), "exact HTTP fixture reconstruction");
  }
  const emptyInput = { ...input, path: empty };
  const emptyRequest = post(emptyInput);
  const wire = await peer.next();
  peer.send(makeEnvelope("response", "empty-file", { request_id: wire.payload.request_id, result: await resultFor(emptyInput) }));
  const emptyResult = await parseCallResponse(await emptyRequest);
  assert.equal(emptyResult.result.returnedBytes, 0);
  assert.equal(emptyResult.result.eof, true);

  console.log(JSON.stringify({ mode: baseline ? "baseline" : "fixed", transport: "real loopback HTTP + WebSocket + Durable Object RPC", result_source: beta ? "installed Beta stdio/helpers through deterministic WebSocket test peer" : "isolated fixture peer", success_headers_before_first_byte: earlyHeaders, pre_first_byte_failure: failure, full_reads: iterations, page_requests: pageRequests, fixture_bytes: data.length, all_sha256_match: true, empty_file_valid: true, production_services_changed: false }));
} finally {
  for (const peer of peers) peer.socket.close(1000, "test complete");
  for (const waiting of pendingRPC.values()) { clearTimeout(waiting.timer); waiting.reject(new Error("test ending")); }
  pendingRPC.clear();
  if (beta) {
    beta.stdin.end();
    await Promise.race([new Promise((resolveValue) => beta.once("exit", resolveValue)), delay(2000)]);
    if (beta.exitCode === null) beta.kill("SIGTERM");
  }
  await runtime?.dispose();
  await rm(work, { recursive: true, force: true });
}
