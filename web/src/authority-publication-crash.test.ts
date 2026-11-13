import { IDBFactory } from "fake-indexeddb";
import { expect, it, vi } from "vitest";
import { fromBinary } from "@bufbuild/protobuf";
import { readSecurityOperation } from "./security-operation";
import * as securityStore from "./device-security-store";
import { loadSecurity } from "./device-security-store";
import { stateBytes } from "./device-security";
import { PutSecurityStateRequestSchema } from "./gen/remoteagent/v1/relay_pb";
import { createAccount, clients, resetSetup, restartServer } from "./test/authority-workflow-fixture";

it.each(["before", "after"] as const)("keeps a single successor across every state-publication write and RPC (%s)", async (side) => {
  let events = 0, failure = -1, fired = false, monitor = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    if (!monitor) return work();
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry, install = securityStore.installSecurity, touch = securityStore.touchSecurity;
  const fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "installSecurity").mockImplementation((...args) => step(() => install(...args))),
    vi.spyOn(securityStore, "touchSecurity").mockImplementation((...args) => step(() => touch(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  let count = 1;
  try {
    for (let index = -1; index < count; index++) {
      monitor = false; await restartServer();
      vi.stubGlobal("indexedDB", new IDBFactory()); resetSetup();
      const { account } = await createAccount(), c = await clients(account);
      await c.authority.prepareRecovery();
      events = 0; failure = index; fired = false; monitor = true;
      try { await c.authority.publishRecovery(); } catch { /* Restart. */ }
      monitor = false;
      if (index === -1) count = events; else expect(fired).toBe(true);
      const pending = await readSecurityOperation(); const signed = pending?.signed;
      if (pending) {
        if (!pending.codeSaved) await c.authority.publishRecovery(); else await c.authority.resumeSecurityOperation();
        if (signed) {
          const state = fromBinary(PutSecurityStateRequestSchema, signed).states[0].state;
          expect(await stateBytes((await loadSecurity()).states.at(-1)!)).toEqual(state);
        }
      }
      if (!pending && (await loadSecurity()).states.length === 1) {
        // Crash before claiming the slot: no publication was possible. The
        // display-only code is lost, so explicitly prepare and save a new one.
        await c.authority.prepareRecovery(); await c.authority.publishRecovery();
      }
      expect(await readSecurityOperation()).toBeUndefined(); expect((await loadSecurity()).states).toHaveLength(2);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);
