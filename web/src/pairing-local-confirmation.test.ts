// An old-root holder can operate a self-signed fake authority. Displaying its
// SAS is not consent: the candidate must verify locally before accepting it.
import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { joinByPairing, PairCanceled } from "./pairing";
import { newPairKey, derivePair, pairDomainTranscript } from "./pair";
import {
  newDeviceKey,
  accountKey,
  deviceID,
  concat,
  importSessionKey,
  sealMessage,
} from "./crypto";
import {
  Method,
  pairTrustDescriptor,
  pairDomainCommit,
  pairContextFromJSON,
  signPairContext,
  stateFromJSON,
  stateBytes,
  type GoPairContext,
} from "./device-security";
import {
  GoSecurityFixture,
  webMember,
  removeFixtureBuild,
} from "./test/security-fixture";
import { PayloadKind } from "./gen/remoteagent/v1/envelope_pb";
import {
  DeviceKind,
  PeekPairingResponseSchema,
  JoinPairingRequestSchema,
  JoinPairingResponseSchema,
  GetPairingRequestSchema,
  GetPairingResponseSchema,
  PairingState,
  PairApprovalSchema,
  RegisterDeviceResponseSchema,
  GetChallengeResponseSchema,
  LoginResponseSchema,
} from "./gen/remoteagent/v1/relay_pb";
import { PairingPayloadSchema } from "./gen/remoteagent/v1/remote_pb";
import type { StoredAccount } from "./storage";
import * as accountStore from "./storage";
import * as securityStore from "./device-security-store";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
afterAll(removeFixtureBuild);
async function hostilePeer(retained = false) {
  const fixture = await GoSecurityFixture.create();
  const attacker = await webMember(fixture);
  const retainedKey = await newDeviceKey();
  const retainedPub = new Uint8Array(
    await crypto.subtle.exportKey("raw", retainedKey.publicKey),
  );
  const existing: StoredAccount | undefined = retained
    ? {
        ...attacker.account,
        devicePrivate: retainedKey.privateKey,
        deviceId: await deviceID(retainedPub),
      }
    : undefined;
  const privateDH = newPairKey();
  const identity = await attacker.sdk.localIdentity();
  const descriptor = pairTrustDescriptor(
    identity.pin,
    attacker.account.deviceId,
  );
  const commit = await pairDomainCommit(
    new Uint8Array(privateDH.pub),
    descriptor,
  );
  const id = crypto.randomUUID();
  let candidateID = "";
  let candidatePub = new Uint8Array(0);
  let candidateX = new Uint8Array(0);
  let joinerPub = new Uint8Array(0);
  let context: GoPairContext | undefined;
  let payload: Awaited<ReturnType<typeof sealMessage>> | undefined;
  let accepted = 0;
  const saved = vi
    .spyOn(accountStore, "saveAccount")
    .mockResolvedValue(undefined);
  const initialized = vi
    .spyOn(securityStore, "initializeSecurity")
    .mockResolvedValue(undefined);
  vi.stubGlobal("localStorage", { setItem: vi.fn() });
  const reply = (bytes: Uint8Array) =>
    new Response(new Uint8Array(bytes), {
      headers: { "Content-Type": "application/proto" },
    });
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/GetChallenge"))
        return reply(
          toBinary(
            GetChallengeResponseSchema,
            create(GetChallengeResponseSchema, {
              challenge: crypto.getRandomValues(new Uint8Array(32)),
            }),
          ),
        );
      if (path.endsWith("/Login"))
        return reply(
          toBinary(
            LoginResponseSchema,
            create(LoginResponseSchema, {
              token: "synthetic-retained-login",
              expiresMs: BigInt(Date.now() + 3600000),
              devicePublicKey: retainedPub,
            }),
          ),
        );
      if (path.endsWith("/PeekPairing"))
        return reply(
          toBinary(
            PeekPairingResponseSchema,
            create(PeekPairingResponseSchema, { pairingId: id, commit }),
          ),
        );
      if (path.endsWith("/JoinPairing")) {
        const joined = fromBinary(
          JoinPairingRequestSchema,
          new Uint8Array(await request.arrayBuffer()),
        );
        candidatePub = new Uint8Array(joined.devicePublicKey);
        candidateX = new Uint8Array(joined.deviceHpkePublicKey);
        joinerPub = new Uint8Array(joined.joinerPublic);
        candidateID = await deviceID(candidatePub);
        context = await attacker.sdk.rpc<GoPairContext>(Method.BeginPair, {
          ID: id,
          Candidate: {
            ID: candidateID,
            EdPublic: [...candidatePub],
            XPublic: [...candidateX],
            Kind: DeviceKind.CLIENT,
          },
        });
        return reply(
          toBinary(
            JoinPairingResponseSchema,
            create(JoinPairingResponseSchema, {
              pairingId: id,
              candidateToken: "synthetic-hostile-candidate",
              commit,
            }),
          ),
        );
      }
      if (path.endsWith("/GetPairing")) {
        const ask = fromBinary(
          GetPairingRequestSchema,
          new Uint8Array(await request.arrayBuffer()),
        );
        if (ask.candidateSignature.length) {
          accepted++;
          const typed = pairContextFromJSON(context!);
          const signature = await signPairContext(
            typed,
            attacker.account.devicePrivate,
            "approve",
          );
          const encoded = await attacker.sdk.rpc<
            Parameters<typeof stateFromJSON>[0]
          >(Method.ApprovePair, {
            ID: id,
            ApproverSignature: Buffer.from(signature).toString("base64"),
            CandidateSignature: Buffer.from(ask.candidateSignature).toString(
              "base64",
            ),
          });
          const state = await stateFromJSON(encoded);
          const keys = await accountKey(fixture.root);
          const approver = identity.state.core.members.find(
            (m) => m.id === attacker.account.deviceId,
          )!;
          const approval = toBinary(
            PairApprovalSchema,
            create(PairApprovalSchema, {
              accountId: await deviceID(new Uint8Array(keys.publicKey)),
              pairingId: id,
              approverDeviceId: attacker.account.deviceId,
              approverPublicKey: approver.ed,
              candidateDevicePublicKey: candidatePub,
              candidateKind: DeviceKind.CLIENT,
              nonce: crypto.getRandomValues(new Uint8Array(16)),
              expiresMs: BigInt(Date.now() + 300000),
            }),
          );
          const proof = new Uint8Array(
            await crypto.subtle.sign(
              "Ed25519",
              attacker.account.devicePrivate,
              concat(
                new TextEncoder().encode("remote-agent/v1/pair-approval\0"),
                approval,
              ),
            ),
          );
          const transcript = await pairDomainTranscript(
            id,
            privateDH.pub,
            joinerPub,
            candidatePub,
            candidateX,
            DeviceKind.CLIENT,
            descriptor,
          );
          const secrets = await derivePair(privateDH, joinerPub, transcript);
          payload = await sealMessage(
            await importSessionKey(new Uint8Array(secrets.key)),
            {
              kind: PayloadKind.PAIRING,
              requestId: id,
              sender: attacker.account.deviceId,
            },
            PairingPayloadSchema,
            create(PairingPayloadSchema, {
              server: attacker.account.server,
              root: fixture.rootRaw,
              approval,
              approvalSignature: proof,
              securityState: await stateBytes(state),
            }),
          );
        }
        return reply(
          toBinary(
            GetPairingResponseSchema,
            create(GetPairingResponseSchema, {
              state: payload ? PairingState.APPROVED : PairingState.REVEALED,
              commit,
              initiatorPublic: privateDH.pub,
              securityContext: new TextEncoder().encode(
                JSON.stringify(context),
              ),
              payload,
            }),
          ),
        );
      }
      if (path.endsWith("/RegisterDevice"))
        return reply(
          toBinary(
            RegisterDeviceResponseSchema,
            create(RegisterDeviceResponseSchema, { deviceId: candidateID }),
          ),
        );
      throw new Error("unexpected synthetic hostile relay operation");
    },
  );
  return {
    fixture,
    attacker,
    existing,
    saved,
    initialized,
    accepted: () => accepted,
  };
}

