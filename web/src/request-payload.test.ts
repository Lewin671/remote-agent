import {test,expect,vi} from "vitest";
import {create,fromBinary,toBinary} from "@bufbuild/protobuf";
import fixture from "../../testdata/transport/prepared-command.json";
import {SyntheticRemote} from "./test/remote-fixture";
import {webMember} from "./test/security-fixture";
import {DeviceSecurityClient,unpackSecurity} from "./device-security-client";
import {parseHeader} from "./device-security";
import {CommandSchema,CommandResultSchema} from "./gen/remoteagent/v1/remote_pb";
import {type Envelope} from "./gen/remoteagent/v1/envelope_pb";
import {newSlot,restoreSlot} from "./remote";
import {ComposerDraftStore} from "./composer-drafts";
import {Tray} from "./attach";
import {t} from "./i18n";
const raw=Uint8Array.from(atob(fixture.plain),c=>c.charCodeAt(0));

test("create retries and a fresh client preserve the first payload bytes across rekey",async()=>{
 const r=await SyntheticRemote.create();let fresh:SyntheticRemote|undefined;
 try{
  const plain=fromBinary(CommandSchema,raw);expect(plain.kind.case).toBe("createSession");
  expect(toBinary(CommandSchema,plain)).not.toEqual(raw);
  const slot=restoreSlot({...newSlot(),prepared:{machineId:r.machine,sid:"",plain:raw.slice()}}, false);
  const bodies:Uint8Array[]=[];const epochs:bigint[]=[];
  function install(target:SyntheticRemote){
   (target as unknown as {api:unknown}).api={forward:async(q:{command:Envelope})=>{
    const h=parseHeader(new Uint8Array(q.command.securityHeader));const opened=await r.world.fixture.open(r.machine,q.command);
    bodies.push(unpackSecurity(h,opened.plaintext));epochs.push(h.contentEpoch);
    return {result:await target.seal({kind:6,requestId:h.request},CommandResultSchema,create(CommandResultSchema))};
   }};
  }
  install(r);
  await r.machineCommand(r.machine,plain.kind,slot);
  await webMember(r.world);
  await (r as unknown as {security:DeviceSecurityClient}).security.sync();
  await r.machineCommand(r.machine,{case:"createSession",value:{env:{different:"ignored"}}},slot);
  fresh=new SyntheticRemote(r.world,r.member,r.seed);install(fresh);
  const saved=JSON.parse(JSON.stringify({...slot,prepared:{...slot.prepared,plain:Array.from(slot.prepared!.plain)}}));
  saved.prepared.plain=new Uint8Array(saved.prepared.plain);
  await fresh.machineCommand(r.machine,plain.kind,restoreSlot(saved,true));
  expect(bodies).toHaveLength(3);for(const b of bodies)expect(b).toEqual(raw);
  expect(epochs[1]).toBeGreaterThan(epochs[0]);
  // Fresh prepares freeze once too; mutating the caller's map cannot change a retry.
  const first=newSlot();const env={z:"three",a:"one",m:"two"};
  await r.machineCommand(r.machine,{case:"createSession",value:{env}},first);
  env.z="changed";await r.machineCommand(r.machine,{case:"createSession",value:{env}},first);
  expect(bodies[4]).toEqual(bodies[3]);
 }finally{fresh?.stop();await r.dispose();}
});


test("an outbox never dispatches before the exact prepared bytes are durable", async () => {
 const r = await SyntheticRemote.create();
 const store = new ComposerDraftStore(r.account);
 const read = vi.spyOn(store, "readOutbox").mockResolvedValue([]);
 const save = vi.spyOn(store, "saveOutbox").mockRejectedValue(new Error("disk full"));
 Object.defineProperty(r, "localDrafts", {value: store});
 r.trays.set("s", new Tray(null!, null!, () => {}));
 const bodies: Uint8Array[] = [];
 (r as unknown as {api: unknown}).api = {forward: async (q: {command: Envelope}) => {
  const h = parseHeader(new Uint8Array(q.command.securityHeader));
  const opened = await r.world.fixture.open(r.machine, q.command);
  bodies.push(unpackSecurity(h, opened.plaintext));
  return {result: await r.seal({kind: 6, sessionId: "s", requestId: h.request}, CommandResultSchema, create(CommandResultSchema))};
 }};
 try {
  const id = r.send("s", "persist before forwarding");
  await vi.waitFor(() => expect(r.outbox("s")[0].status).toBe("failed"));
  expect(bodies).toHaveLength(0);
  expect(r.trays.get("s")?.note).toBe(t("errors.the_draft_could_not_be_saved_locally_keep_the"));
  const persisted: Uint8Array[] = [];
  save.mockReset().mockImplementation(async (_sid, outgoing) => {
   const plain = outgoing[0]?.prepared?.plain;
   if (plain) persisted.push(new Uint8Array(plain));
  });
  r.retrySend("s", id);
  await vi.waitFor(() => expect(r.outbox("s")[0].status).toBe("sent"));
  expect(bodies).toHaveLength(1);
  expect(persisted.some(bytes => bytes.every((b, i) => b === bodies[0][i]) && bytes.length === bodies[0].length)).toBe(true);
 } finally { read.mockRestore(); save.mockRestore(); await r.dispose(); }
});
