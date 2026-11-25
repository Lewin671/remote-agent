// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "../i18n";
import { DemoRemote } from "../dev/demo";
import {
  AccountSecurityActionKind,
  AccountSecuritySubpage,
  AddDeviceActionKind,
  AddDeviceStep,
  AppRootStage,
  ExitConfirmStep,
  ExitIntentKind,
  getCoreHost,
  PendingSecurityOpKind,
  type CoreHost,
} from "../core/host";
import {
  AccountSecurityViewModelSchema,
  AddDeviceViewModelSchema,
  AppRootViewModelSchema,
  ExitConfirmViewSchema,
  HandoffViewModelSchema,
  RemovedDeviceViewSchema,
} from "../gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { Settings, DevicesPage, RemovedScreen } from "./Settings";
import { DeviceActions, LeaveAlert, PairCodePage, SecurityPage } from "./Security";
import { Home } from "./Home";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let hostEl: HTMLDivElement;
let root: Root;
const remotes: DemoRemote[] = [];

afterEach(() => {
  if (root) act(() => root.unmount());
  hostEl?.remove();
  for (const r of remotes.splice(0)) r.dispose();
  vi.restoreAllMocks();
});

async function mount(node: React.ReactNode) {
  hostEl = document.createElement("div");
  document.body.append(hostEl);
  root = createRoot(hostEl);
  await act(async () => root.render(node));
}

const button = (label: string) =>
  [...hostEl.querySelectorAll("button")].find(
    (b) => b.textContent?.replace(/›/g, "").trim() === label,
  )!;

async function click(label: string) {
  await act(async () => button(label).click());
}

function demo(q: string) {
  const r = DemoRemote.create(new URLSearchParams(q), async () => {});
  remotes.push(r);
  r.start();
  return r;
}

it("renders RemovedScreen from AppRootViewModel and dispatches verifyPendingOp", async () => {
  const r = demo("role=member");
  const coreHost = getCoreHost(r);
  const verifySpy = vi.spyOn(coreHost, "appRootVerifyPendingOp").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "viewAppRoot").mockReturnValue(
    create(AppRootViewModelSchema, {
      stage: AppRootStage.REMOVED,
      server: r.account.server,
      removed: create(RemovedDeviceViewSchema, {
        removalVerified: true,
        server: r.account.server,
        hasPendingOp: true,
        canVerifyPending: true,
        verifyResultText: create(TextSchema, { key: "settings.the_original_security_operation_is_verified_complete_you_can" }),
      }),
    }),
  );
  await mount(<RemovedScreen host={coreHost} remote={r} />);
  expect(hostEl.textContent).toContain("已通过账户签名状态核实本机被移除");
  await click(message("settings.verify_and_continue_security_operation"));
  expect(verifySpy).toHaveBeenCalledOnce();
  expect(hostEl.textContent).toContain("原安全操作已核实完成");
});

it("renders pending handoff abandon confirmation from AccountSecurityViewModel", async () => {
  const r = demo("role=member");
  const coreHost = getCoreHost(r);
  vi.spyOn(coreHost, "accountSecurityOpen").mockImplementation(() => undefined);
  const actionSpy = vi.spyOn(coreHost, "accountSecurityAction").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "viewAccountSecurity").mockReturnValue(
    create(AccountSecurityViewModelSchema, {
      subpage: AccountSecuritySubpage.PENDING_OP,
      hasPendingOp: true,
      pendingKind: PendingSecurityOpKind.HANDOFF_OFFER,
      canContinuePending: true,
      canDiscardPending: true,
      discardRequiresConfirm: true,
    }),
  );
  vi.spyOn(coreHost, "viewHandoff").mockReturnValue(create(HandoffViewModelSchema, {}));

  await mount(<SecurityPage remote={r} onBack={() => {}} />);
  await click(message("security.abandon_this_succession_preparation_security"));
  expect(actionSpy).not.toHaveBeenCalled();
  expect(hostEl.textContent).toContain("公钥可能已经发布");
  await click(message("new_session.android_abandon"));
  expect(actionSpy).toHaveBeenCalledWith(AccountSecurityActionKind.DISCARD_PENDING, { flag: true });
});

