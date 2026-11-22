// @vitest-environment happy-dom

import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CoreHost, type CoreExecutor } from "./host";
import { WebSecurityExecutor } from "./security-executor";
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CommandOutcomeEventSchema,
  EventSchema as CoreEventSchema,
  type Event as CoreEvent,
  OutcomeSource,
  TokenResultEventSchema,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  CryptoBatchAeadOpenItemSchema,
  CryptoBatchAeadSealItemSchema,
  CryptoBatchSha256ItemSchema,
  CryptoBatchVerifyItemSchema,
  SecurityCryptoBatchEffectSchema,
  SecurityCryptoBatchItemSchema,
  SecurityKeyAgreeEffectSchema,
  SecurityKeyKind,
  SecurityKeySignEffectSchema,
  SecurityStorageResultEventSchema,
} from "../gen/remoteagent/core/v1/security_pb";
import vectors from "../../../internal/deviceauth/testdata/domain-v3.json";
import { concat, fromHex, utf8 } from "../crypto";

const fromB64Url = (s: string): Uint8Array<ArrayBuffer> =>
  Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));

function waitForEvent(events: CoreEvent[], predicate: (ev: CoreEvent) => boolean): Promise<CoreEvent> {
  return vi.waitFor(() => {
    const found = events.find(predicate);
    if (!found) throw new Error("event not emitted yet");
    return found;
  });
}

