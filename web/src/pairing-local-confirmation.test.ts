// Displaying the four emoji is not consent: the candidate must confirm them
// locally before it registers or stores any security state.
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import {
  checkPairChainSize,
  joinByPairing,
  PairCanceled,
} from "./pairing";
import { newPairKey } from "./pair";
import {
  pairDomainCommit,
  pairTrustDescriptor,
  type Pin,
} from "./device-security";
import {
  PeekPairingRequestSchema,
  PeekPairingResponseSchema,
  JoinPairingRequestSchema,
  JoinPairingResponseSchema,
  GetPairingResponseSchema,
  PairingState,
} from "./gen/remoteagent/v1/relay_pb";
import * as accountStore from "./storage";
import * as securityStore from "./device-security-store";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const server = "https://ra.example.com";
const pin = (): Pin => ({
  account: "0123456789abcdef0123456789abcdef",
  domain: crypto.getRandomValues(new Uint8Array(32)),
  genesis: crypto.getRandomValues(new Uint8Array(32)),
});
const approver = "fedcba9876543210fedcba9876543210";

async function peer(options: { badCommit?: boolean; calls: { registered: number } }) {
  const initiator = newPairKey();
  const trust = pairTrustDescriptor(pin(), approver);
  const commit = await pairDomainCommit(
    new Uint8Array(initiator.pub),
    trust,
  );
  const id = crypto.randomUUID();
  const reply = (bytes: Uint8Array) =>
    new Response(new Uint8Array(bytes), {
      headers: { "Content-Type": "application/proto" },
    });
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/PeekPairing")) {
        const peek = fromBinary(
          PeekPairingRequestSchema,
          new Uint8Array(await request.arrayBuffer()),
        );
        expect(peek.protocolVersion).toBe(10);
        return reply(
          toBinary(
            PeekPairingResponseSchema,
            create(PeekPairingResponseSchema, { pairingId: id, commit }),
          ),
        );
      }
      if (path.endsWith("/JoinPairing")) {
        const join = fromBinary(
          JoinPairingRequestSchema,
          new Uint8Array(await request.arrayBuffer()),
        );
        expect(join.protocolVersion).toBe(10);
        return reply(
          toBinary(
            JoinPairingResponseSchema,
            create(JoinPairingResponseSchema, {
              pairingId: id,
              commit,
              candidateToken: "synthetic-candidate",
            }),
          ),
        );
      }
      if (path.endsWith("/GetPairing"))
        return reply(
          toBinary(
            GetPairingResponseSchema,
            create(GetPairingResponseSchema, {
              state: PairingState.REVEALED,
              commit,
              initiatorPublic: new Uint8Array(initiator.pub),
              // A lying relay it is not: the descriptor must match the
              // commitment the code carried.
              trust: options.badCommit
                ? pairTrustDescriptor(
                    { ...pin(), genesis: new Uint8Array(32).fill(9) },
                    approver,
                  )
                : trust,
            }),
          ),
        );
      if (path.endsWith("/RegisterDevice")) options.calls.registered++;
      throw new Error("unexpected synthetic pairing operation");
    },
  );
}

it("refuses a pairing chain above the Go limits", () => {
  const one = { state: new Uint8Array(100), recovery: new Uint8Array() };
  checkPairChainSize([one]);
  expect(() =>
    checkPairChainSize(Array.from({ length: 257 }, () => one)),
  ).toThrow(/too large to pair/);
  expect(() =>
    checkPairChainSize([
      { state: new Uint8Array(800 << 10), recovery: new Uint8Array(1) },
    ]),
  ).toThrow(/too large to pair/);
  expect(() => checkPairChainSize([])).toThrow(/too large to pair/);
});

it("does not register or store anything when the local SAS confirmation is absent", async () => {
  const calls = { registered: 0 };
  await peer({ calls });
  const saved = vi.spyOn(accountStore, "saveAccount").mockResolvedValue();
  const initialized = vi
    .spyOn(securityStore, "initializeSecurity")
    .mockResolvedValue();
  let emoji: string[] | undefined;
  await expect(
    joinByPairing(
      { server, code: "12345678" },
      (e) => (emoji = e),
      () => false,
      () => undefined,
      undefined,
      async () => false,
    ),
  ).rejects.toBeInstanceOf(PairCanceled);
  expect(emoji).toHaveLength(4);
  expect(calls.registered).toBe(0);
  expect(saved).not.toHaveBeenCalled();
  expect(initialized).not.toHaveBeenCalled();
}, 20000);

it("stops before showing the emoji when the phone's trust descriptor does not match", async () => {
  const calls = { registered: 0 };
  await peer({ badCommit: true, calls });
  let shown = false;
  await expect(
    joinByPairing(
      { server, code: "12345678" },
      () => (shown = true),
      () => false,
      () => undefined,
      undefined,
      async () => true,
    ),
  ).rejects.toThrow();
  expect(shown).toBe(false);
  expect(calls.registered).toBe(0);
}, 20000);
