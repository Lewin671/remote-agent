// The account lives in IndexedDB. The device's private key is a
// non-extractable CryptoKey. The root key is kept as bytes too, since
// pairing hands it to new devices; a script that could read it could
// already decrypt everything through the page (docs/architecture.md §9).

export interface StoredAccount {
  server: string;
  root: CryptoKey;
  // The root key's bytes, so that this browser can pair new devices.
  // Browsers that joined before pairing existed do not have them.
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
