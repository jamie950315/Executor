import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LiveRemoteDesktopPanel } from "../../src/ui/panels/LiveRemoteDesktopPanel";
import type { DeviceCall } from "../../src/ui/panels/types";

afterEach(()=>vi.unstubAllGlobals());
it("provides fullscreen without enabling remote input",async()=>{
 const call:DeviceCall=async()=>({requestID:"status",result:{supported:true,available:true,active:false,iceServers:[]}});
 render(<LiveRemoteDesktopPanel call={call}/>);
 const request=vi.fn().mockResolvedValue(undefined);
 Object.defineProperty(screen.getByRole("application"),"requestFullscreen",{value:request});
 fireEvent.click(screen.getByRole("button",{name:"Fullscreen"}));
 await waitFor(()=>expect(request).toHaveBeenCalledOnce());
 expect(screen.getByRole("button",{name:"Enable control"})).toHaveAttribute("aria-pressed","false");
});
it("explains unsupported Linux live mode without probing an older helper",async()=>{
 const call=vi.fn<DeviceCall>();
 render(<LiveRemoteDesktopPanel call={call} platform="linux"/>);
 expect(screen.getByText(/Live video currently supports macOS and Windows/)).toBeVisible();
 expect(call).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Snapshot tools"}));
 expect(screen.getByRole("button",{name:"Refresh screen"})).toBeInTheDocument();
});
it("defaults to live mode and retains explicit snapshot tools",async()=>{
 const call=vi.fn(async()=>({result:{supported:false,available:false,active:false,iceServers:[],reason:"Unsupported platform"}})) as unknown as DeviceCall;
 render(<LiveRemoteDesktopPanel call={call}/>);await screen.findByText("Unsupported platform");expect(screen.getByRole("button",{name:"Start live desktop"})).toBeDisabled();
 expect(screen.queryByRole("button",{name:"Refresh screen"})).not.toBeInTheDocument();fireEvent.click(screen.getByRole("button",{name:"Snapshot tools"}));expect(screen.getByRole("button",{name:"Refresh screen"})).toBeInTheDocument();
});
it("requires video and explicit control, releases on Escape, and closes on unmount",async()=>{
 class Peer extends EventTarget {
  iceGatheringState="complete";localDescription={sdp:"offer"};connectionState="new";
  ontrack=null;onconnectionstatechange=null; dc={readyState:"open",bufferedAmount:0,send:vi.fn(),close:vi.fn(),onclose:null,onerror:null,onmessage:null as null|((e:{data:string})=>void)};
  addTransceiver=vi.fn();createDataChannel=()=>this.dc;createOffer=async()=>({sdp:"offer",type:"offer"});setLocalDescription=async()=>{};setRemoteDescription=async()=>{};close=vi.fn();
 }
 const peer=new Peer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
 const call=vi.fn(async(_method:string,args:unknown)=>({result:(args as {action:string}).action==="status"?{supported:true,available:true,active:false,iceServers:[]}:{sessionId:"one",answer:"answer",width:1280,height:720,leaseSeconds:15}})) as unknown as DeviceCall;
 const view=render(<LiveRemoteDesktopPanel call={call}/>);await waitFor(()=>expect(screen.getByRole("button",{name:"Start live desktop"})).toBeEnabled());fireEvent.click(screen.getByRole("button",{name:"Start live desktop"}));
 expect(screen.getByRole("combobox",{name:"Frame rate"})).toHaveValue("30");
 expect(screen.getByRole("combobox",{name:"Frame rate"})).toBeDisabled();
 await waitFor(()=>expect(peer.dc.onmessage).not.toBeNull());act(()=>peer.dc.onmessage?.({data:JSON.stringify({type:"ready",control:false})}));
  expect(screen.getByRole("button",{name:"Enable control"})).toBeDisabled();const video=screen.getByLabelText("Live primary display") as HTMLVideoElement;fireEvent.loadedData(video);expect(screen.getByRole("button",{name:"Enable control"})).toBeDisabled();
  let decoded:VideoFrameRequestCallback=()=>{};Object.defineProperties(video,{requestVideoFrameCallback:{value:vi.fn((cb:VideoFrameRequestCallback)=>{decoded=cb;return 1;})},cancelVideoFrameCallback:{value:vi.fn()}});vi.spyOn(video,"play").mockResolvedValue();
  act(()=>{(peer.ontrack as unknown as (event:{streams:object[]})=>void)({streams:[{}]});});act(()=>decoded(0,{} as VideoFrameCallbackMetadata));
  view.rerender(<LiveRemoteDesktopPanel call={(...args)=>call(...args)}/>);expect(peer.close).not.toHaveBeenCalled();
 const stage=screen.getByRole("application");fireEvent.keyDown(stage,{key:"a",code:"KeyA"});expect(peer.dc.send).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Enable control"}));expect(JSON.parse(peer.dc.send.mock.calls.at(-1)![0])).toMatchObject({type:"control",enabled:true});act(()=>peer.dc.onmessage?.({data:JSON.stringify({type:"state",control:true})}));
 fireEvent.keyDown(stage,{key:"a",code:"KeyA"});expect(JSON.parse(peer.dc.send.mock.calls.at(-1)![0])).toMatchObject({type:"key",code:"KeyA",down:true});
 Object.defineProperties(video,{videoWidth:{value:1920},videoHeight:{value:1080}});vi.spyOn(video,"getBoundingClientRect").mockReturnValue({left:10,top:20,width:400,height:400} as DOMRect);
 fireEvent.wheel(stage,{clientX:210,clientY:220,deltaY:120});expect(JSON.parse(peer.dc.send.mock.calls.at(-1)![0])).toMatchObject({type:"wheel",x:.5,y:.5,scrollY:120});
 fireEvent.keyDown(window,{key:"Escape",code:"Escape"});expect(JSON.parse(peer.dc.send.mock.calls.at(-1)![0])).toMatchObject({type:"release"});expect(screen.getByRole("button",{name:"Enable control"})).toHaveAttribute("aria-pressed","false");
 const count=peer.dc.send.mock.calls.length;fireEvent.keyDown(stage,{key:"a",code:"KeyA"});expect(peer.dc.send).toHaveBeenCalledTimes(count);
 let rejectPlayback:(reason:Error)=>void=()=>{};
 vi.spyOn(video,"play").mockImplementationOnce(()=>new Promise((_resolve,reject)=>{rejectPlayback=reject;}));
 act(()=>{(peer.ontrack as unknown as (event:{streams:object[]})=>void)({streams:[{}]});});
 fireEvent.click(screen.getByRole("button",{name:"Stop session"}));
 await act(async()=>rejectPlayback(new Error("play interrupted by stop")));
 expect(screen.getByText("Session stopped. No desktop input is being sent.")).toBeVisible();
 view.unmount();expect(peer.close).toHaveBeenCalledOnce();await waitFor(()=>expect(call).toHaveBeenCalledWith("desktop_live",{action:"stop",sessionId:"one"}));
});
