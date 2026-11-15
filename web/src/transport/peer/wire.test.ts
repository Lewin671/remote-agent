import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import * as w from "./wire";
import { knownPayload } from "./schema";
import { validateSDP } from "./sdp";
import { fixture, fixtureCodec } from "../../test/peer/fixture";
import { replay } from "../../test/peer/replay";

const root = new URL("../../../../testdata/transport/", import.meta.url);
describe("production protocol 16 peer codec", () => {
  it("authenticates the frozen cross-language vectors without changing their bytes", async () => {
    const corpus = JSON.parse(readFileSync(new URL("wire/vectors.json", root), "utf8")) as {
      now_ms: number; sender: string; receiver: string;
      rows: { name: string; packet_hex: string; accept: boolean; type: number; payload_hex: string }[];
    };
    const codec = await fixtureCodec(), a = await fixture(corpus.sender, 1), b = await fixture(corpus.receiver, 2);
    for (const row of corpus.rows) {
      const raw = w.unhex(row.packet_hex);
      if (!row.accept) { await expect(codec.open(raw, a, b, corpus.now_ms), row.name).rejects.toThrow(); continue; }
      const opened = await codec.open(raw, a, b, corpus.now_ms);
      expect(opened.type, row.name).toBe(row.type);
      expect(w.hex(opened.payload), row.name).toBe(row.payload_hex);
      expect(opened.raw, row.name).toEqual(raw);
    }
    expect(corpus.rows).toHaveLength(27);
  });
  it("authenticates the full capability snapshot and rejects expiry, wrong binding and old instances", async () => {
    const rows = JSON.parse(readFileSync(new URL("wire/capability-snapshot.json", root), "utf8")) as {name:string;packet_hex:string;accept:boolean}[];
    const codec=await fixtureCodec(), machine=await fixture("machine",2), client=await fixture("client",1), now=1700000000000;
    for (const row of rows) {
      const open=async () => { const o=await codec.open(w.unhex(row.packet_hex),machine,client,now); knownPayload(2,14,o.payload);const f=w.fields(o.payload,"CapabilitySnapshot"), expires=w.getUint(f,3); if(expires<=BigInt(now)||expires<=o.context.issued||expires-o.context.issued>60000n) throw new Error("expired");return o; };
      if(row.accept) expect((await open()).type).toBe(14); else await expect(open(),row.name).rejects.toThrow();
    }
  });
  it("validates every frozen native SDP original and negative fixture", () => {
    const rows = JSON.parse(readFileSync(new URL("sdp/manifest.json", root), "utf8")) as { file: string; accept: boolean }[];
    for (const row of rows) {
      const raw = new Uint8Array(readFileSync(new URL("sdp/" + row.file, root)));
      if (row.accept) expect(() => validateSDP(raw), row.file).not.toThrow();
      else expect(() => validateSDP(raw), row.file).toThrow();
    }
    expect(rows).toHaveLength(45);
  });
  it("runs the frozen sender, bootstrap, replay and deferred authorization trace", async () => {
    expect(await replay(readFileSync(new URL("signal/scenarios.tsv", root), "utf8"))).toEqual({ signal_scenarios: 150, assertions: 2103 });
  });
  it("keeps accounts and member heads isolated between concurrent codecs", async () => {
    const codec = await fixtureCodec(), a = await fixture("client", 1), b = await fixture("machine", 2), now = 1700000000000;
    const scope = { account: "0123456789abcdef0123456789abcdef", accountDomain: await w.hash(w.utf8("lab/account-domain")), head: await w.hash(w.utf8("lab/member-head/1")), epoch: 1n };
    const context = w.context(a, b, (await w.hash(w.utf8("connection"))).slice(0, 16), 1, 1, 1, now);
    const raw = await codec.seal(a, b, context, 31337, w.utf8("opaque"));
    await expect(codec.open(raw, a, b, now)).resolves.toMatchObject({ type: 31337 });
    for (const change of [{ account: "1123456789abcdef0123456789abcdef" }, { epoch: 2n }, { head: await w.hash(w.utf8("different")) }, { accountDomain: await w.hash(w.utf8("different")) }]) {
      await expect(w.codec({ ...scope, ...change }).open(raw, a, b, now)).rejects.toThrow();
    }
    await expect(codec.open(raw, a, b, now)).resolves.toMatchObject({ type: 31337 });
  });
});

it("reads a capability with repeated type lists without changing its bytes",()=>{
 const raw=w.pb(w.b(1,new Uint8Array(16)),w.u(2,1),w.u(2,14),w.u(3,1),w.u(3,9),w.u(4,1700000060000n),w.u(5,1),w.u(6,1048576));
 knownPayload(2,1,raw);const f=w.fields(raw,"Capabilities");expect(w.getUint(f,5)).toBe(1n);expect(w.getUint(f,4)).toBe(1700000060000n);
});
