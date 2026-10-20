import { afterEach, expect, it, vi } from "vitest";
import { create, toBinary } from "@bufbuild/protobuf";
import { Remote } from "./remote";
import { storedSecurity, rawSecurity } from "./test/stored-security-fixture";
import { loadLoginToken, saveLoginToken } from "./device-security-store";
import { GetChallengeResponseSchema, GetSecurityStatesResponseSchema, LoginResponseSchema } from "./gen/remoteagent/v1/relay_pb";
import type { StoredAccount } from "./storage";
import { unb64 } from "./device-security";
import vectors from "../../internal/deviceauth/testdata/domain-v3.json";
class ProbeRemote extends Remote {
  constructor(account: StoredAccount) { super(account, undefined); }
}
class Socket {
  static all: Socket[] = [];
  static OPEN = 1;
  readyState = 0;
  onclose?: (e: { code: number }) => void;
  close() { this.readyState = 3; }
  send() {}
  constructor() { Socket.all.push(this); }
}
const remotes: Remote[] = [];
afterEach(() => { for (const r of remotes.splice(0)) r.stop(); Socket.all = []; vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers(); });
async function setup() {
  const f = await storedSecurity();
  const account = { server: "https://relay.example.com", deviceId: f.memberID, devicePrivate: f.ed };
  vi.stubGlobal("WebSocket", Socket);
  const calls: string[] = [];
  let denied = false;
  let rejectSync = false;
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const method = String(input instanceof Request ? input.url : input).split("/").at(-1)!;
    calls.push(method);
    if (denied && method === "Login") return new Response(JSON.stringify({ code: "permission_denied" }), {
      status: 403, headers: { "Content-Type": "application/json" },
    });
    if (rejectSync && method === "GetSecurityStates") {
      rejectSync = false;
      return new Response(JSON.stringify({ code: "unauthenticated" }), {
        status: 401, headers: { "Content-Type": "application/json" },
      });
    }
    const bytes = method === "GetChallenge"
      ? toBinary(GetChallengeResponseSchema, create(GetChallengeResponseSchema, { challenge: new Uint8Array(32) }))
      : method === "Login"
        ? toBinary(LoginResponseSchema, create(LoginResponseSchema, { token: "fresh-token", expiresMs: BigInt(Date.now() + 10 * 86_400_000) }))
        : toBinary(GetSecurityStatesResponseSchema, create(GetSecurityStatesResponseSchema, {
            headEpoch: f.states.at(-1)!.core.epoch, lease: unb64(vectors.lease),
          }));
    return new Response(new Uint8Array(bytes), { headers: { "Content-Type": "application/proto" } });
  });
  const remote = () => { const r = new ProbeRemote(account); remotes.push(r); return r; };
  return { f, account, calls, remote, deny: () => { denied = true; }, rejectSync: () => { rejectSync = true; } };
}
it("uses zero login RPCs on reload, two on expiry and two after server 1008 rejection", async () => {
  const { f, account, calls, remote } = await setup();
  const first = remote();
  first.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  expect(calls).toEqual(["GetChallenge", "Login", "GetSecurityStates"]);
  first.stop();
  calls.length = 0;
  const reload = remote();
  reload.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(2));
  expect(calls).toEqual(["GetSecurityStates"]);
  Socket.all[1].onclose?.({ code: 1008 });
  await (reload as any).tokenWrites;
  expect(await loadLoginToken(account.server, f.memberID)).toBeUndefined();
  expect(reload.authHeader()).toBe("Bearer ");
  calls.length = 0;
  reload.reconnectNow();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(3));
  expect(calls).toEqual(["GetChallenge", "Login", "GetSecurityStates"]);
  reload.stop();
  await saveLoginToken(account.server, f.memberID, { token: "expiring", expiresMs: Date.now() + 1000 });
  calls.length = 0;
  const expired = remote();
  expired.start();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(4));
  expect(calls).toEqual(["GetChallenge", "Login", "GetSecurityStates"]);
});

it("also discards a token rejected by the state RPC before a stream can open", async () => {
  const { f, account, calls, remote, rejectSync } = await setup();
  await saveLoginToken(account.server, f.memberID, { token: "stale", expiresMs: Date.now() + 10 * 86_400_000 });
  rejectSync();
  const r = remote();
  r.start();
  await vi.waitFor(() => expect(r.conn).toBe("offline"));
  await (r as any).tokenWrites;
  expect(await loadLoginToken(account.server, f.memberID)).toBeUndefined();
  r.reconnectNow();
  await vi.waitFor(() => expect(Socket.all).toHaveLength(1));
  expect(calls).toEqual(["GetSecurityStates", "GetChallenge", "Login", "GetSecurityStates"]);
});

it("erases both token copies when a fresh login confirms removal", async () => {
  const { account, remote, deny } = await setup();
  deny();
  const r = remote();
  (r as any).token = "expired-memory-token";
  (r as any).tokenExpires = 0;
  r.start();
  await vi.waitFor(() => expect(r.removed).toBe(true));
  expect(r.authHeader()).toBe("Bearer ");
  expect(await rawSecurity("readonly", s => s.get("login-token"))).toBeUndefined();
  expect(Socket.all).toHaveLength(0);
  expect(account.deviceId).toBeTruthy();
});
