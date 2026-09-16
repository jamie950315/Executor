import { useEffect, useRef, useState } from "react";
import type { DeviceView } from "./DeviceGrid";
import { hubService, parseHubRegistration, type HubLink, type HubRecord, type HubRegistration, type HubService } from "./hub-api";

type Pending = {kind:"register";input:HubRegistration} | {kind:"disable";hubID:string} | {kind:"delegate";hubID:string;deviceID:string;generation:number} | {kind:"revoke";hubID:string;deviceID:string;generation:number};

export function HubManager({devices,onUnlock,onNotice,api=hubService}:{devices:DeviceView[];onUnlock:(device:DeviceView)=>void;onNotice:(message:string)=>void;api?:HubService}){
  const [hubs,setHubs]=useState<HubRecord[]>([]),[selected,setSelected]=useState("");
  const [links,setLinks]=useState<HubLink[]>([]),[linksReady,setLinksReady]=useState(false),[ready,setReady]=useState(false);
  const [draft,setDraft]=useState(""),[pending,setPending]=useState<Pending|null>(null);
  const [busy,setBusy]=useState(false),[refresh,setRefresh]=useState(0),[status,setStatus]=useState("Loading Hub registry…"),[error,setError]=useState<string|null>(null);
  const inFlight=useRef(false),mounted=useRef(true),confirm=useRef<HTMLButtonElement>(null);
  const returnFocus=useRef<HTMLElement|null>(null);
  const review=(action:Pending)=>{returnFocus.current=document.activeElement instanceof HTMLElement?document.activeElement:null;setPending(action);};
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;};},[]);
  useEffect(()=>{
    const controller=new AbortController();let active=true;
    setReady(false);
    void api.list(controller.signal).then(next=>{if(!active)return;setHubs(next);setSelected(current=>next.some(h=>h.hub_id===current)?current:next[0]?.hub_id??"");setReady(true);setStatus(`${next.length} registered Hub${next.length===1?"":"s"} · relay-only routing`);}).catch(()=>{if(active)setError("Hub registry unavailable. Refresh before changing access.");});
    return()=>{active=false;controller.abort();};
  },[api,refresh]);
  useEffect(()=>{
    const controller=new AbortController();let active=true;setLinks([]);setLinksReady(false);
    if(selected!=="")void api.links(selected,controller.signal).then(next=>{if(active){setLinks(next);setLinksReady(true);}}).catch(()=>{if(active)setError("Hub device status unavailable. Nothing was changed.");});
    return()=>{active=false;controller.abort();};
  },[api,selected,refresh]);
  useEffect(()=>{if(pending)confirm.current?.focus();else returnFocus.current?.focus();},[pending]);
  const hub=hubs.find(h=>h.hub_id===selected);
  const validPending=()=>{
    if(!pending||busy||!ready)return false;
    if(pending.kind==="register")return true;
    const targetHub=hubs.find(h=>h.hub_id===pending.hubID);
    if(!targetHub)return false;
    if(pending.kind==="disable")return targetHub.enabled;
    const device=devices.find(d=>d.device_id===pending.deviceID);
    return linksReady && device?.generation===pending.generation && device.state==="online" && device.uiState==="unlocked" && (pending.kind==="revoke"||targetHub.enabled);
  };
  const execute=async()=>{
    if(inFlight.current||!validPending()||pending===null)return;
    const action=pending;inFlight.current=true;setBusy(true);setError(null);
    const label=action.kind==="register"?action.input.hub_id:action.hubID;
    try{
      if(action.kind==="register")await api.register(action.input);
      else if(action.kind==="disable")await api.disable(action.hubID);
      else if(action.kind==="delegate")await api.delegate(action.deviceID,action.hubID);
      else await api.revoke(action.deviceID,action.hubID);
      onNotice(`Hub ${label}: ${action.kind} completed. No fallback or automatic retry was used.`);
      if(mounted.current){setPending(null);if(action.kind==="register")setDraft("");setRefresh(n=>n+1);}
    }catch{
      const message=`Hub ${label}: operation failed or outcome unconfirmed. Nothing was retried. Refresh state before trying again.`;
      onNotice(message);if(mounted.current){setError(message);setPending(null);}
    }finally{inFlight.current=false;if(mounted.current)setBusy(false);}
  };
  const confirmation=pending?.kind==="register"?"Confirm registration":pending?.kind==="disable"?"Confirm disable":pending?.kind==="delegate"?"Confirm authorization":"Confirm revocation";
  return <section className="panel-shell hub-manager" aria-labelledby="hub-title">
    <header className="panel-heading"><div><p className="eyebrow">Single gateway / no direct fallback</p><h2 id="hub-title">Pi5 Hub</h2></div><button disabled={busy||pending!==null} onClick={()=>{setError(null);setRefresh(n=>n+1);}}>Refresh Hubs</button></header>
    <p className="status-line" role="status">{status}</p>{error&&<p role="alert" className="hub-error">{error}</p>}
    <div className="hub-columns"><section><h3>Register public identity</h3><p>Paste public registration JSON: hub_id, public_key and token_hash. Never paste a password, private key or bearer token.</p><label htmlFor="hub-registration">Public registration JSON</label><textarea id="hub-registration" rows={7} value={draft} disabled={busy||pending!==null} onChange={e=>setDraft(e.target.value)} spellCheck={false}/><button disabled={busy||pending!==null||draft.trim()===""} onClick={()=>{try{review({kind:"register",input:parseHubRegistration(draft)});setError(null);}catch{setError("Only public P-256 registration data and a token hash are accepted. Nothing was sent.");}}}>Review registration</button></section>
    <section><h3>Device delegation</h3><label htmlFor="hub-selection">Select Hub</label><select id="hub-selection" value={selected} disabled={!ready||busy||pending!==null} onChange={e=>setSelected(e.target.value)}><option value="">Select a registered Hub</option>{hubs.map(h=><option key={h.hub_id} value={h.hub_id}>{h.hub_id}{h.enabled?"":" — disabled"}</option>)}</select>
    {hub&&<p><span className="state-chip">{hub.enabled?"Enabled":"Disabled"}</span> <button disabled={!hub.enabled||busy||pending!==null} onClick={()=>review({kind:"disable",hubID:hub.hub_id})}>Disable Hub</button></p>}
    <p>Only unlocked, online devices can change delegation. Disabling a Hub stops its gateway access; it does not delete devices.</p>
    <ul className="hub-device-list">{devices.map(device=>{
      const link=links.find(item=>item.device_id===device.device_id);
      const usable=ready&&linksReady&&device.state==="online"&&device.uiState==="unlocked"&&!busy&&pending===null&&hub!==undefined;
      return <li key={device.device_id}><div><strong>{device.name}</strong><span>{device.uiState} · {linksReady?(link?.authorized?"Authorized":"Not authorized"):"Status pending"}</span></div><div className="button-row">{device.uiState==="locked"&&<button disabled={busy||pending!==null} onClick={()=>onUnlock(device)}>Unlock {device.name} for Hub</button>}<button disabled={!usable||!hub?.enabled} onClick={()=>review({kind:"delegate",hubID:selected,deviceID:device.device_id,generation:device.generation})}>Authorize {device.name}</button><button disabled={!usable||!link?.granted} onClick={()=>review({kind:"revoke",hubID:selected,deviceID:device.device_id,generation:device.generation})}>Revoke {device.name}</button></div></li>;
    })}</ul>{devices.length===0&&<p>No enrolled devices yet.</p>}</section></div>
    {pending&&<section className="hub-confirm" aria-labelledby="hub-confirm-title" onKeyDown={e=>{if(e.key==="Escape"&&!busy)setPending(null);}}><h3 id="hub-confirm-title">{confirmation}</h3><p>{pending.kind==="delegate"?`Authorize ${pending.hubID} to use the existing full-control capabilities of device ${pending.deviceID}?`:pending.kind==="revoke"?`Revoke ${pending.hubID} on device ${pending.deviceID}?`:pending.kind==="disable"?`Disable gateway access for ${pending.hubID}?`:`Register public identity ${pending.input.hub_id}? Device access still requires separate authorization.`}</p><div className="button-row"><button disabled={busy} onClick={()=>setPending(null)}>Cancel</button><button ref={confirm} className="primary-button" disabled={!validPending()} onClick={()=>void execute()}>{busy?"Applying…":confirmation}</button></div>{!busy&&!validPending()&&<p>State changed or is unavailable. Cancel, then refresh or unlock the device before continuing.</p>}</section>}
  </section>;
}