describe("WebSecurityExecutor", () => {
  it("signs payloads with devicePrivate and reports missing signing key", async () => {
    const seed = fromHex(vectors.phone_seed);
    const pkcs8Signer = await crypto.subtle.importKey(
      "pkcs8",
      concat(fromHex("302e020100300506032b657004220420"), seed),
      "Ed25519",
      true,
      ["sign"],
    );
    const jwk = await crypto.subtle.exportKey("jwk", pkcs8Signer);
    const rawPub = fromB64Url(jwk.x!);

    const emitted: CoreEvent[] = [];
    let currentKey: CryptoKey | undefined = pkcs8Signer;
    const exec = new WebSecurityExecutor({
      devicePrivate: () => currentKey,
      xPrivate: () => undefined,
      emit: (ev) => emitted.push(ev),
    });

    const msg = utf8("test-sign-payload");
    await exec.keySign(create(SecurityKeySignEffectSchema, {
      opId: 1n,
      keyKind: SecurityKeyKind.DEVICE_ED,
      message: msg,
    }));
    const okEv = await waitForEvent(emitted, (e) => e.event.case === "securityKeySigned" && e.event.value.opId === 1n);
    if (okEv.event.case !== "securityKeySigned") throw new Error("unexpected event");
    expect(okEv.event.value.error).toBe("");
    const verifyKey = await crypto.subtle.importKey("raw", rawPub, "Ed25519", false, ["verify"]);
    const valid = await crypto.subtle.verify(
      "Ed25519",
      verifyKey,
      new Uint8Array(okEv.event.value.signature),
      msg,
    );
    expect(valid).toBe(true);

    currentKey = undefined;
    await exec.keySign(create(SecurityKeySignEffectSchema, {
      opId: 2n,
      keyKind: SecurityKeyKind.DEVICE_ED,
      message: msg,
    }));
    const errEv = await waitForEvent(emitted, (e) => e.event.case === "securityKeySigned" && e.event.value.opId === 2n);
    if (errEv.event.case !== "securityKeySigned") throw new Error("unexpected event");
    expect(errEv.event.value.error).toBeTruthy();
  });

  it("derives X25519 shared secret and handles missing private key or invalid peer key", async () => {
    const alice = await crypto.subtle.generateKey("X25519", false, ["deriveBits"]) as CryptoKeyPair;
    const bob = await crypto.subtle.generateKey("X25519", false, ["deriveBits"]) as CryptoKeyPair;
    const bobPub = new Uint8Array(await crypto.subtle.exportKey("raw", bob.publicKey));
    const alicePub = new Uint8Array(await crypto.subtle.exportKey("raw", alice.publicKey));
    const expected = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: alice.publicKey }, bob.privateKey, 256));
    void alicePub;

    const emitted: CoreEvent[] = [];
    let currentX: CryptoKey | undefined = alice.privateKey;
    const exec = new WebSecurityExecutor({
      devicePrivate: () => undefined,
      xPrivate: () => currentX,
      emit: (ev) => emitted.push(ev),
    });

    await exec.keyAgree(create(SecurityKeyAgreeEffectSchema, {
      opId: 10n,
      keyKind: SecurityKeyKind.DEVICE_X,
      peerPublic: bobPub,
    }));
    const okEv = await waitForEvent(emitted, (e) => e.event.case === "securityKeyAgreed" && e.event.value.opId === 10n);
    if (okEv.event.case !== "securityKeyAgreed") throw new Error("unexpected event");
    expect(okEv.event.value.error).toBe("");
    expect(new Uint8Array(okEv.event.value.shared)).toEqual(expected);

    await exec.keyAgree(create(SecurityKeyAgreeEffectSchema, {
      opId: 11n,
      keyKind: SecurityKeyKind.DEVICE_X,
      peerPublic: new Uint8Array(16),
    }));
    const badPeer = await waitForEvent(emitted, (e) => e.event.case === "securityKeyAgreed" && e.event.value.opId === 11n);
    if (badPeer.event.case !== "securityKeyAgreed") throw new Error("unexpected event");
    expect(badPeer.event.value.error).toBeTruthy();

    currentX = undefined;
    await exec.keyAgree(create(SecurityKeyAgreeEffectSchema, {
      opId: 12n,
      keyKind: SecurityKeyKind.DEVICE_X,
      peerPublic: bobPub,
    }));
    const noKey = await waitForEvent(emitted, (e) => e.event.case === "securityKeyAgreed" && e.event.value.opId === 12n);
    if (noKey.event.case !== "securityKeyAgreed") throw new Error("unexpected event");
    expect(noKey.event.value.error).toBeTruthy();
  });

  it("executes mixed verify, aeadSeal, aeadOpen, and sha256 items in cryptoBatch", async () => {
    const keyPair = await crypto.subtle.generateKey("Ed25519", false, ["sign", "verify"]) as CryptoKeyPair;
    const rawPub = new Uint8Array(await crypto.subtle.exportKey("raw", keyPair.publicKey));
    const msg = utf8("batch-verify-msg");
    const sig = new Uint8Array(await crypto.subtle.sign("Ed25519", keyPair.privateKey, msg));
    const badSig = sig.slice();
    badSig[0] ^= 1;

    const aesKey = crypto.getRandomValues(new Uint8Array(32));
    const nonce = crypto.getRandomValues(new Uint8Array(12));
    const aad = utf8("batch-aad");
    const plain = utf8("batch-secret-payload");
    const shaIn = utf8("batch-digest-input");
    const expectedSha = new Uint8Array(await crypto.subtle.digest("SHA-256", shaIn));

    const emitted: CoreEvent[] = [];
    const exec = new WebSecurityExecutor({
      devicePrivate: () => undefined,
      xPrivate: () => undefined,
      emit: (ev) => emitted.push(ev),
    });

    await exec.cryptoBatch(create(SecurityCryptoBatchEffectSchema, {
      opId: 20n,
      items: [
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "aeadSeal", value: create(CryptoBatchAeadSealItemSchema, { key: aesKey, nonce, aad, plaintext: plain }) },
        }),
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "verify", value: create(CryptoBatchVerifyItemSchema, { publicKey: rawPub, message: msg, signature: sig }) },
        }),
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "verify", value: create(CryptoBatchVerifyItemSchema, { publicKey: rawPub, message: msg, signature: badSig }) },
        }),
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "sha256", value: create(CryptoBatchSha256ItemSchema, { data: shaIn }) },
        }),
      ],
    }));

    const batch1 = await waitForEvent(emitted, (e) => e.event.case === "securityCryptoBatchDone" && e.event.value.opId === 20n);
    if (batch1.event.case !== "securityCryptoBatchDone") throw new Error("unexpected event");
    expect(batch1.event.value.error).toBe("");
    const r1 = batch1.event.value.results;
    expect(r1).toHaveLength(4);
    expect(r1[0].ok).toBe(true);
    expect(r1[1].ok).toBe(true);
    expect(r1[2].ok).toBe(false);
    expect(r1[3].ok).toBe(true);
    expect(new Uint8Array(r1[3].data)).toEqual(expectedSha);

    const ct = new Uint8Array(r1[0].data);
    await exec.cryptoBatch(create(SecurityCryptoBatchEffectSchema, {
      opId: 21n,
      items: [
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "aeadOpen", value: create(CryptoBatchAeadOpenItemSchema, { key: aesKey, nonce, aad, ciphertext: ct }) },
        }),
        create(SecurityCryptoBatchItemSchema, {
          kind: { case: "aeadOpen", value: create(CryptoBatchAeadOpenItemSchema, { key: aesKey, nonce, aad: utf8("wrong-aad"), ciphertext: ct }) },
        }),
      ],
    }));

    const batch2 = await waitForEvent(emitted, (e) => e.event.case === "securityCryptoBatchDone" && e.event.value.opId === 21n);
    if (batch2.event.case !== "securityCryptoBatchDone") throw new Error("unexpected event");
    expect(batch2.event.value.results[0].ok).toBe(true);
    expect(new Uint8Array(batch2.event.value.results[0].data)).toEqual(plain);
    expect(batch2.event.value.results[1].ok).toBe(false);
  });
});

