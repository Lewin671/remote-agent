// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { Settings, DevicesPage, RemovedScreen } from "./Settings";
import {
  DeviceActions,
  LeaveAlert,
  PairCodePage,
  SecurityPage,
} from "./Security";
import { Home } from "./Home";
import * as operations from "../security-operation";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  vi.restoreAllMocks();
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(node));
}
const button = (label: string) =>
  [...host.querySelectorAll("button")].find((b) => b.textContent?.replace(/›/g, "").trim() === label)!;
async function click(label: string) {
  await act(async () => button(label).click());
}
function demo(q: string) {
  const r = DemoRemote.create(new URLSearchParams(q), async () => {});
  r.start();
  return r;
}

it("distinguishes verified removal from a refused login and uses stopped-transport reconciliation", async () => {
  const r = demo("role=member");
  const resume = vi.spyOn(r, "resumeSecurityOperation").mockResolvedValue();
  vi.spyOn(r.authority, "pending").mockResolvedValue(undefined);
  await mount(<RemovedScreen remote={r} />);
  expect(host.textContent).toContain("尚未获得可信的设备撤销回执");
  r.removalVerified = true;
  await act(async () => root.render(<RemovedScreen remote={r} />));
  expect(host.textContent).toContain("已通过账户签名状态核实本机被移除");
  expect(host.textContent).not.toContain("尚未获得可信的设备撤销回执");
  await click("核实并继续安全操作");
  expect(resume).toHaveBeenCalledOnce();
  expect(host.textContent).toContain("原安全操作已核实完成");
});

