import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { retainedIdentityDenied } from "./pairing";
import * as signing from "./crypto";
import type { StoredAccount } from "./storage";
import { GetChallengeResponseSchema, LoginResponseSchema } from "./gen/remoteagent/v1/relay_pb";

const account = { server: "https://relay.example.com", deviceId: "synthetic-device" } as StoredAccount;
const challenge = () => new Response(new Uint8Array(toBinary(GetChallengeResponseSchema,
  create(GetChallengeResponseSchema, { challenge: new Uint8Array(32) }))),
  { headers: { "Content-Type": "application/proto" } });
const denied = () => new Response(JSON.stringify({ code: "permission_denied", message: "login failed" }),
  { status: 403, headers: { "Content-Type": "application/json" } });
const success = () => new Response(new Uint8Array(toBinary(LoginResponseSchema,
  create(LoginResponseSchema, { token: "synthetic-token" }))),
  { headers: { "Content-Type": "application/proto" } });
function prepare() { vi.spyOn(signing, "signLogin").mockResolvedValue(new Uint8Array(64)); }
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it.each([false, true])("retries one denial with a fresh challenge; second denial=%s", async secondDenial => {
  prepare();
  const calls: string[] = [];
  let logins = 0;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const method = String(input).split("/").pop()!;
    calls.push(method);
    if (method === "GetChallenge") return challenge();
    return ++logins === 1 || secondDenial ? denied() : success();
  }));
  expect(await retainedIdentityDenied(account)).toBe(secondDenial);
  expect(calls).toEqual(["GetChallenge", "Login", "GetChallenge", "Login"]);
});

it("does not declare denial when the retry is offline", async () => {
  prepare();
  let logins = 0;
  vi.stubGlobal("fetch", vi.fn(async input => {
    if (String(input).endsWith("/GetChallenge")) return challenge();
    if (++logins === 1) return denied();
    throw new TypeError("offline");
  }));
  expect(await retainedIdentityDenied(account)).toBe(false);
  expect(logins).toBe(2);
});

it("does not retry a non-denial", async () => {
  prepare();
  const fetch = vi.fn(async () => { throw new TypeError("offline"); });
  vi.stubGlobal("fetch", fetch);
  expect(await retainedIdentityDenied(account)).toBe(false);
  expect(fetch).toHaveBeenCalledOnce();
});

it("cancels a hung challenge at five seconds and keeps the retained flow", async () => {
  prepare(); vi.useFakeTimers();
  let cancelled = false, finished = false;
  const fetch = vi.fn((_input, init) => new Promise<Response>((_resolve, reject) => {
    init.signal.addEventListener("abort", () => { cancelled = true; reject(init.signal.reason); }, { once: true });
  }));
  vi.stubGlobal("fetch", fetch);
  const result = retainedIdentityDenied(account).then(value => { finished = true; return value; });
  await vi.advanceTimersByTimeAsync(4_999);
  expect(finished).toBe(false);
  await vi.advanceTimersByTimeAsync(1);
  expect(await result).toBe(false);
  expect(cancelled).toBe(true);
  expect(fetch).toHaveBeenCalledOnce();
  expect(vi.getTimerCount()).toBe(0);
});

it("keeps the retry in the original five-second budget", async () => {
  prepare(); vi.useFakeTimers();
  let logins = 0, cancelled = false, finished = false;
  vi.stubGlobal("fetch", vi.fn(async (input, init) => {
    if (String(input).endsWith("/GetChallenge")) return challenge();
    if (++logins === 1) return new Promise<Response>(resolve => setTimeout(() => resolve(denied()), 3_000));
    return new Promise<Response>((_resolve, reject) => {
      init.signal.addEventListener("abort", () => { cancelled = true; reject(init.signal.reason); }, { once: true });
    });
  }));
  const result = retainedIdentityDenied(account).then(value => { finished = true; return value; });
  await vi.advanceTimersByTimeAsync(3_000);
  expect(logins).toBe(2);
  await vi.advanceTimersByTimeAsync(1_999);
  expect(finished).toBe(false);
  await vi.advanceTimersByTimeAsync(1);
  expect(await result).toBe(false);
  expect(cancelled).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
});
