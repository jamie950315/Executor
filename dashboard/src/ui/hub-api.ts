import { callDevice } from "./api";

export interface HubRegistration { hub_id:string; public_key:{kty:"EC";crv:"P-256";x:string;y:string}; token_hash:string }
export interface HubRecord { hub_id:string; public_key:HubRegistration["public_key"]; enabled:boolean }
export interface HubLink { device_id:string; authorized:boolean; granted:boolean; delegation_version:number; expires_at:number }
export interface HubService {
  list(signal?:AbortSignal):Promise<HubRecord[]>;
  links(id:string,signal?:AbortSignal):Promise<HubLink[]>;
  register(input:HubRegistration):Promise<HubRecord>;
  disable(id:string):Promise<void>;
  delegate(device:string,hub:string):Promise<void>;
  revoke(device:string,hub:string):Promise<void>;
}

function record(value:unknown):value is Record<string,unknown>{return typeof value==="object" && value!==null && !Array.isArray(value);}
export function parseHubRegistration(text:string):HubRegistration {
  if(text.length>16384)throw new Error("Only public registration data is accepted");
  const value:unknown=JSON.parse(text);
  if(!record(value) || Object.keys(value).sort().join(",")!=="hub_id,public_key,token_hash" || typeof value.hub_id!=="string" || !/^[A-Za-z0-9_-]{1,128}$/.test(value.hub_id) || typeof value.token_hash!=="string" || !/^[0-9a-f]{64}$/.test(value.token_hash))throw new Error("Only public registration data is accepted");
  const key=value.public_key;
  if(!record(key) || Object.keys(key).sort().join(",")!=="crv,kty,x,y" || key.kty!=="EC" || key.crv!=="P-256" || typeof key.x!=="string" || typeof key.y!=="string" || !/^[A-Za-z0-9_-]{43}$/.test(key.x) || !/^[A-Za-z0-9_-]{43}$/.test(key.y))throw new Error("Only public registration data is accepted");
  return value as unknown as HubRegistration;
}
async function json(path:string,body?:unknown,signal?:AbortSignal):Promise<unknown>{
  const response=await fetch(path,{method:body===undefined?"GET":"POST",credentials:"same-origin",headers:{accept:"application/json",...(body===undefined?{}:{"content-type":"application/json"})},...(body===undefined?{}:{body:JSON.stringify(body)}),signal});
  if(!response.ok || !response.headers.get("content-type")?.startsWith("application/json"))throw new Error("Hub request unavailable or unconfirmed");
  return response.json();
}
async function changeDevice(device:string,hub:string,method:"hub.delegate"|"hub.revoke"):Promise<void>{
  const {result}=await callDevice(device,method,{hub_id:hub});
  if(!record(result) || result.device_id!==device || result.hub_id!==hub || result.enabled!==(method==="hub.delegate"))throw new Error("Hub device outcome unconfirmed");
}
export const hubService:HubService={
  async list(signal){const value=await json("/api/hubs",undefined,signal);if(!record(value)||!Array.isArray(value.hubs)||!value.hubs.every(item=>record(item)&&typeof item.hub_id==="string"&&typeof item.enabled==="boolean"))throw new Error("Invalid Hub registry");return value.hubs as HubRecord[];},
  async links(id,signal){const value=await json(`/api/hubs/${encodeURIComponent(id)}/devices`,undefined,signal);if(!record(value)||!Array.isArray(value.devices)||!value.devices.every(item=>record(item)&&typeof item.device_id==="string"&&typeof item.authorized==="boolean"&&typeof item.granted==="boolean"))throw new Error("Invalid Hub device registry");return value.devices as HubLink[];},
  async register(input){const value=await json("/api/hubs",input);if(!record(value)||value.hub_id!==input.hub_id||typeof value.enabled!=="boolean")throw new Error("Hub registration unconfirmed");return value as unknown as HubRecord;},
  async disable(id){const value=await json(`/api/hubs/${encodeURIComponent(id)}/disable`,{});if(!record(value)||value.hub_id!==id||value.enabled!==false)throw new Error("Hub disable unconfirmed");},
  delegate:(device,id)=>changeDevice(device,id,"hub.delegate"),
  revoke:(device,id)=>changeDevice(device,id,"hub.revoke"),
};
