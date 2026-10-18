import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { importRoot, newDeviceKey } from "./crypto";
import { initiate } from "./pairing";
import { newPairKey } from "./pair";
import { type Remote } from "./remote";
import { type StoredAccount } from "./storage";
import {
  ApprovePairingResponseSchema, CreatePairingResponseSchema, DeviceKind,
  GetPairingResponseSchema, PairingState, RevealPairingResponseSchema,
} from "./gen/remoteagent/v1/relay_pb";

afterEach(() => vi.unstubAllGlobals());

it("reuses the exact signed approval after the server commits but the reply is lost", async () => {
  vi.stubGlobal("location", { origin: "https://web.example.com" });
  const device = await newDeviceKey();
  const pub = new Uint8Array(await crypto.subtle.exportKey("raw", device.publicKey));
  const rootRaw = crypto.getRandomValues(new Uint8Array(32));
  const { deviceID } = await import("./crypto");
  const account: StoredAccount = { server: "https://relay.example.com", rootRaw, root: await importRoot(rootRaw), devicePrivate: device.privateKey, deviceId: await deviceID(pub) };
  const candidate = await newDeviceKey();
  const candidatePub = new Uint8Array(await crypto.subtle.exportKey("raw", candidate.publicKey));
  const ephemeral = newPairKey();
  const id = "0123456789abcdef0123456789abcdef";
  const requests: Uint8Array[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    const path = new URL(request.url).pathname;
    const response = (body: Uint8Array) => new Response(body as Uint8Array<ArrayBuffer>, { headers: { "Content-Type": "application/proto" } });
    if (path.endsWith("/CreatePairing")) return response(toBinary(CreatePairingResponseSchema, create(CreatePairingResponseSchema, { pairingId: id, code: "12345678", expiresMs: BigInt(Date.now() + 300_000), approverPublicKey: pub })));
    if (path.endsWith("/RevealPairing")) return response(toBinary(RevealPairingResponseSchema, create(RevealPairingResponseSchema)));
    if (path.endsWith("/GetPairing")) return response(toBinary(GetPairingResponseSchema, create(GetPairingResponseSchema, { state: requests.length ? PairingState.DELIVERED : PairingState.JOINED, joinerPublic: ephemeral.pub, joinerKind: DeviceKind.CLIENT, devicePublicKey: candidatePub })));
    if (path.endsWith("/ApprovePairing")) {
      requests.push(new Uint8Array(await request.arrayBuffer()));
      if (requests.length === 1) throw new TypeError("reply lost after committing approval");
      return response(toBinary(ApprovePairingResponseSchema, create(ApprovePairingResponseSchema)));
    }
    throw new Error("unexpected RPC in isolated test");
  });
  const pairing = await initiate({ account, authHeader: () => "Bearer synthetic-test-token" } as Remote);
  await pairing.joined;
  await expect(pairing.approve()).rejects.toThrow("reply lost");
  await pairing.approve();
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
});
