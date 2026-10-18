import { create, toBinary, fromBinary } from "@bufbuild/protobuf";
import { afterAll, afterEach, expect, it, vi } from "vitest";
import { newDeviceKey } from "./crypto";
import { initiate } from "./pairing";
import { newPairKey } from "./pair";
import type { Remote } from "./remote";
import {
  newSecurityKey,
  pairContextFromJSON,
  signPairContext,
} from "./device-security";
import {
  GoSecurityFixture,
  webMember,
  removeFixtureBuild,
} from "./test/security-fixture";
import {
  ApprovePairingResponseSchema,
  CreatePairingResponseSchema,
  DeviceKind,
  GetPairingResponseSchema,
  PairingState,
  RevealPairingRequestSchema,
  RevealPairingResponseSchema,
} from "./gen/remoteagent/v1/relay_pb";

afterEach(() => vi.unstubAllGlobals());
afterAll(removeFixtureBuild);
it("reuses exact approved ciphertext after a lost transport reply without consuming the authority context twice", async () => {
  const fixture = await GoSecurityFixture.create();
  try {
    vi.stubGlobal("location", { origin: "https://web.example.com" });
    const { account, sdk, storage } = await webMember(fixture);
    const identity = await sdk.localIdentity();
    const pub = identity.state.core.members.find(
      (m) => m.id === account.deviceId,
    )!.ed;
    const candidate = await newDeviceKey();
    const candidatePub = new Uint8Array(
      await crypto.subtle.exportKey("raw", candidate.publicKey),
    );
    const x = await newSecurityKey();
    const candidateX = new Uint8Array(
      await crypto.subtle.exportKey("raw", x.publicKey),
    );
    const ephemeral = newPairKey();
    const id = crypto.randomUUID();
    let candidateSignature = new Uint8Array(0);
    const requests: Uint8Array[] = [];
    const rpc = vi.spyOn(sdk, "rpc");
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = new Request(input, init);
        const path = new URL(request.url).pathname;
        const response = (body: Uint8Array) =>
          new Response(new Uint8Array(body), {
            headers: { "Content-Type": "application/proto" },
          });
        if (path.endsWith("/CreatePairing"))
          return response(
            toBinary(
              CreatePairingResponseSchema,
              create(CreatePairingResponseSchema, {
                pairingId: id,
                code: "12345678",
                expiresMs: BigInt(Date.now() + 300000),
                approverPublicKey: pub,
              }),
            ),
          );
        if (path.endsWith("/RevealPairing")) {
          const reveal = fromBinary(
            RevealPairingRequestSchema,
            new Uint8Array(await request.arrayBuffer()),
          );
          const context = pairContextFromJSON(
            JSON.parse(new TextDecoder().decode(reveal.securityContext)),
          );
          candidateSignature = new Uint8Array(
            await signPairContext(context, candidate.privateKey, "accept"),
          );
          return response(
            toBinary(
              RevealPairingResponseSchema,
              create(RevealPairingResponseSchema),
            ),
          );
        }
        if (path.endsWith("/GetPairing"))
          return response(
            toBinary(
              GetPairingResponseSchema,
              create(GetPairingResponseSchema, {
                state: requests.length
                  ? PairingState.DELIVERED
                  : PairingState.JOINED,
                joinerPublic: ephemeral.pub,
                joinerKind: DeviceKind.CLIENT,
                devicePublicKey: candidatePub,
                deviceHpkePublicKey: candidateX,
                candidateSignature,
              }),
            ),
          );
        if (path.endsWith("/ApprovePairing")) {
          requests.push(new Uint8Array(await request.arrayBuffer()));
          if (requests.length === 1)
            throw new TypeError("reply lost after committing approval");
          return response(
            toBinary(
              ApprovePairingResponseSchema,
              create(ApprovePairingResponseSchema),
            ),
          );
        }
        throw new Error("unexpected synthetic transport request");
      },
    );
    const pairing = await initiate({
      account,
      security: sdk,
      authHeader: () => "Bearer synthetic-test-token",
    } as Remote);
    await pairing.joined;
    await expect(pairing.approve()).rejects.toThrow("reply lost");
    await pairing.approve();
    expect(requests).toHaveLength(2);
    expect(requests[1]).toEqual(requests[0]);
    expect(rpc.mock.calls.filter(([method]) => method === 6)).toHaveLength(1);
    expect((await storage.loadSecurity()).memberID).toBe(account.deviceId);
  } finally {
    await fixture.fixture.close();
  }
}, 20000);