it.each(["authority", "member", "machine"])(
  "shows management entries by verified role: %s",
  async (role) => {
    const r = demo(`role=${role}&empty=none`);
    const go = vi.fn();
    await mount(<Settings remote={r} go={go} />);
    expect(!!button(message("settings.add_first_computer"))).toBe(role === "authority");
    if (role !== "authority")
      expect(hostEl.textContent).toContain(
        message("settings.empty_computers_member_body", { name: "Android 16 · Pixel 9" }),
      );
    if (role === "authority") {
      const add = button(message("settings.add_first_computer"));
      expect(add.classList.contains("pri-btn")).toBe(true);
      expect(add.querySelector("svg")).toBeTruthy();
      await click(message("settings.add_first_computer"));
      expect(go).toHaveBeenCalledWith("settings/add-device");
    }
    await act(async () => root.render(<DevicesPage remote={r} go={go} onBack={() => {}} />));
    expect(!!button(message("settings.clients_add_client"))).toBe(role === "authority");
    await act(async () => root.render(<Home remote={r} go={go} />));
    expect(!!button(message("home.add_device"))).toBe(role === "authority");
    if (role !== "authority")
      expect(hostEl.textContent).toContain(
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
    await act(async () => root.render(<SecurityPage remote={r} onBack={() => {}} />));
    expect(!!button(message("security.renew_now"))).toBe(role === "authority");
    expect(!!button(message("security.recovery_code"))).toBe(role === "authority");
    expect(!!button(message("security.take_over_management"))).toBe(role === "member");
  },
);

it("separates device removal from archive deletion and retains the actions after an error", async () => {
  const r = demo("role=authority");
  const remove = vi.spyOn(r, "removeDevice").mockRejectedValue(new Error("storage failed"));
  const deletion = vi.spyOn(r, "deleteMachineRecords");
  await mount(<DeviceActions remote={r} id="mini" machine />);
  await click(message("settings.remove_computer_ellipsis"));
  expect(hostEl.textContent).toContain("历史档案仍保留");
  await click(message("common.remove"));
  expect(remove).toHaveBeenCalledWith("mini");
  expect(deletion).not.toHaveBeenCalled();
  expect(hostEl.textContent).toContain("storage failed");
  expect(button(message("settings.remove_computer_ellipsis"))).toBeTruthy();
});

it("shows pending recovery on home and settings, with the actual ready time", async () => {
  const r = demo("role=authority&recovery=pending");
  const go = vi.fn();
  await mount(<Home remote={r} go={go} />);
  expect(hostEl.textContent).toContain(message("security.recovery_request_pending"));
  await click(message("dock.handle"));
  expect(go).toHaveBeenCalledWith("settings/security");
  await act(async () => root.render(<Settings remote={r} go={go} />));
  expect(hostEl.textContent).toContain("可生效时间");
});

it("renders PairCodePage from AddDeviceViewModel and dispatches SAS confirm + approve", async () => {
  const r = demo("role=authority");
  const coreHost = getCoreHost(r);
  vi.spyOn(coreHost, "addDeviceOpen").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "addDeviceClose").mockImplementation(() => undefined);
  const addAction = vi.spyOn(coreHost, "addDeviceAction").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "viewAddDevice").mockReturnValue(
    create(AddDeviceViewModelSchema, {
      step: AddDeviceStep.COMPARE_SAS,
      formattedCode: "1234 5678",
      qrUrl: "https://remote-agent.top/#/pair?code=12345678&s=https%3A%2F%2Fra.example.com",
      sasEmoji: ["🌲", "🍋", "🚲", "🐳"],
      sasConfirmedChecked: false,
      canApprove: false,
      canCancel: true,
    }),
  );
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await vi.waitFor(async () => {
    await act(async () => {});
    expect(hostEl.querySelector('img[alt="添加设备的配对二维码"]')).toBeTruthy();
  });
  expect(hostEl.textContent).toContain("🌲 🍋 🚲 🐳");
  expect(button(message("security.allow_to_join")).disabled).toBe(true);
  await act(async () =>
    hostEl.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  expect(addAction).toHaveBeenCalledWith(AddDeviceActionKind.SET_SAS_CONFIRMED, { flag: true });
});

it("renders authority-specific exit warning in LeaveAlert and dispatches exitConfirm", async () => {
  const fakeHost = {
    subscribe: () => () => {},
    version: () => 1,
    viewExitConfirm: () =>
      create(ExitConfirmViewSchema, {
        intent: ExitIntentKind.LEAVE_ACCOUNT,
        step: ExitConfirmStep.AWAITING_CONFIRM,
        warningBody: create(TextSchema, { key: "security.this_device_is_the_only_managing_client_transfer_management" }),
      }),
    exitRequest: vi.fn(),
    exitConfirm: vi.fn(),
  } as unknown as CoreHost;

  await mount(<LeaveAlert host={fakeHost} onCancel={() => {}} />);
  expect(fakeHost.exitRequest).toHaveBeenCalledWith(ExitIntentKind.LEAVE_ACCOUNT, "");
  expect(hostEl.textContent).toContain("本机是唯一管理端");
  await click(message("security.exit"));
  expect(fakeHost.exitConfirm).toHaveBeenCalledWith(true);
});

it.each(["authority", "member", "machine"])(
  "keeps the security overview ordered and hides absent pending operations: %s",
  async (role) => {
    const r = demo(`role=${role}&recovery=pending`);
    await mount(<SecurityPage remote={r} onBack={() => {}} />);
    const txt = hostEl.textContent!;
    expect(txt.indexOf("有人正在用恢复码")).toBeLessThan(txt.indexOf(message("security.current_managing_client")));
    expect(txt.indexOf(message("security.current_managing_client"))).toBeLessThan(txt.indexOf(message("security.remote_control_expiry")));
    expect(txt).not.toContain(message("security.unfinished_security_operation"));
    expect(txt).not.toContain("租约");
    expect(!!button(message("security.reject"))).toBe(role === "authority");
    expect(!!button(message("security.recovery_code"))).toBe(role === "authority");
    expect(!!button(message("security.take_over_management"))).toBe(role === "member");
  },
);
