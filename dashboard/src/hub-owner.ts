import { importJWK } from "jose";
import type { DeviceRecord } from "./db";
import { registeredHub, type RegisteredHub } from "./hub-admin";
import { validDelegation } from "./hub";
import { jsonResponse, readBoundedJSON } from "./http";
import { sha256Hex } from "./shared/crypto";

export interface HubOwnerAction { hub: RegisteredHub; method: "hub.delegate" | "hub.revoke"; input: Record<string,unknown>; keyID: string }

// Device authorization has already been verified by control.ts. Browser input
// selects only a registered Hub; it cannot substitute that Hub's public key.
export async function prepareHubOwner(db: D1Database, method: "hub.delegate"|"hub.revoke", input: unknown): Promise<HubOwnerAction> {
  if (input === null || typeof input !== "object" || Array.isArray(input) || Object.keys(input).length !== 1 || !Object.hasOwn(input,"hub_id")) throw new Error("invalid Hub selection");
  const id = (input as Record<string,unknown>).hub_id;
  if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(id)) throw new Error("invalid Hub selection");
  const hub = await registeredHub(db,id);
  if (hub === null || method === "hub.delegate" && hub.enabled !== 1) throw new Error("Hub unavailable");
  const key = JSON.parse(hub.public_jwk) as Record<string,unknown>;
  if (Object.keys(key).sort().join(",") !== "crv,kty,x,y" || key.kty !== "EC" || key.crv !== "P-256" || typeof key.x !== "string" || typeof key.y !== "string") throw new Error("invalid Hub key");
  await importJWK(key,"ES256");
  const keyID = await sha256Hex(JSON.stringify({ crv:key.crv,kty:key.kty,x:key.x,y:key.y }));
  return { hub,method,keyID,input:method === "hub.delegate" ? { hub_id:id,public_key:key,expires_in_seconds:30*86400 } : { hub_id:id } };
}

export async function finalizeHubOwner(request: Request, response: Response, env: Env, device: DeviceRecord, action: HubOwnerAction, requestID: string): Promise<Response> {
  if (!response.ok) return response;
  const uncertain = () => jsonResponse({ error:"Hub owner action could not be fully confirmed",outcome:"unconfirmed",request_id:requestID },502);
  try {
    const raw = await readBoundedJSON(new Request(request.url,{ method:"POST",body:response.body }),65536);
    if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return uncertain();
    const result = raw as Record<string,unknown>;
    const enabled = action.method === "hub.delegate";
    const version = result.delegation_version;
    if (result.hub_id !== action.hub.hub_id || result.device_id !== device.device_id || result.generation !== device.generation || result.enabled !== enabled || typeof version !== "number" || !Number.isSafeInteger(version) || version <= 0) return uncertain();
    let grant = "", expires = 0;
    if (enabled) {
      if (typeof result.grant !== "string" || result.grant.length > 16384 || typeof result.expires_at !== "number" || !Number.isSafeInteger(result.expires_at)) return uncertain();
      grant = result.grant; expires = result.expires_at * 1000;
      if (!(await validDelegation(device,action.hub.hub_id,{ device_id:device.device_id,device_generation:device.generation,delegation_version:version,expires_at:expires,grant },grant,Date.now(),action.keyID,result.expires_at))) return uncertain();
    }
    const saved = await env.DB.prepare(`INSERT INTO hub_devices (hub_id,device_id,device_generation,delegation_version,expires_at,grant)
      SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM hubs WHERE hub_id=? AND public_jwk=? AND token_hash=? AND (?=0 OR enabled=1))
      AND EXISTS (SELECT 1 FROM devices WHERE device_id=? AND generation=?)
      ON CONFLICT(hub_id,device_id) DO UPDATE SET device_generation=excluded.device_generation,delegation_version=excluded.delegation_version,expires_at=excluded.expires_at,grant=excluded.grant
      WHERE excluded.device_generation>hub_devices.device_generation OR (excluded.device_generation=hub_devices.device_generation AND excluded.delegation_version>=hub_devices.delegation_version)`)
      .bind(action.hub.hub_id,device.device_id,device.generation,version,expires,grant,action.hub.hub_id,action.hub.public_jwk,action.hub.token_hash,enabled?1:0,device.device_id,device.generation).run();
    if (saved.meta.changes !== 1) return uncertain();
    return jsonResponse({ hub_id:action.hub.hub_id,device_id:device.device_id,enabled,delegation_version:version,expires_at:expires });
  } catch { return uncertain(); }
}
