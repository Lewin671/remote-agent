import { create } from "@bufbuild/protobuf";
import { Code } from "@connectrpc/connect";
import { checkServer } from "../crypto";
import { EventSchema as CoreEventSchema, type Event as CoreEvent } from "../gen/remoteagent/core/v1/app_pb";
import {
  SecurityContentKeySchema,
  SecurityCryptoBatchDoneEventSchema,
  SecurityCryptoBatchResultSchema,
  SecurityKeyAgreedEventSchema,
  SecurityKeyGeneratedEventSchema,
  SecurityKeyKind,
  SecurityKeyEncapsulatedEventSchema,
  SecurityKeySignedEventSchema,
  SecurityRecordEventSchema,
  SecurityRpcMethod,
  SecurityRpcResultEventSchema,
  SecurityStorageResultEventSchema,
  type SecurityCryptoBatchEffect,
  type SecurityCryptoBatchResult,
  type SecurityKeyAgreeEffect,
  type SecurityKeyDiscardEffect,
  type SecurityKeyEncapsulateEffect,
  type SecurityKeyGenerateEffect,
  type SecurityKeySignEffect,
  type SecurityRecordEvent,
  type SecurityRpcEffect,
  type SecurityStorageEffect,
} from "../gen/remoteagent/core/v1/security_pb";
import { relayFetch } from "../transport";
import { SecurityStoreV4, securityStoreV4 } from "./security-store-v4";

