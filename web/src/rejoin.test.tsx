// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { errorText } from "./remote";
import { joinBanner } from "./views/Pairing";
import { RejoinButton } from "./Rejoin";
import * as operations from "./security-operation";
import * as storage from "./storage";
import { App } from "./App";
import * as pairing from "./pairing";
import * as bootstrap from "./authority-setup";
import * as security from "./device-security-store";
import { Remote } from "./remote";
import * as remoteModule from "./remote";
import { retainedIdentityDenied } from "./pairing";
import { create, toBinary } from "@bufbuild/protobuf";
import { GetChallengeResponseSchema } from "./gen/remoteagent/v1/relay_pb";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => { if (root) act(() => root.unmount()); host?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("keeps the login denial distinct from a bad pairing code", () => {
  const error = new ConnectError("login failed", Code.PermissionDenied);
  const text = "服务器不认识这台设备（可能已被移除，或服务已重建）";
  expect(errorText(error)).toBe(text);
  expect(joinBanner(error).text).toBe(text);
  expect(errorText(new ConnectError("operation denied", Code.PermissionDenied))).toBe("operation denied");
});

it("checks the signing identity before code entry and distinguishes denied from offline", async () => {
  const key = await crypto.subtle.generateKey("Ed25519", false, ["sign", "verify"]);
  const account = { server: "https://relay.example.com", deviceId: "synthetic-device", devicePrivate: key.privateKey } as storage.StoredAccount;
  for (const denied of [true, false]) {
    let calls = 0;
    vi.stubGlobal("fetch", vi.fn(async (input) => {
      calls++;
      if (String(input).endsWith("/GetChallenge")) return new Response(new Uint8Array(toBinary(GetChallengeResponseSchema,
        create(GetChallengeResponseSchema, { challenge: new Uint8Array(32) }))), { headers: { "Content-Type": "application/proto" } });
      if (!denied) throw new TypeError("offline");
      return new Response(JSON.stringify({ code: "permission_denied", message: "login failed" }),
        { status: 403, headers: { "Content-Type": "application/json" } });
    }));
    expect(await retainedIdentityDenied(account)).toBe(denied);
    expect(calls).toBe(denied ? 4 : 2);
  }
});

it.each([false, true])("rejoin clears data and goes directly to pairing; pending=%s", async pending => {
  vi.spyOn(operations, "authorityExitAdvice").mockResolvedValue({ authority: false, unresolved: pending, message: "原操作尚未完成。" });
  vi.spyOn(storage, "loadRegistration").mockResolvedValue(undefined);
  const erase = vi.spyOn(storage, "forgetAccount").mockResolvedValue();
  const done = vi.fn();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<RejoinButton server="https://relay.example.com" onDone={done}>用配对码重新加入</RejoinButton>));
  await act(async () => host.querySelector("button")!.click());
  if (pending) {
    expect(erase).not.toHaveBeenCalled();
    expect(host.textContent).toContain("电脑上的会话不受影响");
    const confirm = [...host.querySelectorAll("button")].find(b => b.textContent === "重新加入")!;
    await act(async () => confirm.click());
  }
  expect(erase).toHaveBeenCalledOnce();
  expect(done).toHaveBeenCalledOnce();
  expect(location.hash).toBe("#/pair?");
  expect(localStorage.getItem("ra-server")).toBe("https://relay.example.com");
});

it.each([false, true])("startup decides before code entry and discards retained state on rejoin; denied=%s", async denied => {
  location.hash = "";
  let account: storage.StoredAccount | undefined = {
    server: "https://relay.example.com", deviceId: "synthetic-device",
  } as storage.StoredAccount;
  vi.spyOn(remoteModule, "resumeRegistration").mockResolvedValue();
  vi.spyOn(bootstrap, "authorityStartup").mockResolvedValue({ kind: "empty" });
  vi.spyOn(storage, "loadAccount").mockImplementation(async () => account);
  vi.spyOn(storage, "loadRegistration").mockResolvedValue(undefined);
  vi.spyOn(storage, "watchAccountLogout").mockImplementation(() => () => {});
  vi.spyOn(security, "securitySetupStatus").mockResolvedValue("missing");
  const probe = vi.spyOn(pairing, "retainedIdentityDenied").mockResolvedValue(denied);
  vi.spyOn(Remote, "load").mockResolvedValue(undefined);
  vi.spyOn(operations, "authorityExitAdvice").mockResolvedValue({ authority: false, unresolved: false, message: "" });
  vi.spyOn(storage, "forgetAccount").mockImplementation(async () => { account = undefined; });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root.render(<App />));
  await vi.waitFor(async () => { await act(async () => {}); expect(host.textContent).toContain(denied ? "服务器不认识这台设备" : "一次性安全设置"); });
  expect(probe).toHaveBeenCalledOnce();
  if (!denied) { expect(host.textContent).not.toContain("用配对码重新加入"); return; }
  expect(host.querySelector('input[aria-label="配对码"]')).toBeNull();
  await act(async () => host.querySelector("button")!.click());
  await vi.waitFor(async () => { await act(async () => {}); expect(host.querySelector('input[aria-label="配对码"]')).not.toBeNull(); });
  expect(host.textContent).not.toContain("一次性安全设置");
  expect(host.querySelector<HTMLInputElement>('input[aria-label="服务器"]')?.value).toBe("https://relay.example.com");
  expect(account).toBeUndefined();
});
