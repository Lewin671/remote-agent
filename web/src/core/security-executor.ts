import { create, type DescMessage, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf";
import { Code } from "@connectrpc/connect";
import { checkServer } from "../crypto";
import { EventSchema as CoreEventSchema, type Effect, type Event as CoreEvent } from "../gen/remoteagent/core/v1/app_pb";
import {
  RpcErrorDetailBytesSchema,
  SecurityCryptoBatchDoneEventSchema,
  SecurityCryptoBatchResultSchema,
  SecurityKeyAgreedEventSchema,
  SecurityKeyGeneratedEventSchema,
  SecurityKeyKind,
  SecurityKeyEncapsulatedEventSchema,
  SecurityKeySignedEventSchema,
  SecurityRpcMethod,
  SecurityRpcResultEventSchema,
  SecurityStorageResultEventSchema,
  type RpcErrorDetailBytes,
  type SecurityCryptoBatchEffect,
  type SecurityCryptoBatchResult,
  type SecurityKeyAgreeEffect,
  type SecurityKeyDiscardEffect,
  type SecurityKeyEncapsulateEffect,
  type SecurityKeyGenerateEffect,
  type SecurityKeySignEffect,
  type SecurityRpcEffect,
  type SecurityStorageEffect,
} from "../gen/remoteagent/core/v1/security_pb";
import { relayFetch } from "../transport";
import { SecurityStoreV4, securityStoreV4 } from "./security-store-v4";

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err));

