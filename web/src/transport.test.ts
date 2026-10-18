import { afterEach, expect, it, vi } from "vitest";
import { relayFetch } from "./transport";
import { checkServer } from "./crypto";
import { apiBase, joinWithApproval } from "./remote";
import { joinByPairing, parsePairLink } from "./pairing";

afterEach(() => vi.unstubAllGlobals());

it("rejects insecure remote relay URLs before pairing or registration sends anything", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  for (const server of ["http://relay.example", "ftp://relay.example", "https://user:password@relay.example"]) {
    expect(() => checkServer(server)).toThrow();
    expect(() => apiBase(server)).toThrow();
    expect(parsePairLink(new URLSearchParams({ code: "12345678", s: server }).toString())).toBeUndefined();
    await expect(joinByPairing({ code: "12345678", server }, () => {}, () => false)).rejects.toThrow();
    await expect(
      joinWithApproval(new Uint8Array(32), server, {} as never),
    ).rejects.toThrow();
  }
  expect(fetch).not.toHaveBeenCalled();
  expect(checkServer("https://relay.example/")).toBe("https://relay.example");
  for (const host of ["localhost", "127.0.0.1", "[::1]"]) expect(checkServer(`http://${host}:8080`)).toBe(`http://${host}:8080`);
});

it("treats malformed pairing links as invalid instead of crashing setup", () => {
  expect(parsePairLink("code=12345678&s=https://relay.example&c=%" )).toBeUndefined();
  expect(parsePairLink("code=12345678&s=https://relay.example&c=AQ" )).toBeUndefined();
  expect(parsePairLink("code=12345678&s=https://relay.example" )?.server).toBe("https://relay.example");
});

it("fails closed on RPC redirects even if the caller asks to follow them", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response());
  vi.stubGlobal("fetch", fetch);
  await relayFetch("https://relay.example", { method: "POST", body: "sensitive", redirect: "follow" });
  expect(fetch).toHaveBeenCalledWith("https://relay.example", { method: "POST", body: "sensitive", redirect: "error" });
});
