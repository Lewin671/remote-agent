import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, vi } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthorityServer } from "./authority-server";
import { AuthoritySetup, authorityServices } from "../authority-setup";
import { AuthorityLifecycle, readSecurityOperation, claimSecurityOperation } from "../security-operation";
import { AuthorityClient } from "../authority-client";
import { loadAccount, type StoredAccount } from "../storage";
import * as securityStore from "../device-security-store";
import { DeviceSecurityClient } from "../device-security-client";
import { signLogin } from "../crypto";
import { PairingService } from "../gen/remoteagent/v1/relay_pb";
import { PROTOCOL_VERSION } from "../protocol";

// Vitest isolates modules and globals per file. Each file owns its relay,
// IndexedDB and lifecycle; only the immutable creation draft is reused within it.
let creation: Awaited<ReturnType<typeof prepareCreationFixture>>;
export let server: AuthorityServer;
export let setup: AuthoritySetup;
export let local: IDBFactory;
const lifecycles: AuthorityLifecycle[] = [];
export function lifecycle() { const l = new AuthorityLifecycle(() => true); lifecycles.push(l); return l; }
function locks() {
  let tail = Promise.resolve();
  return { request: (_name: string, _options: object, work: () => Promise<unknown>) => {
    const next = tail.then(work); tail = next.then(() => undefined, () => undefined); return next;
  } };
}
async function prepareCreationFixture() {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("navigator", { locks: locks() });
  const life = new AuthorityLifecycle(() => true);
  try {
    const prepared = await new AuthoritySetup(life).prepareCreation("https://relay.example.com");
    const draft = (await readSecurityOperation<{ account: StoredAccount }>())!;
    return { draft, code: prepared.code! };
  } finally { life.dispose(); vi.unstubAllGlobals(); }
}
beforeAll(async () => { creation = await prepareCreationFixture(); });
beforeEach(async () => {
  local = new IDBFactory();
  vi.stubGlobal("indexedDB", local);
  vi.stubGlobal("navigator", { locks: locks() });
  vi.stubGlobal("localStorage", { setItem: vi.fn(), getItem: () => null });
  setup = new AuthoritySetup(lifecycle());
  server = await AuthorityServer.start();
}, 30000);
afterEach(async () => { for (const l of lifecycles.splice(0)) l.dispose(); await server?.close(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
export async function createAccount(token = "test-setup") {
  // Genesis/signatures are independent of the relay URL. Clone the prepared
  // draft once per fresh relay/database; creation and its crash boundaries
  // still exercise prepareCreation directly in their dedicated tests.
  const draft = structuredClone(creation.draft);
  draft.server = draft.local.account.server = server.url;
  await claimSecurityOperation(draft);
  await setup.continueCreation(token);
  await setup.confirmCreationCodeSaved();
  return { account: (await loadAccount())!, code: creation.code };
}
export async function clients(account: StoredAccount) {
  const services = authorityServices(account.server);
  const { challenge } = await services.auth.getChallenge({ deviceId: account.deviceId });
  const { token } = await services.auth.login({ deviceId: account.deviceId, challenge, signature: await signLogin(account.devicePrivate, challenge), protocolVersion: PROTOCOL_VERSION });
  const api = services.client(token);
  const pairing = createClient(PairingService, createConnectTransport({ baseUrl: account.server, useBinaryFormat: true, interceptors: [(next) => (req) => { req.header.set("Authorization", "Bearer " + token); return next(req); }] }));
  const security = new DeviceSecurityClient(account.deviceId, account.devicePrivate, (afterEpoch, limit) => api.getSecurityStates({ afterEpoch, limit }), () => true, {
    loadSecurity: () => securityStore.loadSecurity(),
    installSecurity: (...args) => securityStore.installSecurity(...args),
    touchSecurity: (...args) => securityStore.touchSecurity(...args),
  });
  const authority = new AuthorityClient(account, security, api, pairing, async (publicKey, check, persist, request) => { if (!request) await persist(publicKey); check(); return true; }, lifecycle());
  return { api, pairing, security, authority, token };
}
export function dropAck(path: string) {
  const fetch = globalThis.fetch;
  let dropped = false;
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const response = await fetch(input, init);
    const url = input instanceof Request ? input.url : String(input);
    if (!dropped && url.endsWith("/" + path) && response.ok) { dropped = true; throw new Error("injected lost ack"); }
    return response;
  });
}

export function resetSetup() { setup = new AuthoritySetup(lifecycle()); }
export async function restartServer(open = false) {
  await server.close();
  server = await AuthorityServer.start(open);
}
