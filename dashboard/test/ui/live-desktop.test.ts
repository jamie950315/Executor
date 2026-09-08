import { describe, expect, it, vi } from "vitest";
import { LiveInput, LiveDesktopConnection, LiveVideoWatch, containedPoint, gatherICE, liveErrorMessage } from "../../src/ui/live-desktop";
import type { DeviceCall } from "../../src/ui/panels/types";

describe("live desktop input", () => {
 it("distinguishes encoder failures from input and never exposes raw device data",()=>{
  expect(liveErrorMessage("video_start_failed")).toMatch(/video.*FFmpeg/i);
  expect(liveErrorMessage("input_release_failed")).toMatch(/released/i);
  expect(liveErrorMessage("SECRET DEVICE DATA")).not.toContain("SECRET DEVICE DATA");
 });
 it("requires freshly decoded frames and stops when decoding stalls",()=>{
  vi.useFakeTimers();let frame:VideoFrameRequestCallback=()=>{};const cancel=vi.fn();const v={requestVideoFrameCallback:vi.fn((cb:VideoFrameRequestCallback)=>{frame=cb;return 1;}),cancelVideoFrameCallback:cancel} as unknown as HTMLVideoElement;
  const fresh=vi.fn();const stalled=vi.fn();const watch=new LiveVideoWatch(v,fresh,stalled);expect(fresh).not.toHaveBeenCalled();
  frame(0,{} as VideoFrameCallbackMetadata);expect(fresh).toHaveBeenCalledWith(true);vi.advanceTimersByTime(2500);expect(stalled).not.toHaveBeenCalled();vi.advanceTimersByTime(1000);expect(stalled).toHaveBeenCalledOnce();expect(fresh).toHaveBeenLastCalledWith(false);expect(cancel).toHaveBeenCalled();watch.close();vi.useRealTimers();
 });
 it("maps only the contained video, excluding letterboxing", () => {
  const rect={left:10,top:20,width:400,height:400};
  expect(containedPoint(rect,1920,1080,210,220)).toEqual({x:.5,y:.5});
  expect(containedPoint(rect,1920,1080,210,30)).toBeNull();
 });
 it("coalesces moves and releases without replaying queued movement", () => {
  vi.useFakeTimers();const send=vi.fn();const dc={readyState:"open",bufferedAmount:0,send} as unknown as RTCDataChannel;
  const input=new LiveInput(dc,vi.fn());input.move({x:.1,y:.1});input.move({x:.8,y:.8});vi.advanceTimersByTime(34);
  expect(send).toHaveBeenCalledTimes(1);expect(JSON.parse(send.mock.calls[0]![0])).toMatchObject({type:"move",x:.8,seq:1});
  input.move({x:.2,y:.2});input.release();vi.advanceTimersByTime(100);expect(send).toHaveBeenCalledTimes(2);expect(JSON.parse(send.mock.calls[1]![0])).toMatchObject({type:"release",seq:2});input.close();vi.useRealTimers();
 });
 it("fails closed on a congested data channel",()=>{
  const fail=vi.fn();const send=vi.fn();const input=new LiveInput({readyState:"open",bufferedAmount:300000,send} as unknown as RTCDataChannel,fail);
  input.send({type:"key",code:"KeyA",down:false});expect(fail).toHaveBeenCalledOnce();expect(send).not.toHaveBeenCalled();input.close();
 });
 it("waits for complete ICE and rejects cancellation",async()=>{
  const pc=new EventTarget() as RTCPeerConnection;Object.defineProperty(pc,"iceGatheringState",{value:"gathering",writable:true});const abort=new AbortController();const p=gatherICE(pc,abort.signal);abort.abort();await expect(p).rejects.toThrow();
 });
 it("uses gathered candidates at the deadline even while another interface is pending",async()=>{
  vi.useFakeTimers();const pc=new EventTarget() as RTCPeerConnection;
  Object.defineProperty(pc,"iceGatheringState",{value:"gathering"});
  Object.defineProperty(pc,"localDescription",{value:{sdp:"v=0\r\na=candidate:1 1 udp 1 192.0.2.1 12345 typ host\r\n"}});
  const pending=gatherICE(pc,new AbortController().signal);
  await vi.advanceTimersByTimeAsync(8000);await expect(pending).resolves.toBe(false);vi.useRealTimers();
 });
});

