import { IDBFactory } from "fake-indexeddb";
import { vi } from "vitest";
import type { Event } from "../gen/remoteagent/v1/event_pb";
import vectors from "../../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex } from "../crypto";
import { parseLease, parseRecoveryStatement, parseState, unb64 } from "../device-security";
import { initializeSecurity } from "../device-security-store";

// Parse and import the immutable Go-generated fixture once per test file.
// Each test still installs it into a fresh database and controls its clock.
let fixture: ReturnType<typeof buildFixture> | undefined;
export function securityFixture() {
  return fixture ??= buildFixture();
}
async function buildFixture() {
  const [genesis, added, renewal, removed] = await Promise.all(
    [vectors.genesis, vectors.added, vectors.renewal, vectors.recovery]
      .map(v => parseState(unb64(v.state))),
  );
  const x = await crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b656e04220420"), fromHex(vectors.phone_x),
  ), "X25519", false, ["deriveBits"]);
  const signingKey = (seed: string) => crypto.subtle.importKey("pkcs8", concat(
    fromHex("302e020100300506032b657004220420"), fromHex(seed),
  ), "Ed25519", false, ["sign"]);
  const [ed, machineEd] = await Promise.all([
    signingKey(vectors.phone_seed), signingKey(vectors.machine_seed),
  ]);
  const lease = parseLease(unb64(vectors.lease));
  const pin = { account: vectors.account, domain: fromHex(vectors.domain), genesis: fromHex(vectors.genesis.hash) };
  const memberID = genesis.core.authorityID;
  return { pin, states: [genesis, added], memberID, x, ed, machineEd, lease,
    removal: {
      states: [renewal, removed],
      proofs: [undefined, await parseRecoveryStatement(unb64(vectors.recovery_statement))],
      now: Number(removed.core.issuedAt),
    },
  };
}
export async function storedSecurity() {
  const f = await securityFixture();
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(Number(f.lease.issuedAt));
  await initializeSecurity(f.pin, f.states, [undefined, undefined], f.memberID, f.x, false, f.lease);
  return f;
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

export async function storedEvent(f: Awaited<ReturnType<typeof storedSecurity>>, seq = 1n, session = "synthetic-session", kind?: Event["kind"]) {
  const { formCipher, stateHash, toEnvelope } = await import("../device-security");
  const { create, toBinary } = await import("@bufbuild/protobuf");
  const { EventSchema } = await import("../gen/remoteagent/v1/event_pb");
  const state = f.states.at(-1)!;
  const machine = state.core.members.find(m => m.kind === 1)!.id;
  const seed = new Uint8Array(32).fill(7);
  const envelope = toEnvelope(await formCipher(fromHex(vectors.content_secret_2), seed, {
    account: f.pin.account, domain: f.pin.domain, epoch: state.core.epoch,
    state: await stateHash(state), contentEpoch: state.core.contentEpoch, sender: machine,
    kind: 1, purpose: 1, machine, session, seq,
    request: "", deadline: 0n, compression: 0,
  }, new Uint8Array(toBinary(EventSchema, create(EventSchema, {
    sessionId: session, seq, kind: kind ?? { case: "agentMessage", value: { text: "synthetic event " + seq } },
  }))), f.machineEd));
  return { envelope, seed };
}
