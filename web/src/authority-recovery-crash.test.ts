import { IDBFactory } from "fake-indexeddb";
import { expect, it, vi } from "vitest";
import { AuthoritySetup } from "./authority-setup";
import { readSecurityOperation, isAuthority } from "./security-operation";
import { loadAccount } from "./storage";
import * as securityStore from "./device-security-store";
import * as accountStore from "./storage";
import { loadSecurity } from "./device-security-store";
import { server, lifecycle, createAccount, resetSetup, restartServer } from "./test/authority-workflow-fixture";

it.each(["before", "after"] as const)("resumes emergency recovery at every durable write and RPC (%s)", async (side) => {
  let events = 0, failure = -1, fired = false, monitor = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    if (!monitor) return work();
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry, adopt = securityStore.adoptRecoveredSecurity;
  const profile = accountStore.saveAccountPublicProfile, account = accountStore.saveAccount, fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "adoptRecoveredSecurity").mockImplementation((...args) => step(() => adopt(...args))),
    vi.spyOn(accountStore, "saveAccountPublicProfile").mockImplementation((...args) => step(() => profile(...args))),
    vi.spyOn(accountStore, "saveAccount").mockImplementation((...args) => step(() => account(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  let count = 1;
  try {
    for (let index = -1; index < count; index++) {
      monitor = false; await restartServer();
      vi.stubGlobal("indexedDB", new IDBFactory()); resetSetup();
      const { code } = await createAccount();
      vi.stubGlobal("indexedDB", new IDBFactory()); const recovery = new AuthoritySetup(lifecycle());
      events = 0; failure = index; fired = false; monitor = true;
      try {
        await recovery.prepareEmergencyRecovery(server.url, code);
        const status = await recovery.resumeRecovery();
        await server.ready(status.recoveryId);
        await recovery.confirmRecoveryCodeSaved();
        await recovery.completeRecovery();
      } catch { /* Restart at the committed boundary. */ }
      monitor = false;
      if (index === -1) count = events;
      else expect(fired).toBe(true);
      const pending = await readSecurityOperation(); const signed = pending?.signed;
      if (pending) {
        if (!signed) await recovery.continueRecoverySigning(code);
        const status = await recovery.resumeRecovery();
        await server.ready(status.recoveryId);
        if (signed) expect((await readSecurityOperation())!.signed).toEqual(signed);
        await recovery.completeRecovery();
      } else if (!await loadAccount()) {
        await recovery.prepareEmergencyRecovery(server.url, code);
        const status = await recovery.resumeRecovery(); await server.ready(status.recoveryId);
        await recovery.completeRecovery();
      }
      expect(await readSecurityOperation()).toBeUndefined(); expect(isAuthority(await loadSecurity())).toBe(true);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);
