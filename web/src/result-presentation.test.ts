import {test,expect,vi} from "vitest";
import {create} from "@bufbuild/protobuf";
import {SyntheticRemote} from "./test/remote-fixture";
import {CommandResultSchema} from "./gen/remoteagent/v1/remote_pb";
import {type Envelope} from "./gen/remoteagent/v1/envelope_pb";
import {NotForwardedSchema} from "./gen/remoteagent/v1/relay_pb";
import {Code,ConnectError} from "@connectrpc/connect";
import {requestIdentity,parseHeader} from "./device-security";
import {unpackSecurity} from "./device-security-client";
import {newSlot,uncertain} from "./remote";
import * as l10n from "./i18n";

test.each(["executed","identity"])("refreshes without renewing on %s",async kind=>{
 const r=await SyntheticRemote.create();
 try{
  const refresh=vi.spyOn(r,"refresh").mockResolvedValue();
  const live=vi.spyOn(r,"refreshLiveSnapshot").mockResolvedValue();
  (r as unknown as {api:unknown}).api={forward:async(q:{command:Envelope;attemptId:string})=>{
   const original=await r.world.fixture.open(r.machine,q.command);
   const h=parseHeader(new Uint8Array(q.command.securityHeader)),binding=await requestIdentity(h,unpackSecurity(h,original.plaintext));
   const proof=create(CommandResultSchema,{errorCode:"disk_full",outcome:kind==="identity"?"conflict":"executed",reason:kind==="identity"?"identity_conflict":"result_unavailable",conflictKind:kind==="identity"?"identity":"",requestKeyHash:binding.key,payloadHash:binding.payload,resultAvailable:false});
   return {result:await r.seal({kind:6,sessionId:h.session,requestId:h.request},CommandResultSchema,proof),outcome:proof.outcome,reason:proof.reason,attemptId:q.attemptId};
  }};
  const slot=newSlot();
  const error=await r.command("s",{case:"send",value:{sessionId:"s",text:"fixture"}},slot).catch(e=>e);
  expect(error.message).toBe(l10n.t(kind==="identity"?"errors.request_identity_conflict":"errors.executed_result_unavailable"));
  expect(error.outcome).toBe(kind==="identity"?"conflict":"executed");expect(uncertain(error)).toBe(true);
  expect(refresh).toHaveBeenCalledOnce();expect(live).toHaveBeenCalledWith("s");
  vi.spyOn(r,"sendMessage").mockRejectedValue(error);
  const sent=r.send("s","fixture");
  await vi.waitFor(()=>expect(r.outbox("s").find(o=>o.sendId===sent)?.status).toBe("failed"));
  expect(r.outbox("s").find(o=>o.sendId===sent)).toMatchObject({outcomeNotice:true,unknown:true,error:error.message});
  const upload=vi.spyOn(r,"machineCommand").mockRejectedValue(error);
  await expect(r.uploadImage(r.machine,{bytes:new Uint8Array([1,2,3]),format:"png"})).rejects.toBe(error);
  expect(upload).toHaveBeenCalledOnce();
 }finally{await r.dispose();}
});
test("relay pre-forward proof binds this attempt and cannot override earlier dispatch",async()=>{
 const r=await SyntheticRemote.create();
 try{
  let prior=false;
  (r as unknown as {api:unknown}).api={forward:async(q:{attemptId:string})=>{
   if(prior)throw new ConnectError("no proof",Code.PermissionDenied);
   throw new ConnectError("offline",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:create(NotForwardedSchema,{attemptId:q.attemptId,reason:"machine_offline",relayEpoch:"fixture-epoch"})}]);
  }};
  const slot=newSlot();
  let error=await r.machineCommand(r.machine,{case:"listModels",value:{}},slot).catch(e=>e);
  expect(error.outcome).toBe("not_sent");expect(uncertain(error)).toBe(false);
  prior=true;error=await r.machineCommand(r.machine,{case:"listModels",value:{}},slot).catch(e=>e);expect(uncertain(error)).toBe(true);
  prior=false;error=await r.machineCommand(r.machine,{case:"listModels",value:{}},slot).catch(e=>e);expect(uncertain(error)).toBe(true);
 }finally{await r.dispose();}
});

test("a pre-forward refusal cannot erase a concurrent attempt",async()=>{
 const r=await SyntheticRemote.create();
 try{
  const waiting:{id:string;reject:(error:unknown)=>void}[]=[];
  (r as unknown as {api:unknown}).api={forward:(q:{attemptId:string})=>new Promise((_resolve,reject)=>waiting.push({id:q.attemptId,reject}))};
  const slot=newSlot(),cmd={case:"listModels" as const,value:{}};
  const first=r.machineCommand(r.machine,cmd,slot).catch(e=>e);
  const second=r.machineCommand(r.machine,cmd,slot).catch(e=>e);
  await vi.waitFor(()=>expect(waiting).toHaveLength(2));
  waiting[0].reject(new ConnectError("offline",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:{attemptId:waiting[0].id,reason:"machine_offline",relayEpoch:"fixture-epoch"}}]));
  expect(uncertain(await Promise.race([first,second]))).toBe(true);
  waiting[1].reject(new ConnectError("no proof",Code.PermissionDenied));
  for(const error of await Promise.all([first,second])) expect(uncertain(error)).toBe(true);
  const third=r.machineCommand(r.machine,cmd,slot).catch(e=>e);
  await vi.waitFor(()=>expect(waiting).toHaveLength(3));
  waiting[2].reject(new ConnectError("offline",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:{attemptId:waiting[2].id,reason:"machine_offline",relayEpoch:"fixture-epoch"}}]));
  expect(uncertain(await third)).toBe(true);
 }finally{await r.dispose();}
});

test("a restored unknown send keeps earlier evidence after a relay refusal",async()=>{
 const r=await SyntheticRemote.create();
 try{
  (r as unknown as {api:unknown}).api={forward:async(q:{attemptId:string})=>{
   throw new ConnectError("offline",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:{attemptId:q.attemptId,reason:"machine_offline",relayEpoch:"fixture-epoch"}}]);
  }};
  const slot=newSlot();
  const error=await r.sendMessage("s","original","stable-send",slot,true).catch(e=>e);
  expect(error.outcome).toBe("unknown");expect(uncertain(error)).toBe(true);
  const missing=await r.sendMessage("missing","original","stable-send",slot,true).catch(e=>e);
  expect(missing.outcome).toBe("unknown");expect(uncertain(missing)).toBe(true);
 }finally{await r.dispose();}
});


test("a full relay queue keeps its own unsent reason",async()=>{
 const r=await SyntheticRemote.create();
 try {
  (r as unknown as {api:unknown}).api={forward:async(q:{attemptId:string})=>{throw new ConnectError("queue is full",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:{attemptId:q.attemptId,reason:"queue_full",relayEpoch:"fixture-epoch"}}]);}};
  const error=await r.machineCommand(r.machine,{case:"listModels",value:{}},newSlot()).catch(e=>e);
  expect(error.outcome).toBe("not_sent");expect(error.message).toBe("queue is full");
 }finally{await r.dispose();}
});
