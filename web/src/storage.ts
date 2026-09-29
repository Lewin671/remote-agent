// The account lives in IndexedDB. The root key and the device's private key
// are stored as non-extractable CryptoKeys, so page scripts can use them
// but cannot read their bytes.

export interface StoredAccount {
  server: string;
  root: CryptoKey;
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
