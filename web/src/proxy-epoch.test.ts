import {afterEach,expect,test,vi} from "vitest";
import {create} from "@bufbuild/protobuf";
import {createServer as httpServer} from "node:http";
import type {AddressInfo} from "node:net";
import {fileURLToPath} from "node:url";
import {createServer as viteServer} from "vite";
import {Remote,apiBase} from "./remote";
import {relayFetch} from "./transport";
import {SyntheticRemote} from "./test/remote-fixture";
import {CommandResultSchema} from "./gen/remoteagent/v1/remote_pb";
import {parseHeader} from "./device-security";
import type {Envelope} from "./gen/remoteagent/v1/envelope_pb";

afterEach(()=>{vi.unstubAllGlobals();vi.unstubAllEnvs();});
function useProductionDiscovery(r:Remote) {
 const discover=(Remote.prototype as unknown as {relayEpoch:()=>Promise<string>}).relayEpoch;
 Object.defineProperty(r,"relayEpoch",{value:()=>discover.call(r)});
}

test.each([true,false])("proxy-only command uses effective API origin (cached=%s)",async(warm)=>{
 vi.stubEnv("VITE_SAME_ORIGIN_API","1");
 const page=`https://proxy-${warm?"warm":"cold"}.example`;
 vi.stubGlobal("location",new URL(page));
 const fetch=vi.fn(async(input:RequestInfo|URL)=>{
  const url=new URL(String(input));
  if(url.origin!==page)throw Error("direct relay requests are blocked");
  return new Response("ok",{headers:{"Remote-Agent-Relay-Epoch":"proxied-epoch"}});
 });
 vi.stubGlobal("fetch",fetch);
 const r=await SyntheticRemote.create();useProductionDiscovery(r);
 let calls=0;
 (r as unknown as {api:unknown}).api={forward:async(q:{command:Envelope;relayEpoch:string})=>{
  calls++;expect(q.relayEpoch).toBe("proxied-epoch");
  const header=parseHeader(new Uint8Array(q.command.securityHeader));
  return {result:await r.seal({kind:6,requestId:header.request},CommandResultSchema,create(CommandResultSchema))};
 }};
 try {
  expect(apiBase(r.account.server)).toBe(page);
  if(warm)await relayFetch(page+"/remoteagent.v1.AuthService/Login",{method:"POST"});
  await expect(r.machineCommand(r.machine,{case:"createSession",value:{cwd:"/work/example"}})).resolves.toBeDefined();
  expect(calls).toBe(1);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(String(fetch.mock.calls[0][0])).toBe(page+(warm?"/remoteagent.v1.AuthService/Login":"/healthz"));
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
