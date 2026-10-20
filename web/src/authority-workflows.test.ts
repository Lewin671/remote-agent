import { IDBFactory } from "fake-indexeddb";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { AuthorityServer } from "./test/authority-server";
import { AuthoritySetup, authorityStartup } from "./authority-setup";
import { AuthorityLifecycle, readSecurityOperation, isAuthority, operation, claimSecurityOperation } from "./security-operation";
import { loadAccount } from "./storage";
import * as securityStore from "./device-security-store";
import * as accountStore from "./storage";
import { loadSecurity, securitySetupStatus } from "./device-security-store";
import { RecoveryCode } from "./recovery";

let built: ReturnType<typeof AuthorityServer.build>;
let server: AuthorityServer;
let visible = true;
let setup: AuthoritySetup;
let local: IDBFactory;
const lifecycles: AuthorityLifecycle[] = [];
function lifecycle() { const l = new AuthorityLifecycle(() => visible); lifecycles.push(l); return l; }
function locks() {
  let tail = Promise.resolve();
  return { request: (_name: string, _options: object, work: () => Promise<unknown>) => {
    const next = tail.then(work); tail = next.then(() => undefined, () => undefined); return next;
  } };
}
beforeAll(() => { built = AuthorityServer.build(); }, 120000);
afterAll(() => built.cleanup());
beforeEach(async () => {
  local = new IDBFactory(); visible = true;
  vi.stubGlobal("indexedDB", local);
  vi.stubGlobal("navigator", { locks: locks() });
  vi.stubGlobal("localStorage", { setItem: vi.fn(), getItem: () => null });
  setup = new AuthoritySetup(lifecycle());
  server = await AuthorityServer.start(built.binary);
}, 30000);
afterEach(async () => { for (const l of lifecycles.splice(0)) l.dispose(); await server?.close(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function createAccount(token = "test-setup") {
  const prepared = await setup.prepareCreation(server.url);
  await setup.continueCreation(token);
  await setup.confirmCreationCodeSaved();
  return { account: (await loadAccount())!, code: prepared.code! };
}
function dropAck(path: string) {
  const fetch = globalThis.fetch;
  let dropped = false;
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const response = await fetch(input, init);
    const url = input instanceof Request ? input.url : String(input);
    if (!dropped && url.endsWith("/" + path) && response.ok) { dropped = true; throw new Error("injected lost ack"); }
    return response;
  });
}
it("continues the exact creation draft after registration and pin acknowledgements are lost; token can be corrected", async () => {
  expect(await securitySetupStatus()).toBe("missing");
  const { code } = await setup.prepareCreation(server.url);
  const original = await readSecurityOperation();
  await expect(setup.continueCreation("wrong-token")).rejects.toThrow();
  expect((await readSecurityOperation())!.signed).toEqual(original!.signed);
  const ack = dropAck("RegisterDevice");
  await expect(setup.continueCreation("test-setup")).rejects.toThrow(); ack.mockRestore();
  setup = new AuthoritySetup(lifecycle());
  expect(await setup.prepareCreation(server.url)).toMatchObject({ code: undefined, resumed: true });
  const pin = dropAck("PinSecurityDomain");
  await expect(setup.continueCreation()).rejects.toThrow(); pin.mockRestore();
  await setup.continueCreation();
  expect((await authorityStartup()).kind).toBe("create");
  expect((await readSecurityOperation())!.signed).toEqual(original!.signed);
  expect(await loadAccount()).toBeUndefined();
  await setup.confirmCreationCodeSaved();
  expect(await readSecurityOperation()).toBeUndefined();
  expect(isAuthority(await loadSecurity())).toBe(true);
  expect(RecoveryCode.decode(code!)!.genesis).toEqual((await loadSecurity()).pin.genesis.slice(0, 10));
});
it("creates on an open relay with real PoW while retaining its signed registration", async () => {
  await server.close(); server = await AuthorityServer.start(built.binary, true);
  await createAccount("");
  expect(isAuthority(await loadSecurity())).toBe(true);
  expect((await loadSecurity()).lease).toBeDefined();
});
it.each(["before", "after"] as const)("recovers creation after every durable write and RPC (%s)", async (side) => {
  // Discover the boundaries on the real flow, then crash once at every one.
  // Signature material must be durable before any remote mutation.
  let events = 0, failure = -1, fired = false;
  const step = async <T>(work: () => Promise<T>): Promise<T> => {
    const at = events++;
    if (at === failure && side === "before") { fired = true; throw new Error("injected crash"); }
    const result = await work();
    if (at === failure && side === "after") { fired = true; throw new Error("injected crash"); }
    return result;
  };
  const update = securityStore.updateSecurityEntry;
  const initialize = securityStore.initializeSecurity;
  const profile = accountStore.saveAccountPublicProfile;
  const account = accountStore.saveAccount;
  const fetch = globalThis.fetch;
  const spies = [
    vi.spyOn(securityStore, "updateSecurityEntry").mockImplementation((...args) => step(() => update(...args))),
    vi.spyOn(securityStore, "initializeSecurity").mockImplementation((...args) => step(() => initialize(...args))),
    vi.spyOn(accountStore, "saveAccountPublicProfile").mockImplementation((...args) => step(() => profile(...args))),
    vi.spyOn(accountStore, "saveAccount").mockImplementation((...args) => step(() => account(...args))),
    vi.spyOn(globalThis, "fetch").mockImplementation((...args) => step(() => fetch(...args))),
  ];
  const run = async () => { await setup.prepareCreation(server.url); await setup.continueCreation("test-setup"); await setup.confirmCreationCodeSaved(); };
  try {
    await run(); const count = events;
    for (let index = 0; index < count; index++) {
      vi.stubGlobal("indexedDB", new IDBFactory()); events = 0; failure = index; fired = false;
      setup = new AuthoritySetup(lifecycle());
      try { await run(); } catch { /* Simulate process loss, preserving committed storage. */ }
      const pending = await readSecurityOperation();
      const signed = pending?.signed;
      failure = -1;
      setup = new AuthoritySetup(lifecycle());
      if (pending) {
        await setup.continueCreation("test-setup");
        if (await readSecurityOperation()) {
          if (signed) expect((await readSecurityOperation())!.signed).toEqual(signed);
          await setup.adoptCreationWithoutCode();
        }
      } else if (!await loadAccount()) await run();
      expect(isAuthority(await loadSecurity())).toBe(true);
      expect(await readSecurityOperation()).toBeUndefined();
      expect(fired).toBe(true);
    }
  } finally { for (const spy of spies) spy.mockRestore(); }
}, 60000);

it("retains a surviving pin after partial storage loss and refuses initialization or a new operation", async () => {
  const { account } = await createAccount(), record = await loadSecurity();
  const { rawSecurity } = await import("./test/stored-security-fixture");
  await rawSecurity("readwrite", (store) => store.delete("current"));
  expect(await securitySetupStatus()).toBe("invalid");
  await expect(securityStore.initializeSecurity(record.pin, record.states, record.proofs, record.memberID, record.xPrivate, false, record.lease)).rejects.toThrow("storage");
  await expect(claimSecurityOperation(operation("lease", server.url, account.deviceId, record.stateHash, {}))).rejects.toThrow("storage");
  expect(await rawSecurity("readonly", (store) => store.get("pinned-identity"))).toEqual(record.pin);
});
