// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import {
  DeviceActions,
  ManagementEntry,
  PairCodePage,
} from "./DeviceManagement";
// This UI test exercises pairing/SAS, not Vite's cold QR-library import.
// QR loading behavior is covered in lazy-qr.test.tsx.
const qr = vi.hoisted(() => ({ generate: vi.fn(async (_url: string) => "data:image/gif;base64,pairing-fixture") }));
vi.mock("../qr-code", () => ({ pairingQRCode: qr.generate }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  qr.generate.mockClear();
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(node));
}
const button = (label: string) =>
  [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
async function click(label: string) {
  await act(async () => button(label).click());
}
function demo(q: string) {
  const r = DemoRemote.create(new URLSearchParams(q), async () => {});
  r.start();
  return r;
}
it.each(["authority", "member", "machine"])(
  "offers add, remove and archive actions only to the authority: %s",
  async (role) => {
    const r = demo(`role=${role}&archive=1`);
    const go = vi.fn();
    await mount(<ManagementEntry remote={r} go={go} />);
    expect(!!button("添加设备")).toBe(role === "authority");
    await act(async () =>
      root.render(<DeviceActions remote={r} id="mini" machine go={go} />),
    );
    expect(!!button("重新加入并接续会话")).toBe(role === "authority");
    expect(!!button("删除这台电脑的记录…")).toBe(role === "authority");
  },
);
it("shows a QR code and waits for explicit candidate-first SAS confirmation", async () => {
  const r = demo("role=authority&pair=compare");
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click("生成配对码");
  expect(qr.generate).toHaveBeenCalledExactlyOnceWith("https://app.example.com/#/pair?code=12345678");
  expect(host.querySelector(".security-qr")?.getAttribute("src")).toBe("data:image/gif;base64,pairing-fixture");
  expect(button("允许加入").disabled).toBe(true);
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click("允许加入");
  expect(host.textContent).toContain("设备已加入");
});
it("leaves a failed cancellation retryable after the local SAS key is gone", async () => {
  const r = demo("role=authority");
  const start = r.startPairing;
  Object.defineProperty(r, "startPairing", {value:async (web:string) => { const pair = await start(web); pair.cancel = async () => {throw new Error("cancel acknowledgement lost");}; return pair; }});
  await mount(<PairCodePage remote={r} onBack={() => {}} />); await click("生成配对码"); await click("取消配对");
  expect(host.textContent).toContain("cancel acknowledgement lost"); expect(button("生成配对码")).toBeTruthy();
});

it.each([false, true])("keeps approved delivery after leaving the page (approval still pending: %s)", async (pending) => {
  const r = demo("role=authority&pair=compare");
  const pair = await r.startPairing("https://app.example.com");
  const cancel = vi.spyOn(pair, "cancel");
  let release!: () => void;
  const wait = new Promise<void>(resolve => { release = resolve; });
  pair.approve = async () => { if (pending) await wait; };
  vi.spyOn(r, "startPairing").mockResolvedValue(pair);
  vi.spyOn(r.authority, "pending").mockResolvedValue({} as never);
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click("生成配对码");
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  await click("允许加入");
  await act(async () => root.render(<p>设备列表</p>));
  expect(cancel).not.toHaveBeenCalled();
  await act(async () => release());
});
it("labels an unfinished candidate and offers explicit removal to pair again", async () => {
  const { DevicesPage } = await import("./Settings");
  const { DeviceKind } = await import("../gen/remoteagent/v1/relay_pb");
  const r = demo("role=authority");
  vi.spyOn(r, "loadDevices").mockResolvedValue();
  r.devices.set("candidate", { id: "candidate", kind: DeviceKind.CLIENT, createdMs: 0n, lastSeenMs: 0n, metaRev: 0n, pairingIncomplete: true });
  await mount(<DevicesPage remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.textContent).toContain("未完成的配对");
  expect(host.textContent).toContain("移除后重新配对");
  expect(host.querySelectorAll(".row.client")).toHaveLength(3);
  const remove = vi.spyOn(r, "removeDevice").mockResolvedValue();
  await click("移除未完成的配对…");
  await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === "移除")!.click());
  expect(remove).toHaveBeenCalledWith("candidate");
});

it("calls this device 本机 in its list and detail", async () => {
  const { DevicesPage, ClientPage } = await import("./Settings");
  const r = demo("role=authority");
  await mount(<DevicesPage remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".row.client .v")?.textContent).toBe("本机");
  await act(async () => root.render(<ClientPage remote={r} id={r.account.deviceId} onBack={() => {}} />));
  expect(host.querySelector(".navbar")?.textContent).toContain("本机");
  expect(host.textContent).not.toContain("当前客户端");
});
