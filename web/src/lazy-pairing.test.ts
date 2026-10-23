import { expect, it, vi } from "vitest";
import { Remote } from "./remote";
import type { StoredAccount } from "./storage";
const loaded = vi.hoisted(() => ({ authority: 0, curves: 0, start: vi.fn(async () => "started") }));
vi.mock("./authority-pairing", () => {
  loaded.authority++;
  return { AuthorityPairing: { start: loaded.start } };
});
vi.mock("./pair", () => {
  loaded.curves++;
  return { newPairKey: vi.fn(), derivePair: vi.fn(), pairDomainTranscript: vi.fn() };
});
class Probe extends Remote {
  constructor() { super({ server: "https://example.com", deviceId: "synthetic-client" } as StoredAccount, undefined); }
}
it("does not load pairing implementation or curves until pairing is requested", async () => {
  const r = new Probe();
  expect(loaded.authority).toBe(0);
  expect(loaded.curves).toBe(0);
  await r.startPairing("https://example.com", "old-machine");
  expect(loaded.authority).toBe(1);
  expect(loaded.start).toHaveBeenCalledWith(r.authority, expect.anything(), "https://example.com", "old-machine");
  r.stop();
});