it("does not auto-pin even a fully valid fake authority when local confirmation is absent", async () => {
  const attack = await hostilePeer();
  try {
    await expect(
      joinByPairing(
        { server: attack.attacker.account.server, code: "12345678" },
        () => undefined,
        () => false,
      ),
    ).rejects.toBeInstanceOf(PairCanceled);
    expect(attack.accepted()).toBe(0);
    expect(attack.saved).not.toHaveBeenCalled();
    expect(attack.initialized).not.toHaveBeenCalled();
  } finally {
    await attack.fixture.fixture.close();
  }
}, 20000);

it("waits for and honors the candidate's local SAS refusal before signing or storing trust", async () => {
  const attack = await hostilePeer();
  try {
    let decide!: (allow: boolean) => void;
    let visible!: () => void;
    const shown = new Promise<void>((resolve) => {
      visible = resolve;
    });
    const joining = joinByPairing(
      { server: attack.attacker.account.server, code: "12345678" },
      () => undefined,
      () => false,
      () => undefined,
      undefined,
      async () => {
        visible();
        return new Promise<boolean>((resolve) => {
          decide = resolve;
        });
      },
    );
    const outcome = joining.catch((error) => error);
    await shown;
    expect(attack.accepted()).toBe(0);
    expect(attack.saved).not.toHaveBeenCalled();
    decide(false);
    expect(await outcome).toBeInstanceOf(PairCanceled);
    expect(attack.accepted()).toBe(0);
    expect(attack.initialized).not.toHaveBeenCalled();
  } finally {
    await attack.fixture.fixture.close();
  }
}, 20000);

// Positive control for the attack model: this deliberately simulates the old
// automatic consent. An old-root match and valid signatures cannot replace SAS.
it("shows that old-root equality alone permits a fully fake authority if the app auto-confirms", async () => {
  const attack = await hostilePeer(true);
  try {
    const account = await joinByPairing(
      { server: attack.attacker.account.server, code: "12345678" },
      () => undefined,
      () => false,
      () => undefined,
      attack.existing,
      async () => true,
    );
    expect(account.deviceId).toBe(attack.existing!.deviceId);
    expect(attack.accepted()).toBe(1);
    expect(attack.initialized).toHaveBeenCalledOnce();
  } finally {
    await attack.fixture.fixture.close();
  }
}, 20000);
it("keeps an existing account unmodified when its user refuses the fake authority", async () => {
  const attack = await hostilePeer(true);
  try {
    await expect(
      joinByPairing(
        { server: attack.attacker.account.server, code: "12345678" },
        () => undefined,
        () => false,
        () => undefined,
        attack.existing,
        async () => false,
      ),
    ).rejects.toBeInstanceOf(PairCanceled);
    expect(attack.accepted()).toBe(0);
    expect(attack.initialized).not.toHaveBeenCalled();
    expect(attack.saved).not.toHaveBeenCalled();
  } finally {
    await attack.fixture.fixture.close();
  }
}, 20000);