function decodeConnectDetailBase64(raw: string): Uint8Array | null {
  const cleaned = raw.replace(/\s+/g, "").replace(/-/g, "+").replace(/_/g, "/");
  if (!cleaned) return null;
  const padded = cleaned + "=".repeat((4 - (cleaned.length % 4)) % 4);
  try {
    const bin = atob(padded);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

function rpcBase(server: string): string {
  const origin = checkServer(server);
  return import.meta.env?.VITE_SAME_ORIGIN_API && typeof location !== "undefined" ? location.origin : origin;
}

const RPC_PATHS: Partial<Record<SecurityRpcMethod, string>> = {
  [SecurityRpcMethod.GET_SECURITY_STATES]: "/remoteagent.v1.ClientService/GetSecurityStates",
  [SecurityRpcMethod.PUT_SECURITY_STATE]: "/remoteagent.v1.ClientService/PutSecurityState",
  [SecurityRpcMethod.PIN_SECURITY_DOMAIN]: "/remoteagent.v1.ClientService/PinSecurityDomain",
  [SecurityRpcMethod.CREATE_PAIRING]: "/remoteagent.v1.PairingService/CreatePairing",
  [SecurityRpcMethod.PEEK_PAIRING]: "/remoteagent.v1.PairingService/PeekPairing",
  [SecurityRpcMethod.JOIN_PAIRING]: "/remoteagent.v1.PairingService/JoinPairing",
  [SecurityRpcMethod.REVEAL_PAIRING]: "/remoteagent.v1.PairingService/RevealPairing",
  [SecurityRpcMethod.APPROVE_PAIRING]: "/remoteagent.v1.PairingService/ApprovePairing",
  [SecurityRpcMethod.GET_PAIRING]: "/remoteagent.v1.PairingService/GetPairing",
  [SecurityRpcMethod.CANCEL_PAIRING]: "/remoteagent.v1.PairingService/CancelPairing",
  [SecurityRpcMethod.GET_RECOVERY_CHALLENGE]: "/remoteagent.v1.RecoveryService/GetRecoveryChallenge",
  [SecurityRpcMethod.GET_RECOVERY_CONTEXT]: "/remoteagent.v1.RecoveryService/GetRecoveryContext",
  [SecurityRpcMethod.SUBMIT_RECOVERY]: "/remoteagent.v1.RecoveryService/SubmitRecovery",
  [SecurityRpcMethod.COMPLETE_RECOVERY]: "/remoteagent.v1.RecoveryService/CompleteRecovery",
  [SecurityRpcMethod.DELETE_MACHINE_RECORDS]: "/remoteagent.v1.ClientService/DeleteMachineRecords",
  [SecurityRpcMethod.DELETE_ACCOUNT]: "/remoteagent.v1.ClientService/DeleteAccount",
  [SecurityRpcMethod.GET_CHALLENGE]: "/remoteagent.v1.AuthService/GetChallenge",
  [SecurityRpcMethod.LOGIN]: "/remoteagent.v1.AuthService/Login",
  [SecurityRpcMethod.GET_SIGNUP_CHALLENGE]: "/remoteagent.v1.AuthService/GetSignupChallenge",
  [SecurityRpcMethod.REGISTER_DEVICE]: "/remoteagent.v1.AuthService/RegisterDevice",
  [SecurityRpcMethod.PUT_DEVICE_META]: "/remoteagent.v1.ClientService/PutDeviceMeta",
  [SecurityRpcMethod.LIST_DEVICES]: "/remoteagent.v1.ClientService/ListDevices",
  [SecurityRpcMethod.LIST_MACHINES]: "/remoteagent.v1.ClientService/ListMachines",
  [SecurityRpcMethod.GET_ACCOUNT_PREFS]: "/remoteagent.v1.ClientService/GetAccountPrefs",
  [SecurityRpcMethod.PUT_ACCOUNT_PREFS]: "/remoteagent.v1.ClientService/PutAccountPrefs",
};

const CONNECT_NAME_TO_CODE: Record<string, Code> = {
  canceled: Code.Canceled,
  unknown: Code.Unknown,
  invalid_argument: Code.InvalidArgument,
  deadline_exceeded: Code.DeadlineExceeded,
  not_found: Code.NotFound,
  already_exists: Code.AlreadyExists,
  permission_denied: Code.PermissionDenied,
  resource_exhausted: Code.ResourceExhausted,
  failed_precondition: Code.FailedPrecondition,
  aborted: Code.Aborted,
  out_of_range: Code.OutOfRange,
  unimplemented: Code.Unimplemented,
  internal: Code.Internal,
  unavailable: Code.Unavailable,
  data_loss: Code.DataLoss,
  unauthenticated: Code.Unauthenticated,
};

const HTTP_TO_CONNECT: Record<number, Code> = {
  400: Code.InvalidArgument,
  401: Code.Unauthenticated,
  403: Code.PermissionDenied,
  404: Code.NotFound,
  409: Code.Aborted,
  412: Code.FailedPrecondition,
  429: Code.ResourceExhausted,
  499: Code.Canceled,
  502: Code.Unavailable,
  503: Code.Unavailable,
  504: Code.Unavailable,
};

const httpStatusToConnectCode = (status: number): Code =>
  HTTP_TO_CONNECT[status] ?? (status >= 500 ? Code.Internal : Code.Unknown);

export interface SecurityEffectHandler {
  securityKeyGenerate?(eff: SecurityKeyGenerateEffect): void;
  securityKeySign?(eff: SecurityKeySignEffect): void;
  securityKeyAgree?(eff: SecurityKeyAgreeEffect): void;
  securityKeyEncapsulate?(eff: SecurityKeyEncapsulateEffect): void;
  securityKeyDiscard?(eff: SecurityKeyDiscardEffect): void;
  securityStorage?(eff: SecurityStorageEffect): void;
  securityRpc?(eff: SecurityRpcEffect): void;
  securityCryptoBatch?(eff: SecurityCryptoBatchEffect): void;
}

/** Routes Go `security*` effects to a `WebSecurityExecutor` or mock `HostExecutor`. */
export function dispatchSecurityEffect(exec: SecurityEffectHandler | WebSecurityExecutor | undefined, eff: Effect["effect"]): boolean {
  if (!exec) return false;
  if (exec instanceof WebSecurityExecutor) {
    const h: Record<string, ((v: never) => void) | undefined> = {
      securityKeyGenerate: (v) => void exec.keyGenerate(v),
      securityKeySign: (v) => void exec.keySign(v),
      securityKeyAgree: (v) => void exec.keyAgree(v),
      securityKeyEncapsulate: (v) => void exec.keyEncapsulate(v),
      securityKeyDiscard: (v) => void exec.keyDiscard(v),
      securityStorage: (v) => void exec.storage(v),
      securityRpc: (v) => void exec.rpc(v),
      securityCryptoBatch: (v) => void exec.cryptoBatch(v),
    };
    const fn = eff.case ? h[eff.case] : undefined;
    if (!fn) return false;
    fn(eff.value as never);
    return true;
  }
  switch (eff.case) {
    case "securityKeyGenerate": exec.securityKeyGenerate?.(eff.value); return true;
    case "securityKeySign": exec.securityKeySign?.(eff.value); return true;
    case "securityKeyAgree": exec.securityKeyAgree?.(eff.value); return true;
    case "securityKeyEncapsulate": exec.securityKeyEncapsulate?.(eff.value); return true;
    case "securityKeyDiscard": exec.securityKeyDiscard?.(eff.value); return true;
    case "securityStorage": exec.securityStorage?.(eff.value); return true;
    case "securityRpc": exec.securityRpc?.(eff.value); return true;
    case "securityCryptoBatch": exec.securityCryptoBatch?.(eff.value); return true;
    default: return false;
  }
}

/**
 * Executes asynchronous non-extractable WebCrypto operations, v4 IndexedDB CAS storage,
 * and unary Connect-RPC calls requested by clientcore/security.
 */
export class WebSecurityExecutor {
  private readonly caches = new Map<string, Promise<CryptoKey>>();
  private readonly store: SecurityStoreV4;
  private readonly fetchFn: typeof fetch;
  private closed = false;

  constructor(
    private readonly options: {
      store?: SecurityStoreV4;
      fetch?: typeof fetch;
      devicePrivate?: () => CryptoKey | undefined;
      xPrivate?: () => CryptoKey | undefined;
      emit: (ev: CoreEvent) => void;
    },
  ) {
    this.store = options.store ?? securityStoreV4;
    this.fetchFn = options.fetch ?? relayFetch;
  }

  close() {
    this.closed = true;
    this.caches.clear();
  }

  private emit(event: CoreEvent["event"]) {
    if (!this.closed) this.options.emit(create(CoreEventSchema, { event }));
  }

  private async runEvent<K extends NonNullable<CoreEvent["event"]["case"]>, D extends DescMessage>(
    caseName: K,
    schema: D,
    opId: bigint,
    fn: () => Promise<MessageInitShape<D>>,
    makeValue?: (ok: MessageShape<D>) => unknown,
  ): Promise<void> {
    try {
      const value = makeValue ? makeValue((await fn()) as MessageShape<D>) : create(schema, await fn());
      this.emit({ case: caseName, value } as CoreEvent["event"]);
    } catch (err) {
      this.emit({ case: caseName, value: create(schema, { opId, error: errorMessage(err) } as unknown as MessageInitShape<D>) } as CoreEvent["event"]);
    }
  }

  private importCached(tag: string, key: Uint8Array, alg: string, usages: KeyUsage[]): Promise<CryptoKey> {
    const id = tag + Array.from(key, (x) => x.toString(16).padStart(2, "0")).join("");
    let p = this.caches.get(id);
    if (!p) {
      if (this.caches.size >= 512) this.caches.clear();
      p = crypto.subtle.importKey("raw", new Uint8Array(key), alg, false, usages);
      p.catch(() => {
        if (this.caches.get(id) === p) this.caches.delete(id);
      });
      this.caches.set(id, p);
    }
    return p;
  }

  keyGenerate(eff: SecurityKeyGenerateEffect): Promise<void> {
    return this.runEvent("securityKeyGenerated", SecurityKeyGeneratedEventSchema, eff.opId, async () => ({
      opId: eff.opId,
      publicKey: await this.store.generateKey(eff.keyKind, eff.slot, eff.persist),
    }));
  }

  keySign(eff: SecurityKeySignEffect): Promise<void> {
    return this.runEvent("securityKeySigned", SecurityKeySignedEventSchema, eff.opId, async () => {
      const override = !eff.slot && eff.keyKind === SecurityKeyKind.DEVICE_ED ? this.options.devicePrivate?.() : undefined;
      const signature = override
        ? new Uint8Array(await crypto.subtle.sign("Ed25519", override, new Uint8Array(eff.message)))
        : await this.store.signKey(eff.keyKind || SecurityKeyKind.DEVICE_ED, eff.slot, eff.message);
      return { opId: eff.opId, signature };
    });
  }

  keyAgree(eff: SecurityKeyAgreeEffect): Promise<void> {
    return this.runEvent("securityKeyAgreed", SecurityKeyAgreedEventSchema, eff.opId, async () => {
      const override = !eff.slot && (!eff.keyKind || eff.keyKind === SecurityKeyKind.DEVICE_X) ? this.options.xPrivate?.() : undefined;
      if (override) {
        if (eff.peerPublic.byteLength !== 32) throw new Error("invalid peer public");
        const pub = await this.importCached("x:", eff.peerPublic, "X25519", []);
        return { opId: eff.opId, shared: new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: pub }, override, 256)) };
      }
      return { opId: eff.opId, shared: await this.store.agreeKey(eff.keyKind || SecurityKeyKind.DEVICE_X, eff.slot, eff.peerPublic) };
    });
  }

  keyEncapsulate(eff: SecurityKeyEncapsulateEffect): Promise<void> {
    return this.runEvent("securityKeyEncapsulated", SecurityKeyEncapsulatedEventSchema, eff.opId, async () => ({
      opId: eff.opId,
      pairs: await this.store.encapsulate(eff.recipients),
    }));
  }

  async keyDiscard(eff: SecurityKeyDiscardEffect): Promise<void> {
    await this.store.discardKey(eff).catch(() => undefined);
  }

  storage(eff: SecurityStorageEffect): Promise<void> {
    return this.runEvent(
      "securityStorageResult",
      SecurityStorageResultEventSchema,
      eff.opId,
      () => this.store.executeStorage(eff),
      (v) => v,
    );
  }

  async rpc(eff: SecurityRpcEffect): Promise<void> {
    const emitRpc = (fields: MessageInitShape<typeof SecurityRpcResultEventSchema>) =>
      this.emit({ case: "securityRpcResult", value: create(SecurityRpcResultEventSchema, { opId: eff.opId, ...fields }) });
    const path = RPC_PATHS[eff.method];
    if (!path) {
      emitRpc({ connectCode: Code.InvalidArgument, error: "unsupported security rpc method" });
      return;
    }
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(new Error("timeout")), eff.method === SecurityRpcMethod.GET_SECURITY_STATES ? 5000 : 15000);
    try {
      const headers: Record<string, string> = { "Content-Type": "application/proto", "Connect-Protocol-Version": "1" };
      if (eff.authToken) headers["Authorization"] = `Bearer ${eff.authToken}`;
      const res = await this.fetchFn(rpcBase(eff.server) + path, {
        method: "POST",
        headers,
        body: new Uint8Array(eff.requestBytes),
        signal: ctrl.signal,
      });
      if (!res.ok) {
        let code = httpStatusToConnectCode(res.status);
        let msg = `HTTP ${res.status}`;
        const errorDetails: RpcErrorDetailBytes[] = [];
        try {
          const parsed = JSON.parse(await res.text()) as {
            code?: string;
            message?: string;
            details?: Array<{ type?: unknown; value?: unknown }>;
          };
          if (parsed.code && parsed.code in CONNECT_NAME_TO_CODE) code = CONNECT_NAME_TO_CODE[parsed.code];
          if (parsed.message) msg = parsed.message;
          if (Array.isArray(parsed.details)) {
            for (const d of parsed.details) {
              if (typeof d?.type !== "string" || !d.type || typeof d.value !== "string") continue;
              const value = decodeConnectDetailBase64(d.value);
              if (value && value.byteLength > 0) {
                errorDetails.push(create(RpcErrorDetailBytesSchema, { typeName: d.type, value }));
              }
            }
          }
        } catch { /* keep HTTP status fallback */ }
        emitRpc({ connectCode: code, error: msg, errorDetails });
        return;
      }
      emitRpc({ responseBytes: new Uint8Array(await res.arrayBuffer()) });
    } catch (err) {
      const isTimeout = err instanceof Error && err.name === "AbortError";
      emitRpc({
        connectCode: isTimeout ? Code.DeadlineExceeded : Code.Unavailable,
        error: isTimeout ? "connection timed out" : errorMessage(err),
        networkError: true,
      });
    } finally {
      clearTimeout(timer);
    }
  }

  cryptoBatch(eff: SecurityCryptoBatchEffect): Promise<void> {
    return this.runEvent("securityCryptoBatchDone", SecurityCryptoBatchDoneEventSchema, eff.opId, async () => ({
      opId: eff.opId,
      results: await Promise.all(eff.items.map((item): Promise<SecurityCryptoBatchResult> => this.runBatchItem(item))),
    }));
  }

  private async runBatchItem(item: SecurityCryptoBatchEffect["items"][number]): Promise<SecurityCryptoBatchResult> {
    try {
      switch (item.kind.case) {
        case "verify": {
          const v = item.kind.value;
          if (v.publicKey.byteLength !== 32 || v.signature.byteLength !== 64) return create(SecurityCryptoBatchResultSchema, { ok: false });
          const pub = await this.importCached("ed:", v.publicKey, "Ed25519", ["verify"]);
          return create(SecurityCryptoBatchResultSchema, {
            ok: await crypto.subtle.verify("Ed25519", pub, new Uint8Array(v.signature), new Uint8Array(v.message)),
          });
        }
        case "aeadOpen":
        case "aeadSeal": {
          const a = item.kind.value;
          const decrypt = item.kind.case === "aeadOpen";
          const key = await this.importCached(decrypt ? "ao:" : "as:", a.key, "AES-GCM", [decrypt ? "decrypt" : "encrypt"]);
          const params = { name: "AES-GCM", iv: new Uint8Array(a.nonce), additionalData: new Uint8Array(a.aad) };
          const raw = item.kind.case === "aeadOpen" ? item.kind.value.ciphertext : item.kind.value.plaintext;
          const out = new Uint8Array(await (decrypt ? crypto.subtle.decrypt(params, key, new Uint8Array(raw)) : crypto.subtle.encrypt(params, key, new Uint8Array(raw))));
          return create(SecurityCryptoBatchResultSchema, { ok: true, data: out });
        }
        case "sha256":
          return create(SecurityCryptoBatchResultSchema, {
            ok: true,
            data: new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array(item.kind.value.data))),
          });
        default:
          return create(SecurityCryptoBatchResultSchema, { ok: false, error: "unsupported batch op" });
      }
    } catch (err) {
      return create(SecurityCryptoBatchResultSchema, { ok: false, error: errorMessage(err) });
    }
  }
}
