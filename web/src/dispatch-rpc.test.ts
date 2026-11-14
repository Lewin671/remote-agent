import {expect,test,vi} from "vitest";
import {create} from "@bufbuild/protobuf";
import {Code,ConnectError} from "@connectrpc/connect";
import {SyntheticRemote} from "./test/remote-fixture";
import {newSlot,CommandError} from "./remote";
import {NotForwardedSchema} from "./gen/remoteagent/v1/relay_pb";
import {CommandResultSchema} from "./gen/remoteagent/v1/remote_pb";
import {parseHeader} from "./device-security";
import type {Envelope} from "./gen/remoteagent/v1/envelope_pb";
function gate<T>() {let resolve!:(v:T)=>void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve};}

test("an async preflight failure cannot retire a concurrently dispatched copy",async()=>{
 const r=await SyntheticRemote.create();
 const signing=gate<void>(),reply=gate<void>();
 const holder=r as unknown as {sealSecureMessage:(...args:any[])=>Promise<unknown>;api:unknown};
 const original=holder.sealSecureMessage.bind(r);
 const seal=vi.spyOn(holder,"sealSecureMessage").mockImplementation(original);
 // Apply the one-use failure after the default implementation is installed.
 seal.mockImplementationOnce(async()=>{await signing.promise;throw Error("preflight failed");});
 let delivered=0;
 holder.api={forward:async(q:{command:Envelope})=>{
  delivered++;await reply.promise;
  const h=parseHeader(new Uint8Array(q.command.securityHeader));
  return {result:await r.seal({kind:6,requestId:h.request},CommandResultSchema,create(CommandResultSchema))};
 }};
 try {
  const slot=newSlot(),cmd={case:"createSession" as const,value:{cwd:"/work/example"}};
  const first=r.machineCommand(r.machine,cmd,slot).catch(e=>e);
  const second=r.machineCommand(r.machine,cmd,slot);
  await vi.waitFor(()=>expect(delivered).toBe(1));
  signing.resolve();
  expect(await first).toMatchObject({outcome:"unknown"});
  reply.resolve();await second;
  expect(delivered).toBe(1);
 } finally {signing.resolve();reply.resolve();seal.mockRestore();await r.dispose();}
});

test("NotForwarded must bind the frozen body epoch as well as attempt ID",async()=>{
 const r=await SyntheticRemote.create();
 try {
  (r as unknown as {api:unknown}).api={forward:async(q:{attemptId:string;relayEpoch:string})=>{
   expect(q.relayEpoch).toBe("fixture-epoch");
   throw new ConnectError("refused",Code.Unavailable,undefined,[{desc:NotForwardedSchema,value:{attemptId:q.attemptId,relayEpoch:"another-instance",reason:"machine_offline"}}]);
  }};
  const result=await r.machineCommand(r.machine,{case:"createSession",value:{cwd:"/work/example"}}).catch(e=>e);
  expect(result).toBeInstanceOf(CommandError);expect(result.outcome).toBe("unknown");
 } finally {await r.dispose();}
});

test("outbox settlement retains live evidence when a raw error follows the RPC",async()=>{
 const r=await SyntheticRemote.create();
 const original=r.sendMessage.bind(r);
 let id="";
 const send=vi.spyOn(r,"sendMessage").mockImplementation(async(...args)=>{
  id=args[3].requestId;
  await original(...args);
  throw Error("canceled at UI handoff");
 });
 (r as unknown as {api:unknown}).api={forward:async(q:{command:Envelope})=>{
  const h=parseHeader(new Uint8Array(q.command.securityHeader));
  return {result:await r.seal({kind:6,sessionId:"s",requestId:h.request},CommandResultSchema,create(CommandResultSchema))};
 }};
 try {
  const sent=r.send("s","original");
  await vi.waitFor(()=>expect(r.outbox("s")[0].status).toBe("failed"));
  expect(r.outbox("s")[0]).toMatchObject({sendId:sent,requestId:id,unknown:true,text:"original"});
 } finally {send.mockRestore();await r.dispose();}
});