class FakePeer extends EventTarget {
 iceGatheringState="complete";connectionState="new";localDescription={sdp:"offer"};
 ontrack=null;onconnectionstatechange=null; dc={readyState:"open",bufferedAmount:0,send:vi.fn(),close:vi.fn(),onclose:null,onerror:null,onmessage:null};
 addTransceiver=vi.fn();createDataChannel=vi.fn(()=>this.dc);createOffer=vi.fn(async()=>({sdp:"offer",type:"offer"}));setLocalDescription=vi.fn(async()=>{});setRemoteDescription=vi.fn(async()=>{});close=vi.fn();
}
const liveStatus={supported:true,available:true,active:false,iceServers:[]};
const liveSession={sessionId:"session1",answer:"answer",width:1280,height:720,leaseSeconds:15};
describe("live desktop connection",()=>{
 it("requests bounded 30 FPS video at the smoother setting",async()=>{
  const peer=new FakePeer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
  const call=vi.fn(async()=>({result:liveSession})) as unknown as DeviceCall;
  const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped:vi.fn()});
  await connection.start(liveStatus,30);
  expect(call).toHaveBeenCalledWith("desktop_live",{action:"start",offer:"offer",maxWidth:1280,fps:30,bitrate:4000000});
  connection.stop();vi.unstubAllGlobals();
 });
 it("identifies local discovery timeout without blaming device permissions",async()=>{
  vi.useFakeTimers();const peer=new FakePeer();peer.iceGatheringState="gathering";
  vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
  const call=vi.fn();const stopped=vi.fn();const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped});
  const starting=connection.start(liveStatus);await vi.advanceTimersByTimeAsync(8100);await starting;
  expect(call).not.toHaveBeenCalled();expect(stopped).toHaveBeenCalledWith(expect.stringContaining("Browser network discovery failed"));
  vi.unstubAllGlobals();vi.useRealTimers();
 });
 it("reports missing browser WebRTC before contacting the device",async()=>{
  vi.stubGlobal("RTCPeerConnection",undefined);
  const call=vi.fn();const stopped=vi.fn();
  const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped});
  await connection.start(liveStatus);
  expect(call).not.toHaveBeenCalled();
  expect(stopped).toHaveBeenCalledWith("This browser does not provide WebRTC video connections. Open this Dashboard in a WebRTC-enabled browser such as Chrome or Edge; device permissions cannot fix this browser limitation.");
  vi.unstubAllGlobals();
 });
 it("pings once a second for the server control watchdog and stops all heartbeats on close",async()=>{
  vi.useFakeTimers();const peer=new FakePeer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
  const call=vi.fn(async()=>({result:liveSession})) as unknown as DeviceCall;const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped:vi.fn()});await connection.start(liveStatus);
  (peer.dc.onmessage as unknown as (event:{data:string})=>void)({data:JSON.stringify({type:"ready",control:false})});
  await vi.advanceTimersByTimeAsync(2100);expect(peer.dc.send).toHaveBeenCalledTimes(2);expect(JSON.parse(peer.dc.send.mock.calls[0]![0])).toMatchObject({type:"ping",seq:1});connection.stop();const count=peer.dc.send.mock.calls.length;await vi.advanceTimersByTimeAsync(5000);expect(peer.dc.send).toHaveBeenCalledTimes(count);vi.unstubAllGlobals();vi.useRealTimers();
 });
 it("receives video only, renews the exact lease, then closes peer and lease",async()=>{
  vi.useFakeTimers();const peer=new FakePeer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
  const call=vi.fn(async()=>({result:liveSession})) as unknown as DeviceCall;const callbacks={stream:vi.fn(),state:vi.fn(),stopped:vi.fn()};const connection=new LiveDesktopConnection(call,callbacks);
  await connection.start(liveStatus);expect(peer.addTransceiver).toHaveBeenCalledWith("video",{direction:"recvonly"});expect(peer.createDataChannel).toHaveBeenCalledWith("executor-input",{ordered:true});
  await vi.advanceTimersByTimeAsync(5000);expect(call).toHaveBeenCalledWith("desktop_live",{action:"renew",sessionId:"session1"});
  connection.stop();expect(peer.close).toHaveBeenCalledOnce();expect(call).toHaveBeenCalledWith("desktop_live",{action:"stop",sessionId:"session1"});expect(peer.dc.send).toHaveBeenCalledWith(JSON.stringify({type:"release",seq:1}));vi.unstubAllGlobals();vi.useRealTimers();
 });
 it("stops a late start result after the local session was abandoned",async()=>{
  const peer=new FakePeer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));let resolve:(value:unknown)=>void=()=>{};
  const call=vi.fn().mockImplementationOnce(()=>new Promise(r=>{resolve=r;})).mockResolvedValue({result:{}}) as unknown as DeviceCall;
  const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped:vi.fn()});const starting=connection.start(liveStatus);
  await vi.waitFor(()=>expect(call).toHaveBeenCalledOnce());connection.stop();resolve({result:liveSession});await starting;
  expect(call).toHaveBeenLastCalledWith("desktop_live",{action:"stop",sessionId:"session1"});expect(peer.setRemoteDescription).not.toHaveBeenCalled();vi.unstubAllGlobals();
 });
 it("failed renewal closes locally rather than continuing input",async()=>{
  vi.useFakeTimers();const peer=new FakePeer();vi.stubGlobal("RTCPeerConnection",vi.fn(function(){return peer;}));
  const call=vi.fn().mockResolvedValueOnce({result:liveSession}).mockRejectedValueOnce(new Error()).mockResolvedValue({result:{}}) as unknown as DeviceCall;
  const stopped=vi.fn();const connection=new LiveDesktopConnection(call,{stream:vi.fn(),state:vi.fn(),stopped});await connection.start(liveStatus);await vi.advanceTimersByTimeAsync(5000);
  expect(peer.close).toHaveBeenCalledOnce();expect(stopped).toHaveBeenCalledWith("Session renewal failed. Control has stopped.");vi.unstubAllGlobals();vi.useRealTimers();
 });
});
