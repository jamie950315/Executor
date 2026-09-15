import { importJWK } from "jose";
import type { AccessIdentity } from "./access";
import { writeAudit } from "./db";
import { errorResponse, jsonResponse, readBoundedJSON, requireStateChangingRequest } from "./http";

export interface RegisteredHub { hub_id: string; token_hash: string; public_jwk: string; enabled: number }
export async function registeredHub(db: D1Database, id: string): Promise<RegisteredHub | null> {
  return db.prepare("SELECT hub_id, token_hash, public_jwk, enabled FROM hubs WHERE hub_id=?").bind(id).first<RegisteredHub>();
}

// Called only after the normal Cloudflare Access owner authentication.
export async function handleHubAdmin(request: Request, env: Env, access: AccessIdentity): Promise<Response> {
  const path = new URL(request.url).pathname;
  const links = /^\/api\/hubs\/([A-Za-z0-9_-]{1,128})\/devices$/.exec(path);
  if (links !== null && request.method === "GET") {
    if (await registeredHub(env.DB,links[1]!) === null) return errorResponse("Hub not found",404);
    const rows = await env.DB.prepare(`SELECT h.device_id,h.device_generation,h.delegation_version,h.expires_at,length(h.grant)>0 AS granted,d.generation AS current_generation
      FROM hub_devices h JOIN devices d ON d.device_id=h.device_id WHERE h.hub_id=? ORDER BY h.device_id`).bind(links[1]).all<{device_id:string;device_generation:number;delegation_version:number;expires_at:number;granted:number;current_generation:number}>();
    return jsonResponse({devices:rows.results.map(row=>({device_id:row.device_id,delegation_version:row.delegation_version,expires_at:row.expires_at,granted:row.granted===1,authorized:row.granted===1&&row.expires_at>Date.now()&&row.device_generation===row.current_generation}))});
  }
  if (path === "/api/hubs" && request.method === "GET") {
    const rows = await env.DB.prepare("SELECT hub_id, public_jwk, enabled FROM hubs ORDER BY hub_id").all<Pick<RegisteredHub,"hub_id"|"public_jwk"|"enabled">>();
    return jsonResponse({ hubs: rows.results.map(row => ({ hub_id: row.hub_id, public_key: JSON.parse(row.public_jwk) as unknown, enabled: row.enabled === 1 })) });
  }
  if (request.method !== "POST") return errorResponse("not found",404);
  const invalid = requireStateChangingRequest(request);
  if (invalid !== null) return invalid;
  const disable = /^\/api\/hubs\/([A-Za-z0-9_-]{1,128})\/disable$/.exec(path);
  if (disable !== null) {
    const changed = await env.DB.prepare("UPDATE hubs SET enabled=0 WHERE hub_id=?").bind(disable[1]).run();
    if (changed.meta.changes === 0) return errorResponse("Hub not found",404);
    await writeAudit(env.DB,null,access.subject,"hub.disable","succeeded",Date.now());
    return jsonResponse({ hub_id: disable[1], enabled: false });
  }
  if (path !== "/api/hubs") return errorResponse("not found",404);
  let id: string, tokenHash: string, publicKey: string;
  try {
    const body = object(await readBoundedJSON(request,16384),["hub_id","public_key","token_hash"]);
    if (typeof body.hub_id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(body.hub_id) || typeof body.token_hash !== "string" || !/^[0-9a-f]{64}$/.test(body.token_hash)) throw new Error("invalid identity");
    const key = object(body.public_key,["kty","crv","x","y"]);
    if (key.kty !== "EC" || key.crv !== "P-256" || typeof key.x !== "string" || typeof key.y !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(key.x) || !/^[A-Za-z0-9_-]{43}$/.test(key.y)) throw new Error("invalid public key");
    const jwk = { kty:"EC",crv:"P-256",x:key.x,y:key.y };
    await importJWK(jwk,"ES256");
    publicKey = JSON.stringify(jwk); id = body.hub_id; tokenHash = body.token_hash;
  } catch { return errorResponse("invalid Hub registration",400); }
  const inserted = await env.DB.prepare("INSERT OR IGNORE INTO hubs (hub_id,token_hash,public_jwk,enabled) VALUES (?,?,?,1)").bind(id,tokenHash,publicKey).run();
  const current = await registeredHub(env.DB,id);
  if (current === null || current.token_hash !== tokenHash || current.public_jwk !== publicKey) return errorResponse("Hub identity conflict",409);
  await writeAudit(env.DB,null,access.subject,"hub.register",inserted.meta.changes === 1 ? "created" : "existing",Date.now());
  return jsonResponse({ hub_id:id,public_key:JSON.parse(publicKey) as unknown,enabled:current.enabled === 1 },inserted.meta.changes === 1 ? 201 : 200);
}

function object(value: unknown, keys: string[]): Record<string,unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid object");
  const record = value as Record<string,unknown>;
  if (Object.keys(record).length !== keys.length || !keys.every(key => Object.hasOwn(record,key))) throw new Error("invalid fields");
  return record;
}
