import { IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";
import vectors from "../../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex } from "../crypto";
import { parseLease, parseState, unb64 } from "../device-security";
import { initializeSecurity } from "../device-security-store";

export async function storedSecurity() {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const states = await Promise.all([vectors.genesis, vectors.added].map(v => parseState(unb64(v.state))));
  const x = await crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b656e04220420"), fromHex(vectors.phone_x),
  ), "X25519", false, ["deriveBits"]);
  const ed = await crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b657004220420"), fromHex(vectors.phone_seed),
  ), "Ed25519", false, ["sign"]);
  const lease = parseLease(unb64(vectors.lease));
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(Number(lease.issuedAt));
  const pin = { account: vectors.account, domain: fromHex(vectors.domain), genesis: fromHex(vectors.genesis.hash) };
  const memberID = states[0].core.authorityID;
  await initializeSecurity(pin, states, [undefined, undefined], memberID, x, false, lease);
  return { pin, states, memberID, x, ed, lease };
}

/** Separate IDB connection/module callers model a second tab. */
export async function rawSecurity<T>(mode: IDBTransactionMode, op: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open("remote-agent-security-v3", 1);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return new Promise<T>((resolve, reject) => {
    const tx = db.transaction("security", mode);
    const req = op(tx.objectStore("security"));
    tx.oncomplete = () => { db.close(); resolve(req.result); };
    tx.onabort = tx.onerror = () => { db.close(); reject(tx.error); };
  });
}

export async function storedEvent(f: Awaited<ReturnType<typeof storedSecurity>>, seq = 1n) {
  const { formCipher, stateHash, toEnvelope } = await import("../device-security");
  const { create, toBinary } = await import("@bufbuild/protobuf");
  const { EventSchema } = await import("../gen/remoteagent/v1/event_pb");
  const state = f.states.at(-1)!;
  const machine = state.core.members.find(m => m.kind === 1)!.id;
  const ed = await crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b657004220420"), fromHex(vectors.machine_seed),
  ), "Ed25519", false, ["sign"]);
  const seed = new Uint8Array(32).fill(7);
  const envelope = toEnvelope(await formCipher(fromHex(vectors.content_secret_2), seed, {
    account: f.pin.account, domain: f.pin.domain, epoch: state.core.epoch,
    state: await stateHash(state), contentEpoch: state.core.contentEpoch, sender: machine,
    kind: 1, purpose: 1, machine, session: "synthetic-session", seq,
    request: "", deadline: 0n, compression: 0,
  }, new Uint8Array(toBinary(EventSchema, create(EventSchema, {
    sessionId: "synthetic-session", seq, kind: { case: "agentMessage", value: { text: "synthetic event " + seq } },
  }))), ed));
  return { envelope, seed };
}
