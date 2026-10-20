import { forgetSecurity } from "./device-security-store";
import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import { clearEventCache } from "./eventcache";
import type { NewConfig } from "./model";
import { checkServer, deviceID, hex } from "./crypto";
import { equal, SecurityError, type Pin } from "./device-security";

// The account lives in IndexedDB. The device's private key is a
// non-extractable CryptoKey. A device that joined through pairing has no
// account root; a retained device may still keep it for protocol-8 history.

export interface StoredAccount {
  server: string;
  // Absent for a device that joined by pairing after protocol 10.
  root?: CryptoKey;
  rootRaw?: Uint8Array<ArrayBuffer>;
  devicePrivate: CryptoKey;
  deviceId: string;
}

const DB = "remote-agent";
const STORE = "kv";
const KEY = "account";

function db(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function run<T>(
  mode: IDBTransactionMode,
  fn: (s: IDBObjectStore) => IDBRequest,
): Promise<T> {
  return db().then(
    (d) =>
      new Promise<T>((resolve, reject) => {
        const tx = d.transaction(STORE, mode, { durability: "strict" });
        const req = fn(tx.objectStore(STORE));
        tx.oncomplete = () => {
          d.close();
          resolve(req.result as T);
        };
        tx.onerror = () => {
          d.close();
          reject(tx.error);
        };
      }),
  );
}

export function loadAccount(): Promise<StoredAccount | undefined> {
  return run("readonly", (s) => s.get(KEY));
}

export function saveAccount(a: StoredAccount): Promise<void> {
  return run("readwrite", (s) => s.put(a, KEY));
}

/** Public material only, separate from the unchanged account/security records.
 * All creation, pairing and recovery intake uses this ID check before saving. */
export interface AccountPublicProfile {
  server: string;
  pin: Pin;
  accountPublicKey: Uint8Array<ArrayBuffer>;
}
function publicProfileKey(server: string, pin: Pin): string {
  if (!/^[0-9a-f]{32}$/.test(pin.account) || pin.domain.length !== 32 ||
      pin.genesis.length !== 32 || !pin.domain.some(Boolean) || !pin.genesis.some(Boolean))
    throw new SecurityError("untrusted");
  return "account-public/" + JSON.stringify([checkServer(server), pin.account, hex(pin.domain), hex(pin.genesis)]);
}
export async function saveAccountPublicProfile(server: string, pin: Pin, accountPublicKey: Uint8Array<ArrayBuffer>): Promise<void> {
  // Copy before awaiting, so a caller cannot change the verified bytes.
  const profile: AccountPublicProfile = {
    server: checkServer(server),
    pin: { account: pin.account, domain: new Uint8Array(pin.domain), genesis: new Uint8Array(pin.genesis) },
    accountPublicKey: new Uint8Array(accountPublicKey),
  };
  const key = publicProfileKey(profile.server, profile.pin);
  if (profile.accountPublicKey.length !== 32 || await deviceID(profile.accountPublicKey) !== profile.pin.account)
    throw new SecurityError("untrusted");
  const d = await db();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = d.transaction(STORE, "readwrite", { durability: "strict" });
      const store = tx.objectStore(STORE);
      const get = store.get(key);
      get.onsuccess = () => {
        const old = get.result as AccountPublicProfile | undefined;
        if (old && (!(old.accountPublicKey instanceof Uint8Array) || !equal(old.accountPublicKey, profile.accountPublicKey))) {
          tx.abort();
          return;
        }
        store.put(profile, key);
      };
      tx.oncomplete = () => resolve();
      tx.onabort = tx.onerror = () => reject(new SecurityError("storage"));
    });
  } finally { d.close(); }
}
export async function loadAccountPublicProfile(server: string, pin: Pin): Promise<AccountPublicProfile | undefined> {
  const profile = await run<AccountPublicProfile | undefined>("readonly", (s) => s.get(publicProfileKey(server, pin)));
  if (!profile) return;
  if (profile.server !== checkServer(server) || profile.pin?.account !== pin.account ||
      !(profile.pin.domain instanceof Uint8Array) || !equal(profile.pin.domain, pin.domain) ||
      !(profile.pin.genesis instanceof Uint8Array) || !equal(profile.pin.genesis, pin.genesis) ||
      !(profile.accountPublicKey instanceof Uint8Array) || profile.accountPublicKey.length !== 32 ||
      await deviceID(profile.accountPublicKey) !== pin.account) throw new SecurityError("storage");
  return profile;
}

let logoutChannel: BroadcastChannel | undefined;

/** Stops every open copy of this browser account after an explicit logout.
 * Reloading also releases keys held by work still completing in the old page. */
export function watchAccountLogout(onLogout: () => void): () => void {
  const channel = new BroadcastChannel("ra-account-logout");
  logoutChannel = channel;
  channel.onmessage = (event) => {
    if (event.data === "logout") onLogout();
  };
  return () => {
    channel.close();
    if (logoutChannel === channel) logoutChannel = undefined;
  };
}

