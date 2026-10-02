import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import type { NewConfig } from "./model";

// The account lives in IndexedDB. The device's private key is a
// non-extractable CryptoKey. The root key is kept as bytes too, since
// pairing hands it to new devices; a script that could read it could
// already decrypt everything through the page (docs/architecture.md §9).

export interface StoredAccount {
  server: string;
  root: CryptoKey;
  // The root key's bytes, so that this browser can pair new devices.
  rootRaw: Uint8Array<ArrayBuffer>;
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

function run<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest): Promise<T> {
  return db().then(
    (d) =>
      new Promise<T>((resolve, reject) => {
        const tx = d.transaction(STORE, mode);
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

export function forgetAccount(): Promise<void> {
  return run("readwrite", (s) => s.delete(KEY));
}

// Project choices stay in this browser, scoped to its server and device.
// Only launch settings are stored, never the copied session or its content.

type ProjectConfig = Omit<NewConfig, "from">;
function projectConfigKey(account: StoredAccount, project: string): string {
  return "ra-project-config/" + JSON.stringify([account.server, account.deviceId, project]);
}

export function loadProjectConfig(account: StoredAccount, project: string): ProjectConfig | undefined {
  try {
    const raw = localStorage.getItem(projectConfigKey(account, project));
    if (!raw) return undefined;
    const c = JSON.parse(raw);
    if (!c || typeof c.machineId !== "string" || typeof c.cwd !== "string" ||
        typeof c.model !== "string" || typeof c.effort !== "string" ||
        ![Agent.CODEX, Agent.CLAUDE].includes(c.agent) ||
        ![ApprovalMode.UNSPECIFIED, ApprovalMode.ASK, ApprovalMode.AUTO].includes(c.mode)) return undefined;
    return { machineId: c.machineId, cwd: c.cwd, agent: c.agent, model: c.model, effort: c.effort, mode: c.mode };
  } catch {
    return undefined;
  }
}

export function saveProjectConfig(account: StoredAccount, project: string, c: NewConfig): void {
  const mode = c.mode === ApprovalMode.ASK || c.mode === ApprovalMode.AUTO ? c.mode : ApprovalMode.UNSPECIFIED;
  localStorage.setItem(projectConfigKey(account, project), JSON.stringify({
    machineId: c.machineId, cwd: c.cwd, agent: c.agent, model: c.model, effort: c.effort, mode,
  }));
}

export function clearProjectConfig(account: StoredAccount, project: string): void {
  localStorage.removeItem(projectConfigKey(account, project));
}
