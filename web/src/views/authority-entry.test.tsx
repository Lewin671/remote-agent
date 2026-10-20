// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { AccountEntry } from "./AuthoritySetup";
import { AuthoritySetup, authorityStartup } from "../authority-setup";
vi.mock("../authority-setup", () => ({ AuthoritySetup: vi.fn(), authorityStartup: vi.fn(async () => ({ kind: "empty" })) }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => { if (root) act(() => root.unmount()); host?.remove(); vi.clearAllMocks(); location.hash = ""; localStorage.clear(); });
async function mount(setup = {} as AuthoritySetup) { host = document.createElement("div"); document.body.append(host); root = createRoot(host); await act(async () => root.render(<AccountEntry setup={setup} onDone={done} />)); }
const done = vi.fn();
const button = (label: string) => [...host.querySelectorAll("button")].find(b => b.textContent === label)!;
async function click(label: string) { await act(async () => button(label).click()); }
async function input(label: string, value: string) { const el = host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!; await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value); el.dispatchEvent(new Event("input", { bubbles: true })); }); }
it("has three unauthenticated entries and keeps pairing direction", async () => {
  await mount(); for (const label of ["新建账户", "加入已有账户", "用恢复码找回管理权"]) expect(button(label)).toBeTruthy();
  await click("加入已有账户"); expect(host.textContent).toContain("在管理端打开「设置 › 添加设备」，把它显示的 8 位配对码填在这里。");
});
it("requires offline backup before continuing creation and does not claim success on a failed save", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({ kind: "empty" });
  const setup = { prepareCreation: vi.fn(async () => { vi.mocked(authorityStartup).mockResolvedValue({ kind: "create", phase: "PREPARED", codeSaved: false }); return { code: "example-code", storagePersisted: false }; }), confirmCreationCodeSaved: vi.fn(async () => { throw new Error("storage failed"); }), continueCreation: vi.fn() } as unknown as AuthoritySetup;
  await mount(setup); await click("新建账户"); await input("服务器", "https://ra.example.com"); await click("生成恢复码");
  expect(host.textContent).toContain("浏览器没有承诺保留数据"); expect(button("继续创建").disabled).toBe(true);
  await click("继续创建"); expect(setup.confirmCreationCodeSaved).not.toHaveBeenCalled();
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click()); await click("继续创建");
  expect(host.textContent).toContain("storage failed"); expect(setup.continueCreation).not.toHaveBeenCalled(); expect(done).not.toHaveBeenCalled();
});
it("resumes a published creation with no replacement genesis or invented backup", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({ kind: "create", phase: "PUBLISHED", codeSaved: false });
  const setup = { continueCreation: vi.fn(async () => {}), adoptCreationWithoutCode: vi.fn(async () => ({})), prepareCreation: vi.fn() } as unknown as AuthoritySetup;
  await mount(setup); expect(host.textContent).toContain("恢复码不会再次显示"); await click("继续创建");
  expect(setup.prepareCreation).not.toHaveBeenCalled(); expect(setup.adoptCreationWithoutCode).toHaveBeenCalledOnce(); expect(done).toHaveBeenCalledOnce();
});
it("queries a recovery draft on restart and permits completing it without claiming a backup", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({ kind: "recovery", phase: "PUBLISHED", codeSaved: false, readyMs: 10n });
  const setup = { resumeRecovery: vi.fn(async () => ({ phase: 2, readyMs: 10n })), completeRecovery: vi.fn(async () => ({})), confirmRecoveryCodeSaved: vi.fn() } as unknown as AuthoritySetup;
  await mount(setup); await click("提交或查询原恢复"); await click("完成恢复");
  expect(setup.completeRecovery).toHaveBeenCalledOnce(); expect(setup.confirmRecoveryCodeSaved).not.toHaveBeenCalled(); expect(done).toHaveBeenCalledOnce();
});