describe("CoreHost cold-boot security ordering", () => {
  const hosts: CoreHost[] = [];
  afterEach(() => {
    vi.restoreAllMocks();
    while (hosts.length) hosts.pop()!.dispose();
  });

  it("dispatches securityConfig -> security bootstrap before cacheBootstrapLoaded during warmLocalCache", async () => {
    const dispatched: Array<CoreEvent["event"]["case"]> = [];
    const origDispatch = (CoreHost.prototype as unknown as { dispatch(ev: CoreEvent, cb?: () => void): void }).dispatch;
    vi.spyOn(CoreHost.prototype as unknown as { dispatch(ev: CoreEvent, cb?: () => void): void }, "dispatch").mockImplementation(function (
      this: CoreHost,
      ev: CoreEvent,
      cb?: () => void,
    ) {
      dispatched.push(ev.event.case);
      return origDispatch.call(this, ev, cb);
    });

    let host!: CoreHost;
    const exec: CoreExecutor = {
      openStreamLeg() {},
      closeStreamLeg() {},
      async ensureToken(opId) {
        return create(TokenResultEventSchema, { opId, token: "t", expiresMs: BigInt(Date.now() + 60_000) });
      },
      discardToken() {},
      securityConfig() {
        return { account: vectors.account, deviceId: "dev-client" };
      },
      securityStorage(eff) {
        host.dispatch(
          create(CoreEventSchema, {
            event: {
              case: "securityStorageResult",
              value: create(SecurityStorageResultEventSchema, { opId: eff.opId }),
            },
          }),
        );
      },
      async readBootstrapCache(opId) {
        return create(CacheBootstrapLoadedEventSchema, {
          opId,
          homeScopeToken: "all",
          scopeHmacKey: new Uint8Array(32).fill(5),
        });
      },
      async readEventsCache(opId, sessionId) {
        return create(CacheEventsLoadedEventSchema, { opId, sessionId });
      },
      async writeCache() {},
      async deleteCache() {},
      async executeRpc() {
        return create(CoreEventSchema, {});
      },
      async executeCommandAttempt(eff) {
        return create(CommandOutcomeEventSchema, { requestId: eff.requestId, attemptId: eff.attemptId, source: OutcomeSource.DAEMON });
      },
      applySyncStateView() {},
    };

    host = new CoreHost(undefined, { executor: exec });
    hosts.push(host);
    await host.warmLocalCache("/");

    const cfgIdx = dispatched.indexOf("securityConfig");
    const actIdx = dispatched.indexOf("securityAction");
    const bootIdx = dispatched.indexOf("cacheBootstrapLoaded");
    expect(cfgIdx).toBeGreaterThanOrEqual(0);
    expect(actIdx).toBeGreaterThan(cfgIdx);
    expect(bootIdx).toBeGreaterThan(actIdx);
  });
});