function hexKey(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
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

function httpStatusToConnectCode(status: number): Code {
  switch (status) {
    case 400:
      return Code.InvalidArgument;
    case 401:
      return Code.Unauthenticated;
    case 403:
      return Code.PermissionDenied;
    case 404:
      return Code.NotFound;
    case 409:
      return Code.Aborted;
    case 412:
      return Code.FailedPrecondition;
    case 429:
      return Code.ResourceExhausted;
    case 499:
      return Code.Canceled;
    case 502:
    case 503:
    case 504:
      return Code.Unavailable;
    default:
      return status >= 500 ? Code.Internal : Code.Unknown;
  }
}

export function buildSecurityRecordEvent(params: {
  opId?: bigint;
  pushSeq?: bigint;
  networkGeneration: number | bigint;
  account: string;
  domain: Uint8Array;
  genesis: Uint8Array;
  states: Uint8Array[];
  recoveryProofs?: Uint8Array[];
  lease?: Uint8Array;
  floorMs?: bigint;
  recoveryPending?: boolean;
  recoveryReadyMs?: bigint;
  recoveryObjectsPending?: boolean;
  removed?: boolean;
  cleared?: boolean;
  error?: string;
  contentKeys?: { epoch: bigint; secret: Uint8Array }[];
}): SecurityRecordEvent {
  return create(SecurityRecordEventSchema, {
    opId: params.opId ?? 0n,
    pushSeq: params.pushSeq ?? 0n,
    networkGeneration: BigInt(params.networkGeneration),
    account: params.account,
    domain: new Uint8Array(params.domain),
    genesis: new Uint8Array(params.genesis),
    states: params.states.map((s) => new Uint8Array(s)),
    recoveryProofs: (params.recoveryProofs ?? []).map((p) => new Uint8Array(p)),
    lease: params.lease ? new Uint8Array(params.lease) : new Uint8Array(),
    floorMs: params.floorMs ?? 0n,
    recoveryPending: !!params.recoveryPending,
    recoveryReadyMs: params.recoveryReadyMs ?? 0n,
    recoveryObjectsPending: !!params.recoveryObjectsPending,
    removed: !!params.removed,
    cleared: !!params.cleared,
    error: params.error ?? "",
    contentKeys: (params.contentKeys ?? []).map((k) =>
      create(SecurityContentKeySchema, { contentEpoch: k.epoch, secret: new Uint8Array(k.secret) }),
    ),
  });
}

/**
 * Executes asynchronous non-extractable WebCrypto operations, v4 IndexedDB CAS storage,
 * and unary Connect-RPC calls requested by clientcore/security.
 */
export class WebSecurityExecutor {
  private readonly xPubKeys = new Map<string, Promise<CryptoKey>>();
  private readonly edPubKeys = new Map<string, Promise<CryptoKey>>();
  private readonly aesOpenKeys = new Map<string, Promise<CryptoKey>>();
  private readonly aesSealKeys = new Map<string, Promise<CryptoKey>>();
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
    this.xPubKeys.clear();
    this.edPubKeys.clear();
    this.aesOpenKeys.clear();
    this.aesSealKeys.clear();
  }

  private emit(event: CoreEvent["event"]) {
    if (!this.closed) {
      this.options.emit(create(CoreEventSchema, { event }));
    }
  }

  private importCached(
    cache: Map<string, Promise<CryptoKey>>,
    key: Uint8Array,
    alg: AlgorithmIdentifier | EcKeyImportParams,
    usages: KeyUsage[],
  ): Promise<CryptoKey> {
    const id = hexKey(key);
    let p = cache.get(id);
    if (!p) {
      if (cache.size >= 256) cache.clear();
      p = crypto.subtle.importKey("raw", new Uint8Array(key), alg, false, usages);
      p.catch(() => {
        if (cache.get(id) === p) cache.delete(id);
      });
      cache.set(id, p);
    }
    return p;
  }

  async keyGenerate(eff: SecurityKeyGenerateEffect): Promise<void> {
    try {
      const publicKey = await this.store.generateKey(eff.keyKind, eff.slot, eff.persist);
      this.emit({
        case: "securityKeyGenerated",
        value: create(SecurityKeyGeneratedEventSchema, { opId: eff.opId, publicKey }),
      });
    } catch (err) {
      this.emit({
        case: "securityKeyGenerated",
        value: create(SecurityKeyGeneratedEventSchema, { opId: eff.opId, error: errorMessage(err) }),
      });
    }
  }

  async keySign(eff: SecurityKeySignEffect): Promise<void> {
    try {
      let sig: Uint8Array;
      const override = !eff.slot && eff.keyKind === SecurityKeyKind.DEVICE_ED ? this.options.devicePrivate?.() : undefined;
      if (override) {
        sig = new Uint8Array(await crypto.subtle.sign("Ed25519", override, new Uint8Array(eff.message)));
      } else {
        sig = await this.store.signKey(eff.keyKind || SecurityKeyKind.DEVICE_ED, eff.slot, eff.message);
      }
      this.emit({
        case: "securityKeySigned",
        value: create(SecurityKeySignedEventSchema, { opId: eff.opId, signature: sig }),
      });
    } catch (err) {
      this.emit({
        case: "securityKeySigned",
        value: create(SecurityKeySignedEventSchema, { opId: eff.opId, error: errorMessage(err) }),
      });
    }
  }

  async keyAgree(eff: SecurityKeyAgreeEffect): Promise<void> {
    try {
      let shared: Uint8Array;
      const override = !eff.slot && (!eff.keyKind || eff.keyKind === SecurityKeyKind.DEVICE_X)
        ? this.options.xPrivate?.()
        : undefined;
      if (override) {
        if (eff.peerPublic.byteLength !== 32) throw new Error("invalid peer public");
        const pub = await this.importCached(this.xPubKeys, eff.peerPublic, "X25519", []);
        shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: pub }, override, 256));
      } else {
        shared = await this.store.agreeKey(eff.keyKind || SecurityKeyKind.DEVICE_X, eff.slot, eff.peerPublic);
      }
      this.emit({
        case: "securityKeyAgreed",
        value: create(SecurityKeyAgreedEventSchema, { opId: eff.opId, shared }),
      });
    } catch (err) {
      this.emit({
        case: "securityKeyAgreed",
        value: create(SecurityKeyAgreedEventSchema, { opId: eff.opId, error: errorMessage(err) }),
      });
    }
  }

  async keyEncapsulate(eff: SecurityKeyEncapsulateEffect): Promise<void> {
    try {
      const pairs = await this.store.encapsulate(eff.recipients);
      this.emit({
        case: "securityKeyEncapsulated",
        value: create(SecurityKeyEncapsulatedEventSchema, { opId: eff.opId, pairs }),
      });
    } catch (err) {
      this.emit({
        case: "securityKeyEncapsulated",
        value: create(SecurityKeyEncapsulatedEventSchema, { opId: eff.opId, error: errorMessage(err) }),
      });
    }
  }

  async keyDiscard(eff: SecurityKeyDiscardEffect): Promise<void> {
    await this.store.discardKey(eff).catch(() => undefined);
  }

  async storage(eff: SecurityStorageEffect): Promise<void> {
    try {
      const value = await this.store.executeStorage(eff);
      this.emit({ case: "securityStorageResult", value });
    } catch (err) {
      this.emit({
        case: "securityStorageResult",
        value: create(SecurityStorageResultEventSchema, {
          opId: eff.opId,
          error: errorMessage(err),
        }),
      });
    }
  }

  async rpc(eff: SecurityRpcEffect): Promise<void> {
    const path = RPC_PATHS[eff.method];
    if (!path) {
      this.emit({
        case: "securityRpcResult",
        value: create(SecurityRpcResultEventSchema, {
          opId: eff.opId,
          connectCode: Code.InvalidArgument,
          error: "unsupported security rpc method",
        }),
      });
      return;
    }
    const timeoutMs = eff.method === SecurityRpcMethod.GET_SECURITY_STATES ? 5000 : 15000;
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(new Error("timeout")), timeoutMs);
    try {
      const url = rpcBase(eff.server) + path;
      const headers: Record<string, string> = {
        "Content-Type": "application/proto",
        "Connect-Protocol-Version": "1",
      };
      if (eff.authToken) {
        headers["Authorization"] = `Bearer ${eff.authToken}`;
      }
      const res = await this.fetchFn(url, {
        method: "POST",
        headers,
        body: new Uint8Array(eff.requestBytes),
        signal: ctrl.signal,
      });
      if (!res.ok) {
        let code = httpStatusToConnectCode(res.status);
        let msg = `HTTP ${res.status}`;
        try {
          const text = await res.text();
          if (text) {
            const parsed = JSON.parse(text) as { code?: string; message?: string };
            if (parsed.code && parsed.code in CONNECT_NAME_TO_CODE) {
              code = CONNECT_NAME_TO_CODE[parsed.code];
            }
            if (parsed.message) msg = parsed.message;
          }
        } catch {
          /* Keep fallback HTTP status code/message. */
        }
        this.emit({
          case: "securityRpcResult",
          value: create(SecurityRpcResultEventSchema, {
            opId: eff.opId,
            connectCode: code,
            error: msg,
          }),
        });
        return;
      }
      const responseBytes = new Uint8Array(await res.arrayBuffer());
      this.emit({
        case: "securityRpcResult",
        value: create(SecurityRpcResultEventSchema, {
          opId: eff.opId,
          responseBytes,
        }),
      });
    } catch (err) {
      const msg = errorMessage(err);
      const isTimeout = err instanceof Error && err.name === "AbortError";
      this.emit({
        case: "securityRpcResult",
        value: create(SecurityRpcResultEventSchema, {
          opId: eff.opId,
          connectCode: isTimeout ? Code.DeadlineExceeded : Code.Unavailable,
          error: isTimeout ? "connection timed out" : msg,
          networkError: true,
        }),
      });
    } finally {
      clearTimeout(timer);
    }
  }

  async cryptoBatch(eff: SecurityCryptoBatchEffect): Promise<void> {
    try {
      const results = await Promise.all(
        eff.items.map(async (item): Promise<SecurityCryptoBatchResult> => {
          try {
            switch (item.kind.case) {
              case "verify": {
                const v = item.kind.value;
                if (v.publicKey.byteLength !== 32 || v.signature.byteLength !== 64) {
                  return create(SecurityCryptoBatchResultSchema, { ok: false });
                }
                const pub = await this.importCached(this.edPubKeys, v.publicKey, "Ed25519", ["verify"]);
                const ok = await crypto.subtle.verify("Ed25519", pub, new Uint8Array(v.signature), new Uint8Array(v.message));
                return create(SecurityCryptoBatchResultSchema, { ok });
              }
              case "aeadOpen": {
                const a = item.kind.value;
                const key = await this.importCached(this.aesOpenKeys, a.key, "AES-GCM", ["decrypt"]);
                const pt = new Uint8Array(
                  await crypto.subtle.decrypt(
                    { name: "AES-GCM", iv: new Uint8Array(a.nonce), additionalData: new Uint8Array(a.aad) },
                    key,
                    new Uint8Array(a.ciphertext),
                  ),
                );
                return create(SecurityCryptoBatchResultSchema, { ok: true, data: pt });
              }
              case "aeadSeal": {
                const a = item.kind.value;
                const key = await this.importCached(this.aesSealKeys, a.key, "AES-GCM", ["encrypt"]);
                const ct = new Uint8Array(
                  await crypto.subtle.encrypt(
                    { name: "AES-GCM", iv: new Uint8Array(a.nonce), additionalData: new Uint8Array(a.aad) },
                    key,
                    new Uint8Array(a.plaintext),
                  ),
                );
                return create(SecurityCryptoBatchResultSchema, { ok: true, data: ct });
              }
              case "sha256": {
                const s = item.kind.value;
                const d = new Uint8Array(await crypto.subtle.digest("SHA-256", new Uint8Array(s.data)));
                return create(SecurityCryptoBatchResultSchema, { ok: true, data: d });
              }
              default:
                return create(SecurityCryptoBatchResultSchema, { ok: false, error: "unsupported batch op" });
            }
          } catch (err) {
            return create(SecurityCryptoBatchResultSchema, { ok: false, error: errorMessage(err) });
          }
        }),
      );
      this.emit({
        case: "securityCryptoBatchDone",
        value: create(SecurityCryptoBatchDoneEventSchema, { opId: eff.opId, results }),
      });
    } catch (err) {
      this.emit({
        case: "securityCryptoBatchDone",
        value: create(SecurityCryptoBatchDoneEventSchema, { opId: eff.opId, error: errorMessage(err) }),
      });
    }
  }
}
