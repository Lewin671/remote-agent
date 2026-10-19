import {
  savePairingIdentity,
  loadPairingIdentity,
} from "./device-security-store";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import {
  PairApprovalSchema,
  RegisterDeviceResponseSchema,
} from "./gen/remoteagent/v1/relay_pb";
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import { deviceID, concat } from "./crypto";
import { joinWithApproval, Remote } from "./remote";
import { loadAccount } from "./storage";
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it("persists the exact approved registration before account state and resumes it on startup", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("localStorage", { setItem: vi.fn(), getItem: () => null });
  const device = (await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ])) as CryptoKeyPair;
  const signer = (await crypto.subtle.generateKey("Ed25519", false, [
    "sign",
    "verify",
  ])) as CryptoKeyPair;
  const pub = new Uint8Array(
    await crypto.subtle.exportKey("raw", device.publicKey),
  );
  const approver = new Uint8Array(
    await crypto.subtle.exportKey("raw", signer.publicKey),
  );
  const account = crypto.getRandomValues(new Uint8Array(32));
  const approval = toBinary(
    PairApprovalSchema,
    create(PairApprovalSchema, {
      accountId: await deviceID(account),
      pairingId: "synthetic-pair",
      approverDeviceId: await deviceID(approver),
      approverPublicKey: approver,
      candidateDevicePublicKey: pub,
      candidateKind: DeviceKind.CLIENT,
      expiresMs: BigInt(Date.now() + 60000),
      nonce: new Uint8Array(16).fill(1),
    }),
  );
  const signature = new Uint8Array(
    await crypto.subtle.sign(
      "Ed25519",
      signer.privateKey,
      concat(
        new TextEncoder().encode("remote-agent/v1/pair-approval\0"),
        approval,
      ),
    ),
  );
  const x = (await crypto.subtle.generateKey("X25519", false, [
    "deriveBits",
  ])) as CryptoKeyPair;
  await savePairingIdentity({
    server: "https://relay.example.com",
    pairingId: "synthetic-pair",
    device,
    x,
  });
  let first: Uint8Array | undefined;
  let calls = 0;
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const r = new Request(input, init);
      expect(new URL(r.url).pathname).toContain("RegisterDevice");
      const raw = new Uint8Array(await r.arrayBuffer());
      calls++;
      if (!first) {
        first = raw;
        return new Response(
          '{"code":"unavailable","message":"synthetic outage"}',
          { status: 503, headers: { "Content-Type": "application/json" } },
        );
      }
      expect(raw).toEqual(first);
      return new Response(
        toBinary(
          RegisterDeviceResponseSchema,
          create(RegisterDeviceResponseSchema, {
            deviceId: await deviceID(pub),
          }),
        ),
        { headers: { "Content-Type": "application/proto" } },
      );
    },
  );
  await expect(
    joinWithApproval(account, "https://relay.example.com", {
      device,
      pairingId: "synthetic-pair",
      approval,
      approvalSignature: signature,
    }),
  ).rejects.toThrow();
  // Restart's loader must finish registration before exposing the saved account.
  vi.spyOn(Remote.prototype as any, "warm").mockResolvedValue(undefined);
  expect(await loadAccount()).toBeDefined();
  expect(await Remote.load()).toBeDefined();
  expect(calls).toBe(2);
  expect(
    await loadPairingIdentity("https://relay.example.com", "synthetic-pair"),
  ).toBeUndefined();
});