it("offers an explicit exit for a confirmed handoff backup whose offer was never signed", async () => {
  const r = demo("role=member");
  const op = { ...operations.operation("offer", r.account.server, r.account.deviceId, new Uint8Array(32), {}), codeSaved: true };
  vi.spyOn(r.authority, "pending").mockImplementation(async () => pending);
  let pending: typeof op | undefined = op;
  const discard = vi.spyOn(r.authority, "discardUnpublishedCode").mockImplementation(async () => { pending = undefined; });
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click("未完成的安全操作");
  await click("放弃未发布的恢复码");
  expect(discard).toHaveBeenCalledOnce();
  expect(button("放弃未发布的恢复码")).toBeUndefined();
});
it.each(["authority", "member", "machine"])(
  "shows management entries by verified role: %s",
  async (role) => {
    const r = demo(`role=${role}&empty=none`);
    const go = vi.fn();
    await mount(<Settings remote={r} go={go} />);
    expect(!!button("添加设备")).toBe(role === "authority");
    if (role !== "authority")
      expect(host.textContent).toContain(
        "要添加电脑或客户端，在管理端（Android · Pixel 9）上打开「设置 › 添加设备」。",
      );
    if (role === "authority") {
      const add = button("添加设备");
      const group = add.closest(".grp")!;
      expect(group.lastElementChild).toBe(add);
      expect(add.classList.contains("add")).toBe(true);
      expect(add.querySelector("svg")).toBeTruthy();
      await click("添加设备");
      expect(go).toHaveBeenCalledWith("settings/add-device");
    }
    await act(async () =>
      root.render(<DevicesPage remote={r} go={go} onBack={() => {}} />),
    );
    expect(!!button("添加设备")).toBe(role === "authority");
    await act(async () => root.render(<Home remote={r} go={go} />));
    expect(!!button("添加设备")).toBe(role === "authority");
    if (role !== "authority")
      expect(host.textContent).toContain(
        "还没有电脑。在管理端（Android · Pixel 9）上打开「设置 › 添加设备」，把电脑加进来。",
      );
  },
);
it.each(["authority", "member", "machine"])(
  "gates removal, archives and handoff: %s",
  async (role) => {
    const r = demo(`role=${role}&archive=1`);
    await mount(<DeviceActions remote={r} id="mini" machine go={() => {}} />);
    expect(!!button("重新加入并接续会话")).toBe(role === "authority");
    expect(!!button("删除这台电脑的记录")).toBe(role === "authority");
    await act(async () =>
      root.render(<SecurityPage remote={r} onBack={() => {}} />),
    );
    expect(!!button("现在续期")).toBe(role === "authority");
    expect(!!button("恢复码")).toBe(role === "authority");
    expect(!!button("接管管理权")).toBe(role === "member");
  },
);
it.each(["authority", "member"])(
  "does not publish a code before backup confirmation or report a failed save as success: %s",
  async (role) => {
    const r = demo(`role=${role}`);
    await mount(<SecurityPage remote={r} onBack={() => {}} />);
    const publish = vi
      .spyOn(
        r.authority,
        role === "authority" ? "publishRecovery" : "offerHandoff",
      )
      .mockRejectedValue(new Error("storage failed"));
    await click(role === "authority" ? "恢复码" : "接管管理权");
    await click("生成新的恢复码");
    const label =
      role === "authority" ? "我已保存，发布换码" : "我已保存，通知管理端";
    expect(button(label).disabled).toBe(true);
    await click(label);
    expect(publish).not.toHaveBeenCalled();
    await act(async () =>
      host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
    );
    await click(label);
    expect(host.textContent).toContain("storage failed");
    expect(host.querySelector(".recovery-code")).toBeTruthy();
    expect(host.textContent).not.toContain("原恢复码和待处理恢复申请已作废");
    expect(host.textContent).not.toContain("等待当前管理端批准交接。");
  },
);
it("separates device removal from archive deletion and retains the actions after an error", async () => {
  const r = demo("role=authority");
  const remove = vi
    .spyOn(r, "removeDevice")
    .mockRejectedValue(new Error("storage failed"));
  const deletion = vi.spyOn(r, "deleteMachineRecords");
  await mount(<DeviceActions remote={r} id="mini" machine />);
  await click("移除设备");
  expect(host.textContent).toContain("历史档案仍保留");
  await click("移除");
  expect(remove).toHaveBeenCalledWith("mini");
  expect(deletion).not.toHaveBeenCalled();
  expect(host.textContent).toContain("storage failed");
  expect(button("移除设备")).toBeTruthy();
});
it("shows pending recovery on home and settings, with the actual ready time", async () => {
  const r = demo("role=authority&recovery=pending");
  const go = vi.fn();
  await mount(<Home remote={r} go={go} />);
  expect(host.textContent).toContain("恢复申请待处理");
  await click("处理");
  expect(go).toHaveBeenCalledWith("settings/security");
  await act(async () => root.render(<Settings remote={r} go={go} />));
  expect(host.textContent).toContain("可生效时间");
});
it("requires the candidate to confirm SAS before authority approval and waits for delivery", async () => {
  const r = demo("role=authority&pair=compare");
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click("生成配对码");
  expect(host.querySelector('img[alt="添加设备的配对二维码"]')).toBeTruthy();
  expect(host.textContent).toContain("🌲 🍋 🚲 🐳");
  expect(button("允许加入").disabled).toBe(true);
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click("允许加入");
  expect(host.textContent).toContain("设备已加入");
});
it("gives authority-specific exit advice before erasing keys", async () => {
  vi.spyOn(operations, "authorityExitAdvice").mockResolvedValue({
    authority: true,
    unresolved: false,
    message:
      "此浏览器是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。",
  });
  await mount(<LeaveAlert onCancel={() => {}} />);
  expect(host.textContent).toContain("建议先交接");
});
it("does not report renewal when an unfinished code draft prevents it", async () => {
  const r = demo("role=authority");
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click("恢复码");
  await click("生成新的恢复码");
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="返回"]')!.click());
  await click("现在续期");
  expect(host.textContent).toContain("请先完成当前安全操作，再续期");
  expect(host.textContent).not.toContain("远程控制已续期");
});
it.each(["authority", "member", "machine"])("keeps the security overview ordered and hides absent pending operations: %s", async role => {
  const r = demo(`role=${role}&recovery=pending`);
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  const text = host.textContent!;
  expect(text.indexOf("有人正在用恢复码")).toBeLessThan(text.indexOf("当前管理端"));
  expect(text.indexOf("当前管理端")).toBeLessThan(text.indexOf("远程控制有效期"));
  expect(text).not.toContain("未完成的安全操作");
  expect(text).not.toContain("租约");
  expect(!!button("否决")).toBe(role === "authority");
  expect(!!button("恢复码")).toBe(role === "authority");
  expect(!!button("接管管理权")).toBe(role === "member");
});
