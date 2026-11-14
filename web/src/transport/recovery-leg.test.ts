import {afterEach,expect,test,vi} from "vitest";
import {create,toBinary,fromBinary} from "@bufbuild/protobuf";
import {SyntheticRemote} from "../test/remote-fixture";
import {CommandSchema,CommandResultSchema} from "../gen/remoteagent/v1/remote_pb";
import {ServerFrameSchema} from "../gen/remoteagent/v1/relay_pb";
import {parseHeader} from "../device-security";
import {unpackSecurity} from "../device-security-client";
import {webMember} from "../test/security-fixture";
import {restoreSlot,newSlot} from "../remote";
import payload from "../../../testdata/transport/prepared-command.json";
const remotes:SyntheticRemote[]=[];
afterEach(async()=>{vi.useRealTimers();for(const r of remotes.splice(0))await r.dispose();vi.restoreAllMocks();});
async function fixture(){const r=await SyntheticRemote.create();remotes.push(r);return r;}
const packet=(kind:unknown)=>toBinary(ServerFrameSchema,create(ServerFrameSchema,{kind} as never)).buffer as ArrayBuffer;
function deferred<T=void>() {let resolve!:(x:T)=>void;const promise=new Promise<T>(r=>{resolve=r});return {promise,resolve};}

test("a failed probe cannot discard queued frames on the still-active leg",async()=>{
 const r=await fixture(),rt=r as any;vi.useFakeTimers();
 const leg={ready:true,send:vi.fn(),close:vi.fn(),onFrame:undefined as ((d:ArrayBuffer)=>void)|undefined};
 vi.spyOn(r.deviceLink,"openPathStream").mockReturnValue(leg);
 const held=deferred();const handler=vi.spyOn(rt,"onFrame").mockImplementationOnce(()=>held.promise).mockResolvedValue(undefined);
 rt.openStream(rt.generation);leg.onFrame!(packet({case:"ready",value:{}}));await Promise.resolve();
 leg.onFrame!(packet({case:"heartbeat",value:{}}));
 vi.spyOn(rt,"preStream").mockResolvedValue(false);await rt.connect();
 expect(rt.ws).toBe(leg);expect(leg.close).not.toHaveBeenCalled();held.resolve();await vi.advanceTimersByTimeAsync(0);
 expect(handler).toHaveBeenCalledTimes(2);
});

test("Ready catch-up belongs to its leg while a probe is pending",async()=>{
 const r=await fixture(),rt=r as any;const held=deferred();
 vi.spyOn(r,"refresh").mockImplementationOnce(()=>held.promise);const history=vi.spyOn(rt,"fetchEvents").mockResolvedValue(undefined);r.events("s");history.mockClear();
 const ready=rt.onFrame({case:"ready",value:{}});await Promise.resolve();rt.generation++;
 held.resolve();await ready;expect(history).toHaveBeenCalledWith("s");
});

test("Forward recovery re-seals original bytes and scope after the verified head changes",async()=>{
 const r=await fixture(),rt=r as any,first=deferred();const raw=Uint8Array.from(atob(payload.plain),c=>c.charCodeAt(0));
 const slot=restoreSlot({...newSlot(),prepared:{machineId:r.machine,sid:"",plain:raw}},false);
 const requests:any[]=[];const plain:Uint8Array[]=[];
 rt.api={forward:async(q:any)=>{requests.push(q);const h=parseHeader(new Uint8Array(q.command.securityHeader));
  if(requests.length===1){first.resolve();return new Promise(()=>{});}
  const opened=await r.world.fixture.open(r.machine,q.command);plain.push(unpackSecurity(h,opened.plaintext));
  return {result:await r.seal({kind:6,requestId:h.request},CommandResultSchema,create(CommandResultSchema))};
 }};
 const call=r.machineCommand(r.machine,fromBinary(CommandSchema,raw).kind,slot);await first.promise;
 await webMember(r.world);await r.security.sync();rt.recovery.ready();await expect(call).resolves.toBeDefined();
 const [old,fresh]=requests.map(q=>parseHeader(new Uint8Array(q.command.securityHeader)));
 expect(fresh.contentEpoch).toBeGreaterThan(old.contentEpoch);expect(plain).toEqual([raw]);
 for(const key of ["machine","session","request","deadline"] as const)expect(fresh[key]).toEqual(old[key]);
 expect(requests[1].attemptId).not.toBe(requests[0].attemptId);
});

test("a list retry fires while the main path is probing",async()=>{
 const r=await fixture(),rt=r as any;rt.api={listRemoteSessions:async()=>{throw new Error("temporary list failure")}};
 vi.useFakeTimers();await r.refresh();r.conn="connecting";
 const refresh=vi.spyOn(r,"refresh").mockResolvedValue(undefined);
 await vi.advanceTimersByTimeAsync(2001);expect(refresh).toHaveBeenCalledOnce();
});