/** Forgets the account, and the sealed history cached for it. */
export async function forgetAccount(): Promise<void> {
  // Keep the account reachable for an explicit retry if key erasure is blocked.
  await forgetSecurity();
  await clearRegistration();
  await run("readwrite", (s) => {
    const cursor = s.openCursor();
    cursor.onsuccess = () => {
      const entry = cursor.result;
      if (!entry) return;
      if (typeof entry.key === "string" && entry.key.startsWith("account-public/")) entry.delete();
      entry.continue();
    };
    return cursor;
  });
  await run("readwrite", (s) => s.delete(KEY));
  // The account store is shared by same-origin tabs. Notify them as soon
  // as the deletion commits, even if clearing the sealed cache fails.
  // Reuse this page's listener so it does not receive its own broadcast:
  // the caller reloads after clearing the cache below.
  const channel = logoutChannel ?? new BroadcastChannel("ra-account-logout");
  channel.postMessage("logout");
  if (channel !== logoutChannel) channel.close();
  await clearEventCache();
}

// Project choices stay in this browser, scoped to its server and device.
// Only launch settings are stored, never the copied session or its content.

type ProjectConfig = Omit<NewConfig, "from">;
function projectConfigKey(account: StoredAccount, project: string): string {
  return (
    "ra-project-config/" +
    JSON.stringify([account.server, account.deviceId, project])
  );
}

export function loadProjectConfig(
  account: StoredAccount,
  project: string,
): ProjectConfig | undefined {
  try {
    const raw = localStorage.getItem(projectConfigKey(account, project));
    if (!raw) return undefined;
    const c = JSON.parse(raw);
    if (
      !c ||
      typeof c.machineId !== "string" ||
      typeof c.cwd !== "string" ||
      typeof c.model !== "string" ||
      typeof c.effort !== "string" ||
      ![Agent.CODEX, Agent.CLAUDE].includes(c.agent) ||
      ![ApprovalMode.UNSPECIFIED, ApprovalMode.ASK, ApprovalMode.AUTO].includes(
        c.mode,
      )
    )
      return undefined;
    return {
      machineId: c.machineId,
      cwd: c.cwd,
      agent: c.agent,
      model: c.model,
      effort: c.effort,
      mode: c.mode,
    };
  } catch {
    return undefined;
  }
}

export function saveProjectConfig(
  account: StoredAccount,
  project: string,
  c: NewConfig,
): void {
  const mode =
    c.mode === ApprovalMode.ASK || c.mode === ApprovalMode.AUTO
      ? c.mode
      : ApprovalMode.UNSPECIFIED;
  localStorage.setItem(
    projectConfigKey(account, project),
    JSON.stringify({
      machineId: c.machineId,
      cwd: c.cwd,
      agent: c.agent,
      model: c.model,
      effort: c.effort,
      mode,
    }),
  );
}

export function clearProjectConfig(
  account: StoredAccount,
  project: string,
): void {
  localStorage.removeItem(projectConfigKey(account, project));
}

/** Private local receipt, written before any account/security installation. */
export interface RegistrationIntent {
  account: StoredAccount;
  request: Uint8Array<ArrayBuffer>;
  security?: import("./device-security-store").SecurityRecord;
  replace: boolean;
}
export function loadRegistration(): Promise<RegistrationIntent | undefined> {
  return run("readonly", (s) => s.get("registration"));
}
export async function saveRegistration(
  intent: RegistrationIntent,
): Promise<void> {
  const d = await db();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = d.transaction(STORE, "readwrite", { durability: "strict" });
      const s = tx.objectStore(STORE);
      const get = s.get("registration");
      get.onsuccess = () => {
        const old = get.result as RegistrationIntent | undefined;
        if (
          old &&
          (old.account.server !== intent.account.server ||
            old.account.deviceId !== intent.account.deviceId ||
            old.request.length !== intent.request.length ||
            old.request.some((b, i) => b !== intent.request[i]))
        ) {
          tx.abort();
          return;
        }
        if (!old) s.add(intent, "registration");
      };
      tx.oncomplete = () => resolve();
      tx.onabort = tx.onerror = () =>
        reject(new Error("还有未完成的设备注册，请先重试"));
    });
  } finally {
    d.close();
  }
}
export async function clearRegistration(
  expected?: RegistrationIntent,
): Promise<void> {
  if (!expected) return run("readwrite", (s) => s.delete("registration"));
  const d = await db();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = d.transaction(STORE, "readwrite", { durability: "strict" });
      const store = tx.objectStore(STORE);
      const get = store.get("registration");
      get.onsuccess = () => {
        const r = get.result as RegistrationIntent | undefined;
        if (
          r &&
          r.account.deviceId === expected.account.deviceId &&
          r.request.length === expected.request.length &&
          r.request.every((b, i) => b === expected.request[i])
        )
          store.delete("registration");
      };
      tx.oncomplete = () => resolve();
      tx.onerror = tx.onabort = () => reject(tx.error);
    });
  } finally {
    d.close();
  }
}
