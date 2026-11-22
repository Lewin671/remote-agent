import { afterEach, expect, it, vi } from "vitest";
import { relayFetch, relayEpoch } from "./transport";
import { checkServer } from "./crypto";
import { apiBase } from "./remote";
import { parsePairLinkQuery } from "./core/security-client";

afterEach(() => vi.unstubAllGlobals());

it("rejects insecure remote relay URLs before pairing or registration sends anything", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  for (const server of ["http://relay.example", "ftp://relay.example", "https://user:password@relay.example"]) {
    expect(() => checkServer(server)).toThrow();
    expect(() => apiBase(server)).toThrow();
    expect(parsePairLinkQuery(new URLSearchParams({ code: "12345678", s: server }).toString())).toBeUndefined();
  }
  expect(fetch).not.toHaveBeenCalled();
  expect(checkServer("https://relay.example/")).toBe("https://relay.example");
  for (const host of ["localhost", "127.0.0.1", "[::1]"]) expect(checkServer(`http://${host}:8080`)).toBe(`http://${host}:8080`);
});

it("treats malformed pairing links as invalid instead of crashing setup", () => {
  expect(parsePairLinkQuery("code=12345678&s=https://relay.example&c=%" )).toBeUndefined();
  expect(parsePairLinkQuery("code=12345678&s=https://relay.example&c=AQ" )).toBeUndefined();
  expect(parsePairLinkQuery("code=12345678&s=https://relay.example" )?.server).toBe("https://relay.example");
});

it("fails closed on RPC redirects even if the caller asks to follow them", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response());
  vi.stubGlobal("fetch", fetch);
  await relayFetch("https://relay.example", { method: "POST", body: "sensitive", redirect: "follow" });
  expect(fetch).toHaveBeenCalledWith("https://relay.example", { method: "POST", body: "sensitive", redirect: "error" });
});

it("captures epoch in replayable body while later responses update the next attempt", async () => {
  const origin = "https://epoch-fixture.example";
  const bodies: string[] = [];
  const fetch = vi.fn(async (_url: unknown, init?: RequestInit) => {
    if (init?.method === "POST") bodies.push(String(init.body));
    return new Response("", { headers: { "Remote-Agent-Relay-Epoch": bodies.length ? "new-instance" : "first-instance" } });
  });
  vi.stubGlobal("fetch", fetch);
  const epoch = await relayEpoch(origin);
  const body = JSON.stringify({ attemptId: "original", relayEpoch: epoch });
  await relayFetch(origin + "/Forward", { method: "POST", body });
  // Model an opaque replay: it reuses the complete request body.
  await relayFetch(origin + "/Forward", { method: "POST", body });
  expect(bodies).toEqual([body, body]);
  expect(JSON.parse(body).relayEpoch).toBe("first-instance");
  expect(await relayEpoch(origin)).toBe("new-instance");
  expect(fetch).toHaveBeenCalledTimes(3);
});
