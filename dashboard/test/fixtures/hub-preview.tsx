import { useState } from "react";
import { createRoot } from "react-dom/client";
import { HubManager } from "../../src/ui/HubManager";
import type { HubLink, HubRecord, HubService } from "../../src/ui/hub-api";
import type { DeviceView } from "../../src/ui/DeviceGrid";
import "../../src/ui/styles.css";

const key={kty:"EC",crv:"P-256",x:"a".repeat(43),y:"b".repeat(43)} as const;
let hubs:HubRecord[]=[{hub_id:"pi5-hub",public_key:key,enabled:true}];
const links=new Map<string,HubLink[]>();
const api:HubService={
  list:async()=>hubs.map(h=>({...h})),
  links:async id=>links.get(id)??[],
  register:async input=>{const row={hub_id:input.hub_id,public_key:input.public_key,enabled:true};hubs=[...hubs,row];return row;},
  disable:async id=>{hubs=hubs.map(h=>h.hub_id===id?{...h,enabled:false}:h);},
  delegate:async(device,id)=>{links.set(id,[...(links.get(id)??[]).filter(row=>row.device_id!==device),{device_id:device,authorized:true,granted:true,delegation_version:1,expires_at:Date.now()+86400000}]);},
  revoke:async(device,id)=>{links.set(id,(links.get(id)??[]).map(row=>row.device_id===device?{...row,authorized:false,granted:false}:row));},
};
function Preview(){
  const [devices,setDevices]=useState<DeviceView[]>([
    {device_id:"mac",name:"Mac Beta",platform:"darwin",generation:1,state:"online",uiState:"unlocked"} as DeviceView,
    {device_id:"windows",name:"Windows CTPS",platform:"windows",generation:1,state:"online",uiState:"locked"} as DeviceView,
    {device_id:"wsl",name:"Debian WSL",platform:"linux",generation:1,state:"offline",uiState:"offline"} as DeviceView,
  ]);
  const [notice,setNotice]=useState("");
  return <main className="fleet-main"><p className="eyebrow">Local UI fixture · no real device operations</p><h1>Hub management</h1>{notice&&<p role="status">{notice}</p>}<HubManager devices={devices} api={api} onNotice={setNotice} onUnlock={device=>setDevices(current=>current.map(d=>d.device_id===device.device_id?{...d,uiState:"unlocked"}:d))}/></main>;
}
createRoot(document.getElementById("root")!).render(<Preview/>);
