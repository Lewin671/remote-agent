// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { DeviceKind } from "./gen/remoteagent/v1/relay_pb";
import { initiate, type Initiation } from "./pairing";
import { type Remote } from "./remote";
import { AddDevicePage } from "./views/Pairing";

vi.mock("./pairing", async (original) => ({ ...await original<typeof import("./pairing")>(), initiate: vi.fn() }));

let root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  document.body.innerHTML = "<div id='test'></div>";
  root = createRoot(document.getElementById("test")!);
});
afterEach(async () => { await act(async () => root.unmount()); vi.clearAllMocks(); vi.unstubAllGlobals(); });

async function page(approve = vi.fn().mockRejectedValueOnce(new Error("不确定新设备收到没有")).mockResolvedValue(undefined)) {
  const operation: Initiation = { code: "12345678", link: "https://web.example.com/#/pair", joined: Promise.resolve({ emoji: ["🐊", "🥯", "🐩", "🦌"], kind: DeviceKind.CLIENT }), approve, cancel: vi.fn() };
  vi.mocked(initiate).mockResolvedValue(operation);
  const listeners = new Set<() => void>();
  let revision = 0;
  const remote = { conn: "online", account: { server: "https://relay.example.com" }, subscribe: (f: () => void) => { listeners.add(f); return () => listeners.delete(f); }, snapshot: () => revision };
  await act(async () => root.render(<AddDevicePage remote={remote as unknown as Remote} onBack={() => {}} />));
  return { operation, setOnline: async (online: boolean) => act(async () => { remote.conn = online ? "online" : "offline"; revision++; listeners.forEach((f) => f()); }) };
}

async function click(label: string) {
  const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
  expect(button, "button " + label).toBeDefined();
  await act(async () => button!.click());
}

it("the failure banner retries the approved operation without canceling or creating a new code", async () => {
  const { operation } = await page();
  await click("图案一致，允许");
  await click("重试");
  expect(operation.approve).toHaveBeenCalledTimes(2);
  expect(operation.cancel).not.toHaveBeenCalled();
  expect(initiate).toHaveBeenCalledTimes(1);
  expect(document.body.textContent).toContain("新设备已加入");
});

it("a connection interruption does not withdraw the user's already approved pairing", async () => {
  const { operation, setOnline } = await page();
  await click("图案一致，允许");
  await setOnline(false);
  await setOnline(true);
  expect(operation.cancel).not.toHaveBeenCalled();
  expect(initiate).toHaveBeenCalledTimes(1);
  await click("重试");
  expect(operation.approve).toHaveBeenCalledTimes(2);
});

it("a terminal failure can explicitly replace the code and withdraw the old attempt", async () => {
  const { operation } = await page(vi.fn().mockRejectedValue(new ConnectError("配对已过期", Code.NotFound)));
  await click("图案一致，允许");
  await click("换一个");
  expect(operation.cancel).toHaveBeenCalledTimes(1);
  expect(initiate).toHaveBeenCalledTimes(2);
});
