import { create } from "@bufbuild/protobuf";
import { t as message } from "../i18n";
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
import Workspace from "../Workspace";
import * as securityClient from "../core/security-client";
import type { Remote } from "../remote";
import {
  SecurityBootstrapKind,
  SecurityExitAdviceSchema,
  SecurityPendingOpSummarySchema,
  SecurityWorkflowStatusSchema,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
const remotes: DemoRemote[] = [];
afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
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
  remotes.push(r);
  r.start();
  return r;
}

it("distinguishes verified removal from a refused login and uses stopped-transport reconciliation", async () => {
  const r = demo("role=member");
  const resume = vi.spyOn(r, "resumeSecurityOperation").mockResolvedValue();
  vi.spyOn(r, "getSecurityStatus").mockResolvedValue(create(SecurityWorkflowStatusSchema, { bootstrap: SecurityBootstrapKind.READY }));
  await mount(<RemovedScreen remote={r} />);
  expect(host.textContent).toContain("可能是设备被移除、服务重置或连接出了问题");
  r.removalVerified = true;
  await act(async () => root.render(<RemovedScreen remote={r} />));
  expect(host.textContent).toContain("已通过账户签名状态核实本机被移除");
  expect(host.textContent).not.toContain("可能是设备被移除、服务重置或连接出了问题");
  await click(message("settings.verify_and_continue_security_operation"));
  expect(resume).toHaveBeenCalledOnce();
  expect(host.textContent).toContain("原安全操作已核实完成");
});

it("offers an explicit exit for a confirmed handoff backup whose offer was never signed", async () => {
  const r = demo("role=member");
  let pending: SecurityWorkflowStatus["pendingOp"] = create(SecurityPendingOpSummarySchema, {
    kind: "offer",
    phase: "PREPARED",
    codeSaved: true,
    server: r.account.server,
  });
  vi.spyOn(r, "getSecurityStatus").mockImplementation(async () =>
    create(SecurityWorkflowStatusSchema, {
      bootstrap: SecurityBootstrapKind.READY,
      isMember: true,
      pendingOp: pending,
    }),
  );
  const discard = vi.spyOn(r, "discardPendingOp").mockImplementation(async () => { pending = undefined; });
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click(message("security.unfinished_security_operation"));
  await click(message("security.abandon_this_succession_preparation_security"));
  expect(discard).not.toHaveBeenCalled();
  expect(host.textContent).toContain("公钥可能已经发布");
  await click(message("new_session.android_abandon"));
  expect(discard).toHaveBeenCalledOnce();
  expect(button(message("security.abandon_this_succession_preparation_security"))).toBeUndefined();
});

