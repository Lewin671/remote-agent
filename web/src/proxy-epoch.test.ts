import {afterEach,expect,test,vi} from "vitest";
import {create} from "@bufbuild/protobuf";
import {createServer as httpServer} from "node:http";
import type {AddressInfo} from "node:net";
import {fileURLToPath} from "node:url";
import {createServer as viteServer} from "vite";
import {apiBase} from "./remote";
import {getCoreHost} from "./core/host";
import { SyntheticRemote, remoteConnState } from "./test/remote-fixture";
import {CommandResultSchema} from "./gen/remoteagent/v1/remote_pb";
import { parseTestHeader } from "./test/stored-security-fixture";
import type {Envelope} from "./gen/remoteagent/v1/envelope_pb";

afterEach(()=>{vi.unstubAllGlobals();vi.unstubAllEnvs();});

test.each([true,false])("proxy-only command uses effective API origin (cached=%s)",async(warm)=>{
 vi.stubEnv("VITE_SAME_ORIGIN_API","1");
 const page=`https://proxy-${warm?"warm":"cold"}.example`;
 vi.stubGlobal("location",new URL(page));
 const healthzCalls: string[] = [];
 const r=await SyntheticRemote.create();
 r.mockRelayEpoch = warm ? "proxied-epoch" : "";
 let calls=0;
 (r as unknown as {token:string;tokenExpires:number;openStreamLeg:(id:bigint,t:string,cb:{onReady:(id:bigint,ms:bigint)=>void})=>void}).token="tok";
 (r as unknown as {tokenExpires:number}).tokenExpires=Date.now()+7*86_400_000;
 (r as unknown as {openStreamLeg:(id:bigint,t:string,cb:{onReady:(id:bigint,ms:bigint)=>void})=>void}).openStreamLeg=(legId,_t,cb)=>cb.onReady(legId,BigInt(Date.now()));
 (r as unknown as {api:unknown}).api={
  healthz: async (url: string) => {
   const parsed = new URL(url);
   if (parsed.origin !== page) throw Error("direct relay requests are blocked");
   healthzCalls.push(url);
   return new Response("ok", { headers: { "Remote-Agent-Relay-Epoch": "proxied-epoch" } });
  },
  listMachines:async()=>({machines:[{id:r.machine,online:true,lastSeenMs:0n}]}),
  listRemoteSessions:async()=>({sessions:[]}),
  listDevices:async()=>({devices:[]}),
  getAccountPrefs:async()=>({}),
  forward:async(q:{command:Envelope;relayEpoch:string})=>{
   calls++;expect(q.relayEpoch).toBe("proxied-epoch");
   const header=parseTestHeader(new Uint8Array(q.command.securityHeader));
   return {result:await r.seal({kind:6,requestId:header.request},CommandResultSchema,create(CommandResultSchema))};
  },
 };
 try {
  r.start();
  await vi.waitFor(()=>expect(remoteConnState(r)).toBe("online"));
  expect(apiBase(r.account.server)).toBe(page);
  await expect(getCoreHost(r).pingMachine(r.machine)).resolves.toBeUndefined();
  expect(calls).toBe(1);
  expect(healthzCalls).toEqual(warm ? [] : [page + "/healthz"]);
 } finally {await r.dispose();}
});

test("Vite proxies cold health discovery for dev and preview configuration",async()=>{
 let hits=0;
 const upstream=httpServer((req,res)=>{
  if(req.url!=="/healthz"){res.writeHead(404).end();return}
  hits++;res.setHeader("Remote-Agent-Relay-Epoch","proxy-instance");res.end("ok");
 });
 await new Promise<void>(resolve=>upstream.listen(0,"127.0.0.1",resolve));
 const target=`http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;
 vi.stubEnv("RA_PROXY",target);
 const server=await viteServer({root:fileURLToPath(new URL("..",import.meta.url)),configFile:fileURLToPath(new URL("../vite.config.ts",import.meta.url)),server:{host:"127.0.0.1",port:0,strictPort:false,hmr:false,watch:null},appType:"custom",optimizeDeps:{noDiscovery:true,include:[]},logLevel:"silent"});
 try {
  await server.listen();
  const origin=`http://127.0.0.1:${(server.httpServer!.address() as AddressInfo).port}`;
  const response=await fetch(origin+"/healthz");
  expect(response.headers.get("Remote-Agent-Relay-Epoch")).toBe("proxy-instance");
  expect(await response.text()).toBe("ok");expect(hits).toBe(1);
  expect(server.config.preview.proxy?.["/healthz"]).toMatchObject({target});
 } finally {
  await server.close();upstream.closeAllConnections();await new Promise<void>(resolve=>upstream.close(()=>resolve()));
 }
},15000);
