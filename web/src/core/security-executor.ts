import { create } from "@bufbuild/protobuf";
import {
  SecurityContentKeySchema,
  SecurityCryptoBatchDoneEventSchema,
  SecurityCryptoBatchResultSchema,
  SecurityKeyAgreedEventSchema,
  SecurityKeySignedEventSchema,
  SecurityRecordEventSchema,
  type SecurityCryptoBatchEffect,
  type SecurityCryptoBatchResult,
  type SecurityKeyAgreeEffect,
  type SecurityKeySignEffect,
  type SecurityRecordEvent,
} from "../gen/remoteagent/core/v1/security_pb";
import { EventSchema as CoreEventSchema, type Event as CoreEvent } from "../gen/remoteagent/core/v1/app_pb";
import {
  SecurityError,
  ed25519SeedPublic,
  ed25519SeedSign,
  makeSecPin,
  secCall,
  secCallWith,
  x25519DeriveWithPrivateKey,
  x25519PrivateKeyFromSeed,
  x25519SeedPublicKey,
} from "./sec-call";

export {
  SecurityError,
  ed25519SeedPublic,
  ed25519SeedSign,
  makeSecPin,
  secCall,
  secCallWith,
  x25519DeriveWithPrivateKey,
  x25519PrivateKeyFromSeed,
  x25519SeedPublicKey,
};

function hexKey(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
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
 * Executes asynchronous non-extractable WebCrypto operations requested by clientcore/security:
 * Ed25519 leaf signing, X25519 DH agreement, and parallel WebCrypto batch verify / AES-GCM / SHA-256.
 */
export class WebSecurityExecutor {
  private readonly xPubKeys = new Map<string, Promise<CryptoKey>>();
  private readonly edPubKeys = new Map<string, Promise<CryptoKey>>();
  private readonly aesOpenKeys = new Map<string, Promise<CryptoKey>>();
  private readonly aesSealKeys = new Map<string, Promise<CryptoKey>>();
  private closed = false;

  constructor(
    private readonly options: {
      devicePrivate: () => CryptoKey | undefined;
      xPrivate: () => CryptoKey | undefined;
      emit: (ev: CoreEvent) => void;
    },
  ) {}

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

  async keySign(eff: SecurityKeySignEffect): Promise<void> {
    try {
      const priv = this.options.devicePrivate();
      if (!priv) {
        this.emit({
          case: "securityKeySigned",
          value: create(SecurityKeySignedEventSchema, { opId: eff.opId, error: "missing device signing key" }),
        });
        return;
      }
      const sig = new Uint8Array(await crypto.subtle.sign("Ed25519", priv, new Uint8Array(eff.message)));
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
      const priv = this.options.xPrivate();
      if (!priv || eff.peerPublic.byteLength !== 32) {
        this.emit({
          case: "securityKeyAgreed",
          value: create(SecurityKeyAgreedEventSchema, { opId: eff.opId, error: "missing X25519 key or invalid peer public" }),
        });
        return;
      }
      const pub = await this.importCached(this.xPubKeys, eff.peerPublic, "X25519", []);
      const shared = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: pub }, priv, 256));
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
