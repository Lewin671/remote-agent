// Explicit local-only, quota-consuming native CLI driver. Skipped in ordinary checks.
import { test } from "vitest";
import { readFileSync, writeFileSync, unlinkSync, realpathSync } from "node:fs";
import { createServer } from "node:http";
import { fromJson, toJson } from "@bufbuild/protobuf";
import { Remote, CommandError, restoreSlot, type RequestSlot } from "../remote";
import { CommandSchema, CommandResultSchema } from "../gen/remoteagent/v1/remote_pb";
import { MemorySecurityStorage, recordFromJSON } from "./security-fixture";
import { concat, fromHex } from "../crypto";

test.skipIf(!process.env.RA_TRANSPORT_WEB)("serve the real Web Remote RPC path",async()=>{
 const root=process.env.RA_DEV_DIR!;
 if(!realpathSync(root).startsWith("/private/tmp/") || process.env.REMOTE_AGENT_HOME!==root+"/bootstrap") throw Error("isolated bootstrap required");
 const data=JSON.parse(readFileSync(root+"/web-bootstrap.json","utf8"));
 const {record}=await recordFromJSON(data.record);
 const seed=Uint8Array.from(Buffer.from(data.seed,"base64"));
 const key=await crypto.subtle.importKey("pkcs8",concat(fromHex("302e020100300506032b657004220420"),seed),"Ed25519",false,["sign"]);
 class DriverRemote extends Remote { constructor(){super({server:data.server,deviceId:record.memberID,devicePrivate:key},undefined,new MemorySecurityStorage(record));} }
 const remote=new DriverRemote();
 const runtime=remote as unknown as {token:string; trustedMembers:Map<string,unknown>;forward(machine:string,sid:string,cmd:unknown,slot:RequestSlot):Promise<import("../gen/remoteagent/v1/remote_pb").CommandResult>};
 runtime.token=data.token;
 runtime.trustedMembers=new Map(record.states.at(-1)!.core.members.map(m=>[m.id,m]));
 const slots=new Map<string,RequestSlot>();
 let finish!:()=>void;const stopped=new Promise<void>(resolve=>{finish=resolve});
 const server=createServer(async(req,res)=>{
  if(req.headers["x-transport-test"]!==readFileSync(root+"/control-token","utf8").trim()){res.writeHead(401).end();return;}
  if(req.url==="/stop"){res.end("{}");finish();return;}
  try{
   const chunks:Buffer[]=[];for await(const chunk of req)chunks.push(Buffer.from(chunk));
   const q=JSON.parse(Buffer.concat(chunks).toString());
   const cmd=fromJson(CommandSchema,q.command);
   const slot=slots.get(q.id)??restoreSlot({requestId:q.id,deadline:q.deadline,prepared:undefined},true);slots.set(q.id,slot);
   const result=await runtime.forward(q.machine,q.session,cmd.kind,slot);
   res.setHeader("Content-Type","application/json");res.end(JSON.stringify({result:toJson(CommandResultSchema,result)}));
  }catch(error){
   const e=error as CommandError;
   res.setHeader("Content-Type","application/json");res.end(JSON.stringify({Code:e.code??"unknown_outcome",Message:e.message,Outcome:e.outcome??"unknown",ConflictKind:e.conflictKind??"",FromDaemon:e.fromDaemon??false,ResultAvailable:e.resultAvailable??false}));
  }
 });
 await new Promise<void>(resolve=>server.listen(0,"127.0.0.1",resolve));
 const address=server.address();if(!address||typeof address==="string")throw Error("listener missing");
 writeFileSync(root+"/web-client-url",`http://127.0.0.1:${address.port}`,{mode:0o600});
 try {await stopped;}finally{remote.stop();await new Promise<void>(resolve=>server.close(()=>resolve()));unlinkSync(root+"/web-client-url");}
},7_200_000);
