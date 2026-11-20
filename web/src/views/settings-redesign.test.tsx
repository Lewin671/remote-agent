// @vitest-environment happy-dom
// Comprehensive tests for Settings & Computer Page Redesign, Secondary Pages, Cross-Page Jumps & CSS invariants.

import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { ApprovalMode, Agent } from "../gen/remoteagent/v1/event_pb";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import {
  MachineStatusDot,
  QuotaCliSectionSchema,
  QuotaSectionState,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import {
  AttachmentStorageViewSchema,
  ClientRowViewSchema,
  CliVersionViewSchema,
  ConnectionGroupStage,
  ConnectionGroupViewSchema,
  DevicesViewModelSchema,
  EffortChoiceViewSchema,
  MachineAgentDefaultsViewSchema,
  MachineSettingsViewModelSchema,
  ModeChoiceViewSchema,
  QuotaSizeChoiceViewSchema,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { localeStore, t as message } from "../i18n";
import { patchSyncState } from "../test/remote-fixture";
import { ManagePage } from "./Projects";
import {
  AttachmentStorageScreenView,
  ClientsPage,
  ClientsScreenView,
  LicensesPage,
  MachinePage,
  MachineSettingsScreenView,
  NotificationsPage,
  NotificationsScreenView,
  Settings,
} from "./Settings";
import {
  NotificationPermissionState,
  PushRegistrationState,
} from "../core/host";
import { NotificationsViewSchema } from "../gen/remoteagent/core/v1/settings_view_pb";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLDivElement;
const remotes: DemoRemote[] = [];

beforeEach(() => {
  localStorage.clear();
  localeStore.overrideForTesting("zh-CN");
  vi.stubGlobal("RTCPeerConnection", class {});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  for (const r of remotes.splice(0)) r.dispose();
  localeStore.overrideForTesting(undefined);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

function createDemo(q = ""): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(q), async () => undefined);
  remotes.push(r);
  r.start();
  return r;
}

async function flushUi(rounds = 8) {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

async function mountAsync(node: React.ReactElement) {
  if (root) {
    act(() => root?.unmount());
    host?.remove();
  }
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
  await flushUi();
}

describe("Settings home redesign (4 groups + separated danger card)", () => {
  it("renders the 4 redesigned groups, removes push troubleshooting block, and navigates sub-pages cleanly", async () => {
    const r = createDemo("role=authority");
    const go = vi.fn();
    await mountAsync(<Settings remote={r} go={go} />);

    // Exact 4 section headers in order
    const labels = [...host.querySelectorAll(".label > span:first-child")].map((s) => s.textContent);
    expect(labels).toEqual([
      message("settings.section_computers"),
      message("settings.section_this_device"),
      message("settings.section_account_and_devices"),
      message("settings.about"),
    ]);

    // Crowded machine count shown right-aligned when >2 computers
    expect(host.querySelector(".label .lbl-meta")?.textContent).toBe(
      message("settings.machine_count", { count: 4 }),
    );

    // No duplicate multi-bar cards or dual 全部设备/全部客户端 rows or push banner copy on Settings home
    expect(host.querySelector(".mach-card")).toBeNull();
    expect(host.textContent).not.toContain("全部设备");
    expect(host.textContent).not.toContain("全部客户端");
    expect(host.textContent).not.toContain(message("settings.session_alerts_are_off_you_will_not_be_notified"));

    // Last danger group separated cleanly with red exit action
    const groups = [...host.querySelectorAll(".grp")];
    const lastDanger = groups[groups.length - 1]!.querySelector("button.row.danger")!;
    expect(lastDanger.textContent).toContain(message("settings.exit_and_delete_local_keys"));

    // Group 1 computer rows have 20px Laptop icon & subtitleParts with selective orange highlight
    const macRow = [...host.querySelectorAll<HTMLButtonElement>(".grp .row.has-ic")].find(
      (b) => b.querySelector(".row-name b")?.textContent === "MacBook",
    )!;
    expect(macRow.querySelector(".lead svg")).toBeTruthy();
    expect(macRow.querySelector(".row-name small")).toBeTruthy();
    // Same group ends with blue ＋ 添加设备 for authority
    const addDeviceBtn = groups[0]!.querySelector<HTMLButtonElement>("button.row.has-ic.act")!;
    expect(addDeviceBtn.textContent).toBe(message("settings.add_device_item"));

    // Navigate to Notifications, Clients, Security, Licenses, and Server & Connection sheet
    const findRow = (txt: string) =>
      [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
        b.querySelector(".row-name")?.textContent?.startsWith(txt),
      )!;

    await act(async () => findRow(message("settings.notifications_title")).click());
    expect(go).toHaveBeenCalledWith("settings/notifications");

    await act(async () => findRow(message("settings.clients_title")).click());
    expect(go).toHaveBeenCalledWith("settings/clients");

    await act(async () => findRow(message("security.account_security")).click());
    expect(go).toHaveBeenCalledWith("settings/security");

    await act(async () => findRow(message("settings.open_source_licenses")).click());
    expect(go).toHaveBeenCalledWith("settings/licenses");

    await act(async () => findRow(message("settings.server_and_connection")).click());
    await flushUi();
    expect(document.querySelector(".sheet .sheet-head b")?.textContent).toBe(
      message("settings.server_and_connection"),
    );

    // Inline 3-segment appearance control works
    const radios = [...host.querySelectorAll<HTMLButtonElement>('[role="radiogroup"] [role="radio"]')];
    expect(radios.map((x) => x.textContent)).toEqual([
      message("settings.theme_auto"),
      message("settings.light"),
      message("settings.dark"),
    ]);
  });

  it("renders empty computer state card with authority CTA vs member guidance", async () => {
    const rAuth = createDemo("role=authority&empty=none");
    const go = vi.fn();
    await mountAsync(<Settings remote={rAuth} go={go} />);
    const card = host.querySelector(".set-empty-card")!;
    expect(card).toBeTruthy();
    expect(card.textContent).toContain(message("settings.empty_computers_title"));
    expect(card.textContent).toContain(message("settings.empty_computers_authority_body"));
    const cta = card.querySelector<HTMLButtonElement>(".pri-btn")!;
    expect(cta.textContent).toBe(message("settings.add_first_computer"));
    await act(async () => cta.click());
    expect(go).toHaveBeenCalledWith("settings/add-device");

    const rMem = createDemo("role=member&empty=none");
    await mountAsync(<Settings remote={rMem} go={go} />);
    const memCard = host.querySelector(".set-empty-card")!;
    expect(memCard.querySelector(".pri-btn")).toBeNull();
    expect(memCard.textContent).toContain(
      message("settings.empty_computers_member_body", { name: "Android 16 · Pixel 9" }),
    );
  });
});

describe("Notifications secondary page (settings/notifications)", () => {
  it("houses banners, 3 notification switches, and security footer cleanly", async () => {
    const onSetPref = vi.fn();
    const onEnablePush = vi.fn();
    const notif = create(NotificationsViewSchema, {
      permission: NotificationPermissionState.UNSPECIFIED,
      pushState: PushRegistrationState.UNSPECIFIED,
      notifyAttention: false,
      notifyTurnEnded: false,
      notifyScheduled: false,
      errorDetail: "network error",
    });

    await mountAsync(
      <NotificationsScreenView
        notif={notif}
        onBack={() => undefined}
        onSetNotificationPref={onSetPref}
        onEnablePushNotifications={onEnablePush}
      />,
    );

    expect(host.querySelector(".navbar")?.textContent).toContain(message("settings.notifications_title"));
    expect(host.querySelector(".navbar")?.textContent).toContain(message("settings.notifications_subtitle"));
    expect(host.textContent).toContain(message("settings.session_alerts_are_off_you_will_not_be_notified"));
    expect(host.textContent).toContain("network error");
    expect(host.querySelector(".gf")?.textContent).toBe(message("settings.notifications_security_footer"));

    const switches = [...host.querySelectorAll<HTMLButtonElement>('[role="switch"]')];
    expect(switches).toHaveLength(3);
    await act(async () => switches[0].click());
    expect(onSetPref).toHaveBeenCalledWith({ notifyAttention: true });
    await act(async () => switches[2].click());
    expect(onSetPref).toHaveBeenCalledWith({ notifyScheduled: true });

    const r = createDemo();
    await mountAsync(<NotificationsPage remote={r} onBack={() => undefined} />);
    expect(host.querySelectorAll('[role="switch"]')).toHaveLength(3);
  });
});

describe("Pure Clients page (settings/clients)", () => {
  it("lists paired clients, current client, unfinished pairings, and omits computers completely", async () => {
    const r = createDemo("role=authority");
    const nextDevices = new Map(r.devices);
    nextDevices.set("cand-1", {
      id: "cand-1",
      kind: DeviceKind.CLIENT,
      createdMs: 0n,
      lastSeenMs: 0n,
      metaRev: 0n,
      pairingIncomplete: true,
    });
    patchSyncState(r, { devices: nextDevices });
    const go = vi.fn();

    await mountAsync(<ClientsPage remote={r} go={go} onBack={() => undefined} />);
    expect(host.querySelector(".navbar .title b")?.textContent).toBe(message("settings.clients_title"));
    expect(host.textContent).toContain(message("settings.clients_pairing_incomplete_badge"));
    expect(host.textContent).toContain(message("settings.clients_pairing_incomplete_desc"));
    expect(host.textContent).toContain(message("settings.clients_add_client"));
    expect(host.textContent).toContain(message("settings.clients_footer_redesign"));
    expect(host.textContent).not.toContain("MacBook");
    expect(host.textContent).not.toContain("Mac mini");

    const addClientBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(message("settings.clients_add_client")),
    )!;
    await act(async () => addClientBtn.click());
    expect(go).toHaveBeenCalledWith("settings/add-device");
  });
});

describe("Open-source Licenses page (settings/licenses)", () => {
  it("fetches bundled font/icon notices and handles unavailable THIRD_PARTY_NOTICES in dev gracefully", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/fonts/OFL-Inter.txt") {
        return new Response("SIL OPEN FONT LICENSE Inter", {
          status: 200,
          headers: { "content-type": "text/plain" },
        });
      }
      // Simulate Vite SPA HTML fallback for unbuilt THIRD_PARTY_NOTICES.txt in dev
      return new Response("<!doctype html><html><body>SPA</body></html>", {
        status: 200,
        headers: { "content-type": "text/html" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    await mountAsync(<LicensesPage onBack={() => undefined} />);
    expect(host.textContent).toContain(message("settings.licenses_web_fonts_title"));
    expect(host.textContent).toContain(message("settings.licenses_web_icons_title"));
    expect(host.textContent).toContain(message("settings.licenses_web_packages_title"));

    const row = (name: string) =>
      [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
        b.querySelector("b")?.textContent === name,
      )!;

    await act(async () => row("Inter").click());
    await flushUi();
    expect(document.querySelector(".sheet .license-pre")?.textContent).toContain("SIL OPEN FONT LICENSE Inter");
    const done = [...document.querySelectorAll<HTMLButtonElement>(".sheet .sheet-head button")].find(
      (b) => b.textContent === message("common.done"),
    )!;
    await act(async () => done.click());

    await act(async () => row("THIRD_PARTY_NOTICES.txt").click());
    await flushUi();
    expect(document.querySelector(".sheet")?.textContent).toContain(
      message("settings.could_not_download_retry_later"),
    );
  });
});

describe("Computer Page 4 core modules + Archive Fix + Storage Page + Filtered Projects", () => {
  it("renders removed computer archive directly on MachinePage and MachineSettingsScreenView (!vm.member bug fix)", async () => {
    const r = createDemo("role=authority&archive=1");
    await mountAsync(<MachinePage remote={r} id="mini" go={() => undefined} onBack={() => undefined} />);
    expect(host.textContent).toContain("Mac mini");
    expect(host.textContent).toContain(message("security.rejoin_and_resume_sessions"));
    expect(host.textContent).toContain(message("settings.delete_computer_records_ellipsis"));
    expect(host.textContent).not.toContain(message("settings.this_device_was_removed_from_the_account"));

    // Verify !vm.member does NOT fall back to RemovedScreen when removedSlot is passed
    const removedVm = create(MachineSettingsViewModelSchema, {
      found: true,
      machineId: "mini",
      name: "Mac mini",
      member: false,
      online: false,
      headerDot: MachineStatusDot.GRAY_STATIC,
      headerSubtitle: create(TextSchema, { literal: "离线 · 已从账户移除" }),
      removedBanner: create(TextSchema, { literal: "这台电脑已从账户成员名单移除，权限已吊销。" }),
      showStatusEntry: true,
      statusCardDot: MachineStatusDot.GRAY_STATIC,
      statusCardBadge: create(TextSchema, { literal: "已移除" }),
      shortId: "…8840cc19",
    });
    await mountAsync(
      <MachineSettingsScreenView
        id="mini"
        vm={removedVm}
        go={() => undefined}
        onBack={() => undefined}
        onRetrySettings={() => undefined}
        onUpdateAgentDefaults={() => undefined}
        onUpdateDaemonSettings={() => undefined}
        removedSlot={<div data-removed-bug="true">WRONG_REMOVED_SLOT</div>}
      />,
    );
    expect(host.querySelector('[data-removed-bug]')).toBeNull();
    expect(host.textContent).toContain("已移除");
    expect(host.textContent).toContain("这台电脑已从账户成员名单移除");
  });

  it("renders 4 modules with selective quota warn tint, formatted 7d/30d/cached usage, FULL_ACCESS red nowrap, and CLI sheet pickers", async () => {
    const onToggle = vi.fn();
    const onDefaults = vi.fn();
    const go = vi.fn();
    const fullModeChoice = create(ModeChoiceViewSchema, {
      mode: ApprovalMode.FULL_ACCESS,
      label: create(TextSchema, { literal: "完全放行" }),
      selected: true,
    });
    const askModeChoice = create(ModeChoiceViewSchema, {
      mode: ApprovalMode.ASK,
      label: create(TextSchema, { literal: "询问" }),
      selected: false,
    });
    const vm = create(MachineSettingsViewModelSchema, {
      found: true,
      machineId: "m-test",
      name: "Workstation",
      member: true,
      online: true,
      editable: true,
      headerDot: MachineStatusDot.GREEN_LIVE,
      headerSubtitle: create(TextSchema, { literal: "在线 · daemon 0.94.0" }),
      showStatusEntry: true,
      statusCardDot: MachineStatusDot.ORANGE_STATIC,
      statusCardBadge: create(TextSchema, { literal: "磁盘紧张" }),
      statusCardWarn: true,
      quotaRows: [
        create(QuotaCliSectionSchema, {
          agent: Agent.CODEX,
          agentName: "Codex",
          state: QuotaSectionState.OK,
          summaryPrimaryText: create(TextSchema, { literal: "剩 18%" }),
          summaryPrimaryWarn: true,
          summaryWindowText: create(TextSchema, { literal: "5 小时" }),
        }),
      ],
      showUsageEntry: true,
      usageTotalTokens: 340_000_000n,
      usageFallbackThirtyDays: true,
      showCliSwitches: true,
      cliSwitchesEditable: true,
      cliVersions: [
        create(CliVersionViewSchema, {
          agent: Agent.CODEX,
          name: "Codex",
          version: "0.91.0",
          verified: false,
          enabled: true,
          lockedLastEnabled: false,
          saving: true,
          defaultModelLabel: create(TextSchema, { literal: "gpt-6-astra" }),
          defaultModeLabel: create(TextSchema, { literal: "完全放行" }),
          defaultModeDanger: true,
          defaults: create(MachineAgentDefaultsViewSchema, {
            agent: Agent.CODEX,
            agentName: "Codex",
            selectedModelLabel: create(TextSchema, { literal: "gpt-6-astra" }),
            selectedMode: ApprovalMode.FULL_ACCESS,
            modeChoices: [askModeChoice, fullModeChoice],
          }),
        }),
        create(CliVersionViewSchema, {
          agent: Agent.CLAUDE,
          name: "Claude Code",
          version: "2.1.0",
          verified: true,
          enabled: false,
          disabledLabel: create(TextSchema, { literal: "已停用" }),
        }),
      ],
      connection: create(ConnectionGroupViewSchema, {
        stage: ConnectionGroupStage.DIRECT,
        toggleChecked: true,
        toggleEnabled: true,
        rowDot: MachineStatusDot.GREEN_LIVE,
        rowValueText: create(TextSchema, { literal: "正在使用 · 14 ms" }),
      }),
      attachmentStorage: create(AttachmentStorageViewSchema, {
        loaded: true,
        summaryText: create(TextSchema, { literal: "480 MB / 512 MB · 预留启用" }),
        summaryWarn: true,
        limitLabel: create(TextSchema, { literal: "512 MB" }),
        quotaChoices: [
          create(QuotaSizeChoiceViewSchema, { bytes: 536_870_912n, label: create(TextSchema, { literal: "512 MB" }) }),
        ],
        selectedQuotaBytes: 536_870_912n,
        usedTotalText: create(TextSchema, { literal: "480 MB" }),
        uploadedText: create(TextSchema, { literal: "400 MB" }),
        previewCacheText: create(TextSchema, { literal: "80 MB" }),
        uploadedPercent: 78,
        previewPercent: 16,
        reservePercent: 6,
      }),
      projectCount: 3,
      projectCountText: create(TextSchema, { literal: "3 个" }),
      shortId: "…1234abcd",
    });

    await mountAsync(
      <MachineSettingsScreenView
        id="m-test"
        vm={vm}
        go={go}
        onBack={() => undefined}
        onRetrySettings={() => undefined}
        onToggleAgent={onToggle}
        onUpdateAgentDefaults={onDefaults}
        onUpdateDaemonSettings={() => undefined}
      />,
    );

    // Module 0 status card with warn badge without dimming
    const ringsCard = host.querySelector<HTMLButtonElement>(".rings-card")!;
    expect(ringsCard.classList.contains("off")).toBe(false);
    expect(ringsCard.querySelector(".rings-hd span")?.classList.contains("warn-txt")).toBe(true);
    await act(async () => ringsCard.click());
    expect(go).toHaveBeenCalledWith("machine-status/m-test");

    // Module 1 quota & usage: primary orange ONLY, window calm ink2
    const quotaRow = host.querySelector<HTMLButtonElement>(".quota-line")!;
    const primarySpan = quotaRow.querySelector(".v .warn-txt")!;
    const windowSpan = quotaRow.querySelector(".v .qt-win")!;
    expect(primarySpan.textContent).toBe("剩 18%");
    expect(windowSpan.textContent).toBe(" · 5 小时");
    expect(windowSpan.classList.contains("warn-txt")).toBe(false);
    expect(host.textContent).toContain("近 30 天 3.4亿");

    // Module 2 CLI rows: unverified subtitle, FULL_ACCESS red nowrap, no flat project list
    const codexCliRow = host.querySelector<HTMLButtonElement>(".cli-row")!;
    expect(codexCliRow.textContent).toContain(message("settings.cli_unverified_short"));
    const modeTag = codexCliRow.querySelector(".red-txt.nowrap")!;
    expect(modeTag.textContent).toBe("完全放行");

    // Module 3 storage row orange summary -> navigates to m/<id>/storage
    const storageRow = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(message("settings.attachment_storage")),
    )!;
    expect(storageRow.querySelector(".v.warn-txt")).toBeTruthy();
    await act(async () => storageRow.click());
    expect(go).toHaveBeenCalledWith("m/m-test/storage");

    // Module 4 management: Projects row navigates to projects/<id>, Device ID mono, NO flat project rows
    const projRow = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.querySelector(".row-name")?.textContent === message("settings.projects_row"),
    )!;
    expect(projRow.querySelector(".v")?.textContent).toBe("3 个");
    await act(async () => projRow.click());
    expect(go).toHaveBeenCalledWith("projects/m-test");

    // Open CLI sheet: saving spinner + FULL_ACCESS sub-picker
    await act(async () => codexCliRow.click());
    expect(document.querySelector(".sheet .spin")).toBeTruthy();
    const modeRow = [...document.querySelectorAll<HTMLButtonElement>(".sheet button.row")].find((b) =>
      b.textContent?.includes(message("settings.cli_sheet_mode")),
    )!;
    expect(modeRow.querySelector(".red-txt .nowrap")?.textContent).toBe("完全放行");
    await act(async () => modeRow.click());
    const askChoice = [...document.querySelectorAll<HTMLButtonElement>(".sheet [role='radio']")].find((b) =>
      b.textContent?.includes("询问"),
    )!;
    await act(async () => askChoice.click());
    expect(onDefaults).toHaveBeenCalledWith(Agent.CODEX, { mode: ApprovalMode.ASK });
  });

  it("uses radio picker (not <select>) on Attachment Storage secondary page and updates quota limit", async () => {
    const r = createDemo("ms-full");
    const cmdSpy = vi.spyOn(r, "machineCommand");
    await mountAsync(<MachinePage remote={r} id="mac" section="storage" go={() => undefined} onBack={() => undefined} />);

    expect(host.querySelector("select")).toBeNull();
    expect(host.querySelector(".stor-bar")).toBeTruthy();
    expect(host.textContent).toContain(message("settings.storage_legend_uploaded"));
    expect(host.textContent).toContain(message("settings.storage_legend_preview"));
    expect(host.textContent).toContain(message("settings.storage_legend_reserve"));

    const limitRow = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(message("settings.storage_limit_row")),
    )!;
    await act(async () => limitRow.click());
    const choices = [...document.querySelectorAll<HTMLButtonElement>(".sheet [role='radio']")];
    expect(choices.length).toBeGreaterThan(1);
    const fiveGb = choices.find((c) => c.textContent?.includes("5 GB"))!;
    await act(async () => fiveGb.click());
    await flushUi();
    expect(cmdSpy).toHaveBeenCalledWith(
      "mac",
      expect.objectContaining({
        case: "updateSettings",
      }),
      expect.anything(),
    );
  });

  it("renders machine-filtered ManagePage with filter pill and reset action, plus CLI deep-link m/<id>/cli/<agent>", async () => {
    const r = createDemo();
    const go = vi.fn();
    await mountAsync(<ManagePage remote={r} machineId="mac" go={go} onBack={() => undefined} />);
    expect(host.querySelector(".proj-filter-bar")?.textContent).toContain("MacBook");
    const resetBtn = host.querySelector<HTMLButtonElement>(".proj-filter-bar button.link")!;
    expect(resetBtn.textContent).toBe(message("projects.filter_show_all"));
    await act(async () => resetBtn.click());
    expect(go).toHaveBeenCalledWith("projects", true);

    // Deep link m/mac/cli/2 opens Claude Code CLI sheet automatically
    await mountAsync(
      <MachinePage remote={r} id="mac" section="cli" subParam="2" go={go} onBack={() => undefined} />,
    );
    expect(document.querySelector(".sheet .cli-sh-ttl")?.textContent).toContain("Claude Code");
  });

  it("renders paired client lastUsedText (not generic roleLabel), hides single effort choice, and allows unloaded storage retry", async () => {
    const devVm = create(DevicesViewModelSchema, {
      clientCountText: create(TextSchema, { literal: "共 3 台客户端" }),
      clients: [
        create(ClientRowViewSchema, {
          deviceId: "c-self",
          displayName: "My iPhone",
          description: "iPhone 16 Pro",
          shortId: "…11112222",
          isSelf: true,
          roleLabel: create(TextSchema, { literal: "本机" }),
          lastUsedText: create(TextSchema, { literal: "现在" }),
        }),
        create(ClientRowViewSchema, {
          deviceId: "c-peer",
          displayName: "iPad Pro",
          description: "iPadOS",
          shortId: "…33334444",
          isSelf: false,
          isAuthority: false,
          roleLabel: create(TextSchema, { literal: "已配对的客户端" }),
          lastUsedText: create(TextSchema, { literal: "2 小时前" }),
        }),
      ],
    });
    await mountAsync(
      <ClientsScreenView vm={devVm} go={() => undefined} onBack={() => undefined} onRetry={() => undefined} />,
    );
    expect(host.querySelector(".lbl-meta")?.textContent).toBe("共 3 台客户端");
    const peerBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row.client")].find(
      (b) => b.querySelector(".row-name b")?.textContent === "iPad Pro",
    )!;
    expect(peerBtn.querySelector(".row-name small")?.textContent).toContain("…33334444");
    expect(peerBtn.querySelector(".v")?.textContent).toBe("2 小时前");

    // Unloaded storage remains clickable and AttachmentStorageScreenView shows error + retry
    const go = vi.fn();
    const onRetry = vi.fn();
    const vm = create(MachineSettingsViewModelSchema, {
      found: true,
      machineId: "m-unload",
      name: "Workbox",
      member: true,
      online: true,
      editable: true,
      showCliSwitches: true,
      cliSwitchesEditable: true,
      cliSwitchesFooter: create(TextSchema, { literal: "停用只影响新建会话选项" }),
      cliVersions: [
        create(CliVersionViewSchema, {
          agent: Agent.CODEX,
          name: "Codex",
          version: "1.0.0",
          verified: true,
          enabled: true,
          defaults: create(MachineAgentDefaultsViewSchema, {
            agent: Agent.CODEX,
            agentName: "Codex",
            effortChoices: [create(EffortChoiceViewSchema, { id: "medium", label: create(TextSchema, { literal: "中" }), selected: true })],
          }),
        }),
        create(CliVersionViewSchema, {
          agent: Agent.CLAUDE,
          name: "Claude Code",
          version: "2.0.0",
          verified: true,
          enabled: true,
        }),
      ],
      attachmentStorage: create(AttachmentStorageViewSchema, { loaded: false }),
      settingsError: create(TextSchema, { literal: "读取设置失败" }),
    });
    await mountAsync(
      <MachineSettingsScreenView
        id="m-unload"
        vm={vm}
        go={go}
        onBack={() => undefined}
        onRetrySettings={onRetry}
        onUpdateAgentDefaults={() => undefined}
        onUpdateDaemonSettings={() => undefined}
      />,
    );
    const storBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(message("settings.attachment_storage")),
    )!;
    expect(storBtn.disabled).toBe(false);
    await act(async () => storBtn.click());
    expect(go).toHaveBeenCalledWith("m/m-unload/storage");

    // Open CLI sheet: single effort hides 推理强度 row; cliSwitchesFooter displays even without leaseExpired
    const codexBtn = host.querySelector<HTMLButtonElement>(".cli-row")!;
    await act(async () => codexBtn.click());
    expect(document.querySelector(".sheet")?.textContent).toContain("停用只影响新建会话选项");
    expect(document.querySelector(".sheet")?.textContent).not.toContain(message("settings.cli_sheet_effort"));

    await mountAsync(
      <AttachmentStorageScreenView
        id="m-unload"
        vm={vm}
        onBack={() => undefined}
        onRetrySettings={onRetry}
        onUpdateDaemonSettings={() => undefined}
      />,
    );
    expect(host.textContent).toContain("读取设置失败");
    const retryBtn = host.querySelector<HTMLButtonElement>("button.link")!;
    await act(async () => retryBtn.click());
    expect(onRetry).toHaveBeenCalled();
  });
});

describe("Stylesheet invariants (46px/34px alignment, desktop 640px pane, 200% wrap, locked switch)", () => {
  it("enforces design-system CSS tokens and container wrapping rules in styles.css", () => {
    const css = readFileSync("src/styles.css", "utf8");
    expect(css).toMatch(/\.set\s+\.row\.has-ic\s*\+\s*\.row\.has-ic::before\s*\{[^}]*left:\s*46px/);
    expect(css).toMatch(/\.diag-check\s*\+\s*\.diag-check::before\s*\{[^}]*left:\s*34px/);
    expect(css).toContain("calc((100% - 640px) / 2)");
    expect(css).toMatch(/@container\s*\(max-width:\s*21rem\)/);
    expect(css).toMatch(/\.nowrap\s*\{[^}]*white-space:\s*nowrap/);
    expect(css).toMatch(/\.cli-sw-row\.locked\s+\.sw\s*\{[^}]*opacity:\s*\.4/);
  });
});
