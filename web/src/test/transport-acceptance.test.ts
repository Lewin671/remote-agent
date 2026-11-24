// Explicit local-only, quota-consuming native CLI driver. Skipped in ordinary checks.
import { test } from "vitest";
import { readFileSync, writeFileSync, unlinkSync, realpathSync } from "node:fs";
import { createServer } from "node:http";
import { fromJson } from "@bufbuild/protobuf";
import { Remote } from "../remote";
import { getCoreHost } from "../core/host";
import { CommandSchema } from "../gen/remoteagent/v1/remote_pb";
import { createTestSecurityStore } from "./security-fixture";

test.skipIf(!process.env.RA_TRANSPORT_WEB)("serve the real Web Remote RPC path",async()=>{
 const root=process.env.RA_DEV_DIR!;
 if(!realpathSync(root).startsWith("/private/tmp/") || process.env.REMOTE_AGENT_HOME!==root+"/bootstrap") throw Error("isolated bootstrap required");
 const data=JSON.parse(readFileSync(root+"/web-bootstrap.json","utf8"));
 const seed=Uint8Array.from(Buffer.from(data.seed,"base64"));
 const { store, record } = await createTestSecurityStore(data.record, seed, data.server);
 class DriverRemote extends Remote { constructor(){super({server:data.server,deviceId:record.memberID,accountId:record.pin.account},undefined,undefined,undefined,undefined,store);} }
 const remote=new DriverRemote();
 const host=getCoreHost(remote);
 await host.securityClient.bootstrap();
 (remote as unknown as {token:string}).token=data.token;
 let finish!:()=>void;const stopped=new Promise<void>(resolve=>{finish=resolve});
 const server=createServer(async(req,res)=>{
  if(req.headers["x-transport-test"]!==readFileSync(root+"/control-token","utf8").trim()){res.writeHead(401).end();return;}
  if(req.url==="/stop"){res.end("{}");finish();return;}
  try{
   const chunks:Buffer[]=[];for await(const chunk of req)chunks.push(Buffer.from(chunk));
   const q=JSON.parse(Buffer.concat(chunks).toString());
   const cmd=fromJson(CommandSchema,q.command);
   await host.runMachineCommand(q.machine,q.session,cmd.kind);
   res.setHeader("Content-Type","application/json");res.end(JSON.stringify({ok:true}));
  }catch(error){
   res.setHeader("Content-Type","application/json");res.end(JSON.stringify({Code:"unknown_outcome",Message:error instanceof Error?error.message:String(error)}));
  }
 });
 await new Promise<void>(resolve=>server.listen(0,"127.0.0.1",resolve));
 const address=server.address();if(!address||typeof address==="string")throw Error("listener missing");
 writeFileSync(root+"/web-client-url",`http://127.0.0.1:${address.port}`,{mode:0o600});
 try {await stopped;}finally{remote.stop();await new Promise<void>(resolve=>server.close(()=>resolve()));unlinkSync(root+"/web-client-url");}
},7_200_000);
