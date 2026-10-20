// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { DeviceActions, ManagementEntry, PairCodePage } from "./DeviceManagement";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => { if (root) act(() => root.unmount()); host?.remove(); });
async function mount(node: React.ReactNode) { host = document.createElement("div"); document.body.append(host); root = createRoot(host); await act(async () => root.render(node)); }
const button = (label: string) => [...host.querySelectorAll("button")].find(b => b.textContent === label)!;
async function click(label: string) { await act(async () => button(label).click()); }
function demo(q: string) { const r = DemoRemote.create(new URLSearchParams(q), async () => {}); r.start(); return r; }
it.each(["authority", "member", "machine"])("offers add, remove and archive actions only to the authority: %s", async role => {
  const r = demo(`role=${role}&archive=1`); const go = vi.fn(); await mount(<ManagementEntry remote={r} go={go} />);
  expect(!!button("添加设备")).toBe(role === "authority");
  await act(async () => root.render(<DeviceActions remote={r} id="mini" machine go={go} />));
  expect(!!button("重新加入并接续会话")).toBe(role === "authority");
  expect(!!button("删除这台电脑的记录")).toBe(role === "authority");
});
it("shows a QR code and waits for explicit candidate-first SAS confirmation", async () => {
  const r = demo("role=authority&pair=compare"); await mount(<PairCodePage remote={r} onBack={() => {}} />); await click("生成配对码");
  expect(host.querySelector(".security-qr")).toBeTruthy(); expect(button("允许加入").disabled).toBe(true);
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click()); await click("允许加入"); expect(host.textContent).toContain("设备已加入");
});
