import { expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { SecurityPairingInfoSchema } from "./gen/remoteagent/core/v1/security_pb";
import { Remote } from "./remote";
import type { StoredAccount } from "./storage";

class Probe extends Remote {
  constructor() { super({ server: "https://example.com", deviceId: "synthetic-client" } as StoredAccount, undefined); }
}

it("delegates startPairing through securityClient cleanly", async () => {
  const r = new Probe();
  const startPairing = vi.fn(async () => create(SecurityPairingInfoSchema, { pairingId: "p-1", code: "12345678" }));
  (r as unknown as { attachHost(b: unknown): void }).attachHost({
    securityClient: { startPairing },
    start: () => {},
    stop: () => {},
  });
  const res = await r.startPairing("https://example.com", "old-machine");
  expect(startPairing).toHaveBeenCalledWith("https://example.com", "old-machine");
  expect(res.pairingId).toBe("p-1");
  r.stop();
});