it("ignores a security read paused by hiding the page and refreshes when it becomes visible", async () => {
  let hidden = false;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  const r = demo("role=authority");
  let reject!: (e: Error) => void;
  const read = vi.spyOn(r, "getSecurityStatus").mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await act(async () => {
    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await act(async () => reject(new Error("security: storage")));
  expect(host.textContent).not.toContain("security: storage");
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => {
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(read.mock.calls.length).toBeGreaterThan(1);
  expect(host.textContent).not.toContain("security: storage");
  expect(host.textContent).not.toContain(message("security.reading"));
});

it("clears a passive read error after a successful foreground refresh", async () => {
  let hidden = false;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  const r = demo("role=member");
  vi.spyOn(r, "getSecurityStatus").mockRejectedValueOnce(new Error("injected read failure"));
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  expect(host.textContent).toContain("injected read failure");
  await act(async () => { hidden = true; document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => { hidden = false; document.dispatchEvent(new Event("visibilitychange")); });
  expect(host.textContent).not.toContain("injected read failure");
});
it.each(["authority", "member", "machine"])(
  "shows management entries by verified role: %s",
  async (role) => {
    const r = demo(`role=${role}&empty=none`);
    const go = vi.fn();
    await mount(<Settings remote={r} go={go} />);
    expect(!!button(message("settings.add_first_computer"))).toBe(role === "authority");
    if (role !== "authority")
      expect(host.textContent).toContain(
        message("settings.empty_computers_member_body", { name: "Android 16 · Pixel 9" }),
      );
    if (role === "authority") {
      const add = button(message("settings.add_first_computer"));
      expect(add.classList.contains("pri-btn")).toBe(true);
      expect(add.querySelector("svg")).toBeTruthy();
      await click(message("settings.add_first_computer"));
      expect(go).toHaveBeenCalledWith("settings/add-device");
    }
    await act(async () =>
      root.render(<DevicesPage remote={r} go={go} onBack={() => {}} />),
    );
    expect(!!button(message("settings.clients_add_client"))).toBe(role === "authority");
    await act(async () => root.render(<Home remote={r} go={go} />));
    expect(!!button(message("home.add_device"))).toBe(role === "authority");
    if (role !== "authority")
      expect(host.textContent).toContain(
        message("home.add_via_manager", { name: "Android 16 · Pixel 9" }),
      );
  },
);
it.each(["authority", "member", "machine"])(
  "gates removal, archives and handoff: %s",
  async (role) => {
    const r = demo(`role=${role}&archive=1`);
    await mount(<DeviceActions remote={r} id="mini" machine go={() => {}} />);
    expect(!!button(message("security.rejoin_and_resume_sessions"))).toBe(role === "authority");
    expect(!!button(message("security.delete_this_computer_s_records"))).toBe(role === "authority");
    await act(async () =>
      root.render(<SecurityPage remote={r} onBack={() => {}} />),
    );
    expect(!!button(message("security.renew_now"))).toBe(role === "authority");
    expect(!!button(message("security.recovery_code"))).toBe(role === "authority");
    expect(!!button(message("security.take_over_management"))).toBe(role === "member");
  },
);
it.each(["authority", "member"])(
  "does not publish a code before backup confirmation or report a failed save as success: %s",
  async (role) => {
    const r = demo(`role=${role}`);
    await mount(<SecurityPage remote={r} onBack={() => {}} />);
    const publish = vi
      .spyOn(
        r,
        role === "authority" ? "confirmRecoveryRotation" : "publishHandoffOffer",
      )
      .mockRejectedValue(new Error("storage failed"));
    await click(role === "authority" ? message("security.recovery_code") : message("security.take_over_management"));
    await click(message("security.generate_new_recovery_code"));
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
  await click(message("settings.remove_computer_ellipsis"));
  expect(host.textContent).toContain("历史档案仍保留");
  await click(message("common.remove"));
  expect(remove).toHaveBeenCalledWith("mini");
  expect(deletion).not.toHaveBeenCalled();
  expect(host.textContent).toContain("storage failed");
  expect(button(message("settings.remove_computer_ellipsis"))).toBeTruthy();
});
it("shows pending recovery on home and settings, with the actual ready time", async () => {
  const r = demo("role=authority&recovery=pending");
  const go = vi.fn();
  await mount(<Home remote={r} go={go} />);
  expect(host.textContent).toContain(message("security.recovery_request_pending"));
  await click(message("dock.handle"));
  expect(go).toHaveBeenCalledWith("settings/security");
  await act(async () => root.render(<Settings remote={r} go={go} />));
  expect(host.textContent).toContain("可生效时间");
});
it("requires the candidate to confirm SAS before authority approval and waits for delivery", async () => {
  const r = demo("role=authority&pair=compare");
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click(message("security.generate_pairing_code"));
  // Clicking finishes before the lazy QR module necessarily loads. Flush React
  // work on each attempt and wait for the rendered image, not a fixed delay.
  await vi.waitFor(async () => {
    await act(async () => {});
    expect(host.querySelector('img[alt="添加设备的配对二维码"]')).toBeTruthy();
  });
  expect(host.textContent).toContain("🌲 🍋 🚲 🐳");
  expect(button(message("security.allow_to_join")).disabled).toBe(true);
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.allow_to_join"));
  expect(host.textContent).toContain(message("security.device_joined"));
});
it("gives authority-specific exit advice and unregisters push before erasing keys", async () => {
  const clearSecurity = vi.fn(async () => {});
  const setPushSubscription = vi.fn(async () => {});
  const unsubscribe = vi.fn(async () => true);
  const origSW = Object.getOwnPropertyDescriptor(navigator, "serviceWorker");
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: {
      getRegistration: vi.fn(async () => ({
        pushManager: {
          getSubscription: vi.fn(async () => ({ unsubscribe })),
        },
      })),
    },
  });
  try {
    vi.spyOn(securityClient, "createStandaloneSecurityClient").mockReturnValue({
      client: {
        bootstrap: vi.fn(async () => create(SecurityWorkflowStatusSchema, { bootstrap: SecurityBootstrapKind.READY, isAuthority: true })),
        checkExitAdvice: vi.fn(async () => create(SecurityExitAdviceSchema, {
          authority: true,
          unresolved: false,
          messageText: create(TextSchema, { key: "security.this_device_is_the_only_managing_client_transfer_management" }),
        })),
        clearSecurity,
      } as unknown as securityClient.SecurityClient,
      dispose: vi.fn(),
    });
    const fakeRemote = { setPushSubscription } as unknown as Remote;
    await mount(<LeaveAlert remote={fakeRemote} onCancel={() => {}} />);
    expect(host.textContent).toContain("本机是唯一管理端");
    expect(host.textContent).not.toContain("此浏览器");
    await click(message("security.exit"));
    expect(setPushSubscription).toHaveBeenCalledWith("", []);
    expect(unsubscribe).toHaveBeenCalledTimes(1);
    expect(clearSecurity).toHaveBeenCalledTimes(1);
  } finally {
    if (origSW) Object.defineProperty(navigator, "serviceWorker", origSW);
    else delete (navigator as { serviceWorker?: unknown }).serviceWorker;
  }
});
it("renews while an unconfirmed code remains in memory", async () => {
  const r = demo("role=authority");
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click(message("security.recovery_code"));
  await click(message("security.generate_new_recovery_code"));
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="返回"]')!.click());
  await click(message("security.renew_now"));
  expect(host.textContent).not.toContain(message("security.finish_the_current_security_operation_before_renewing"));
  expect(host.textContent).toContain(message("security.remote_control_renewed"));
});
it.each(["authority", "member", "machine"])("keeps the security overview ordered and hides absent pending operations: %s", async role => {
  const r = demo(`role=${role}&recovery=pending`);
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  const text = host.textContent!;
  expect(text.indexOf("有人正在用恢复码")).toBeLessThan(text.indexOf(message("security.current_managing_client")));
  expect(text.indexOf(message("security.current_managing_client"))).toBeLessThan(text.indexOf(message("security.remote_control_expiry")));
  expect(text).not.toContain(message("security.unfinished_security_operation"));
  expect(text).not.toContain("租约");
  expect(!!button(message("security.reject"))).toBe(role === "authority");
  expect(!!button(message("security.recovery_code"))).toBe(role === "authority");
  expect(!!button(message("security.take_over_management"))).toBe(role === "member");
});
it("lets a confirmed uncertain handoff offer be abandoned with a public-key warning", async () => {
  const r = demo("role=member");
  const pending = create(SecurityPendingOpSummarySchema, {
    kind: "offer",
    phase: "PREPARED",
    codeSaved: true,
    signed: true,
    server: r.account.server,
  });
  vi.spyOn(r, "getSecurityStatus").mockResolvedValue(
    create(SecurityWorkflowStatusSchema, {
      bootstrap: SecurityBootstrapKind.READY,
      isMember: true,
      pendingOp: pending,
    }),
  );
  const discard = vi.spyOn(r, "discardPendingOp").mockResolvedValue();
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click(message("security.unfinished_security_operation"));
  await click(message("security.abandon_this_succession_preparation_security"));
  expect(discard).not.toHaveBeenCalled();
  expect(host.textContent).toContain("公钥可能已经发布");
  await click(message("new_session.android_abandon"));
  expect(discard).toHaveBeenCalledOnce();
});
it("renders Core readable handoff candidates in the transfer management view", async () => {
  const r = demo("role=authority");
  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click(message("security.transfer_management"));
  expect(host.textContent).toContain("Android · Pixel 9");
});
it("switches Workspace reactively to RemovedScreen when remote.removed flips without sync.removed", async () => {
  const r = demo("role=member");
  await mount(<Workspace remote={r} />);
  expect(host.textContent).not.toContain("已通过账户签名状态核实本机被移除");
  r.removalVerified = true;
  await act(async () => {
    await r.discardPendingOp();
  });
  await vi.waitFor(async () => {
    await act(async () => {});
    expect(host.textContent).toContain("已通过账户签名状态核实本机被移除");
  });
});
