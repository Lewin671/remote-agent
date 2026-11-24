// @vitest-environment happy-dom
// Golden ViewModel layout and interaction tests rendered directly from
// testdata/screens/*.golden.pb binary protobuf goldens without TS rules or Remote.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fromBinary } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type ScreenGoldenFile, ScreenGoldenFileSchema } from "../gen/remoteagent/core/v1/golden_pb";
import type { ViewModel } from "../gen/remoteagent/core/v1/app_pb";
import { ScheduleFormMode, ScheduleOperationKind } from "../gen/remoteagent/core/v1/schedules_view_pb";
import type { SessionViewModel } from "../gen/remoteagent/core/v1/session_view_pb";
import { MachinePollState, MachineUsageTab } from "../gen/remoteagent/core/v1/machine_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { localeStore, t } from "../i18n";
import { ThemePreference } from "../core/host";
import { HomeView, SidebarView } from "./Home";
import { TrayView } from "./Attach";
import { NewBoxView } from "./NewSession";
import { ManageProjectsScreen, RemovedNotice } from "./Projects";
import {
  ScheduleDetailView,
  ScheduleFormView,
  ScheduleStripView,
  SchedulesListView,
  TimingSheetView,
} from "./Schedules";
import { SessionScreenView, type SessionScreenViewProps } from "./Session";
import {
  ClientDetailScreenView,
  DevicesScreenView,
  MachineSettingsScreenView,
  QuotaScreenView,
  SettingsScreenView,
} from "./Settings";
import { MachineStatusScreenView } from "./MachineStatus";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type GoldenDoc = ScreenGoldenFile;

const GOLDENS_DIR = resolve(__dirname, "../../../testdata/screens");

function loadGolden(name: string): GoldenDoc {
  const bytes = readFileSync(resolve(GOLDENS_DIR, `${name}.golden.pb`));
  return fromBinary(ScreenGoldenFileSchema, new Uint8Array(bytes));
}

const homeTimelineGolden = loadGolden("home_timeline");
const homeEmptyGolden = loadGolden("home_empty_errors");
const projectManageGolden = loadGolden("project_manage_detail");
const newSessionGolden = loadGolden("new_session_lifecycle");
const sessionApprovalGolden = loadGolden("session_approval_flow");
const sessionCliErrorGolden = loadGolden("session_cli_error_turn");
const sessionLongWindowGolden = loadGolden("session_long_window");
const sessionQuestionPlanGolden = loadGolden("session_question_plan");
const sessionQueueRetryGolden = loadGolden("session_queue_retry");
const sessionScheduledFoldGolden = loadGolden("session_scheduled_fold");
const sessionSharedFilesImagesGolden = loadGolden("session_shared_files_images");
const sessionAttachmentsDraftGolden = loadGolden("session_attachments_draft");
const schedulesInboxDetailGolden = loadGolden("schedules_inbox_detail");
const schedulesFormLifecycleGolden = loadGolden("schedules_form_lifecycle");
const settingsOverviewGolden = loadGolden("settings_overview");
const machineSettingsEditGolden = loadGolden("machine_settings_edit");
const machineStatusLifecycleGolden = loadGolden("machine_status_lifecycle");
const machineQuotaGolden = loadGolden("machine_quota");

function goldenView(doc: GoldenDoc, viewName: string): ViewModel {
  for (const step of doc.steps) {
    const found = step.views.find((v) => v.name === viewName);
    if (found?.viewModel) {
      return found.viewModel;
    }
  }
  throw new Error(`Missing golden view: ${viewName}`);
}

let root: Root | undefined;
let host: HTMLDivElement;

function mount(node: React.ReactNode) {
  if (root) {
    act(() => root?.unmount());
    host?.remove();
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  localeStore.overrideForTesting();
});

describe("golden ViewModel layout and interaction", () => {
  it("renders HomeView and SidebarView from home_timeline.golden.json and dispatches interactions", () => {
    const initial = goldenView(homeTimelineGolden as GoldenDoc, "homeInitial");
    expect(initial.view.case).toBe("home");
    if (initial.view.case !== "home") return;
    const vm = initial.view.value;

    const onSelectScope = vi.fn();
    const onSetFilter = vi.fn();
    const onOpenSession = vi.fn();
    const onManageProjects = vi.fn();

    mount(
      <HomeView
        vm={vm}
        onSelectScope={onSelectScope}
        onSetFilter={onSetFilter}
        onOpenSession={onOpenSession}
        onOpenSettings={vi.fn()}
        onManageProjects={onManageProjects}
        onAddDevice={vi.fn()}
        onStart={vi.fn()}
        onLoadMoreEnded={vi.fn()}
        onRetryLoad={vi.fn()}
        onOpenDetails={vi.fn()}
      />,
    );

    expect(host.querySelector(".hl em")?.textContent).toBe("1");
    expect(host.querySelector(".tc .cmd")?.textContent).toBe("go test ./...");
    expect(host.querySelector(".tc h4")?.textContent).toBe("Run tests");
    expect([...host.querySelectorAll(".sess .row1 b")].map((e) => e.textContent)).toEqual([
      "Refactor cache",
      "Check lint",
    ]);
    expect([...host.querySelectorAll(".old .sm > span:first-child")].map((e) => e.textContent)).toEqual([
      "Fix test today",
      "Yesterday cleanup",
      "September test",
    ]);

    act(() => host.querySelector<HTMLButtonElement>(".tc h4 button")!.click());
    expect(onOpenSession).toHaveBeenCalledWith("s-appr");

    act(() => host.querySelector<HTMLButtonElement>(".tbar .scope")!.click());
    const betaBtn = [...host.querySelectorAll<HTMLButtonElement>(".plist .pi")].find((b) =>
      b.textContent?.includes("beta"),
    )!;
    expect(betaBtn).toBeTruthy();
    act(() => betaBtn.click());
    expect(onSelectScope).toHaveBeenCalledWith("/Users/me/Code/beta");

    act(() => root!.unmount());
    mount(
      <SidebarView
        vm={vm}
        tasks={false}
        onSelectScope={onSelectScope}
        onSetFilter={onSetFilter}
        onSelectSession={onOpenSession}
        onNewTask={vi.fn()}
        onToggleTasks={vi.fn()}
        onOpenSettings={vi.fn()}
        onManageProjects={onManageProjects}
        onOpenConnCheck={vi.fn()}
        onLoadMoreEnded={vi.fn()}
        onRetryLoad={vi.fn()}
      />,
    );
    expect(host.querySelector(".side .conn")?.textContent).toBe(t("connection.connected"));
    expect(host.querySelectorAll(".side .sess").length).toBeGreaterThanOrEqual(3);
  });

  it("renders empty and error Home states from home_empty_errors.golden.pb", () => {
    const decoded = homeEmptyGolden.steps
      .flatMap((s) => s.views)
      .find((v) => v.viewModel?.view.case === "home")?.viewModel;
    expect(decoded).toBeTruthy();
    if (!decoded || decoded.view.case !== "home") throw new Error("expected home view");

    mount(
      <HomeView
        vm={decoded.view.value}
        onSelectScope={vi.fn()}
        onSetFilter={vi.fn()}
        onOpenSession={vi.fn()}
        onOpenSettings={vi.fn()}
        onManageProjects={vi.fn()}
        onAddDevice={vi.fn()}
        onStart={vi.fn()}
        onLoadMoreEnded={vi.fn()}
        onRetryLoad={vi.fn()}
        onOpenDetails={vi.fn()}
      />,
    );
    expect(host.querySelector(".hl")?.textContent).toBeTruthy();
  });

  it("renders ManageProjectsScreen & RemovedNotice from project_manage_detail.golden.json", () => {
    const manage = goldenView(projectManageGolden as GoldenDoc, "managePinning");
    if (manage.view.case !== "project" || !manage.view.value.manage) {
      throw new Error("expected project manage view");
    }
    const vm = manage.view.value.manage;
    const onSearch = vi.fn();
    const onTogglePin = vi.fn();
    const onToggleHide = vi.fn();
    const onOpenDirPicker = vi.fn();
    const onSelectProject = vi.fn();

    mount(
      <ManageProjectsScreen
        vm={vm}
        onSearch={onSearch}
        onTogglePin={onTogglePin}
        onToggleHide={onToggleHide}
        onOpenDirPicker={onOpenDirPicker}
        onDirNavigate={vi.fn()}
        onCloseDirPicker={vi.fn()}
        onAddProject={vi.fn()}
        onSelectProject={onSelectProject}
        onBack={vi.fn()}
      />,
    );

    expect(host.querySelector(".plist .pi b")?.textContent).toBe("alpha");
    act(() => host.querySelector<HTMLButtonElement>(".dots")!.click());
    const pinBtn = [...host.querySelectorAll<HTMLButtonElement>(".menu .mi")].find((b) =>
      b.textContent?.includes(t("projects.unpin")),
    )!;
    act(() => pinBtn.click());
    expect(onTogglePin).toHaveBeenCalledWith("/Users/me/Code/alpha");

    act(() => host.querySelector<HTMLButtonElement>(".pi.add")!.click());
    expect(onOpenDirPicker).toHaveBeenCalled();

    const detail = goldenView(projectManageGolden as GoldenDoc, "detailInitial");
    if (detail.view.case !== "project" || !detail.view.value.detail) {
      throw new Error("expected project detail view");
    }
    const onReadd = vi.fn();
    const hiddenNotice = {
      ...detail.view.value.detail.hiddenNotice!,
      active: true,
    };
    act(() => root!.render(<RemovedNotice notice={hiddenNotice} onReadd={onReadd} />));
    act(() => host.querySelector<HTMLButtonElement>(".banner button")!.click());
    expect(onReadd).toHaveBeenCalled();
  });

  it("renders NewBoxView from new_session_lifecycle.golden.json and dispatches actions", () => {
    const vmWrap = goldenView(newSessionGolden as GoldenDoc, "offlineWithDraftChoices");
    if (vmWrap.view.case !== "newSession") throw new Error("expected newSession view");
    const vm = vmWrap.view.value;

    const onSelectMachine = vi.fn();
    const onSetPrompt = vi.fn();
    const onSubmit = vi.fn();

    mount(
      <NewBoxView
        vm={vm}
        variant="start"
        onPickProject={vi.fn()}
        onSelectMachine={onSelectMachine}
        onSelectAgent={vi.fn()}
        onSelectModel={vi.fn()}
        onRetryModels={vi.fn()}
        onRetrySaveConfig={vi.fn()}
        onSetPrompt={onSetPrompt}
        onSubmit={onSubmit}
        onAbandonStart={vi.fn()}
        onOpenMachineSettings={vi.fn()}
        onOpenDiskUsage={vi.fn()}
        onReaddProject={vi.fn()}
      />,
    );

    const changeMachineBtn = [...host.querySelectorAll<HTMLButtonElement>(".nt-banners button")].find((b) =>
      b.textContent?.includes(t("new_session.change_machine")),
    )!;
    expect(changeMachineBtn).toBeTruthy();
    act(() => changeMachineBtn.click());
    const machineChoiceBtn = [...host.querySelectorAll<HTMLButtonElement>(".mi")].find((b) =>
      b.textContent?.includes("Workstation"),
    )!;
    expect(machineChoiceBtn).toBeTruthy();
    act(() => machineChoiceBtn.click());
    expect(onSelectMachine).toHaveBeenCalledWith("m-on");

    const input = host.querySelector<HTMLTextAreaElement>("textarea")!;
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "Implement feature");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(onSetPrompt).toHaveBeenCalledWith("Implement feature");
  });
});

function sessionGoldenView(doc: GoldenDoc, viewName: string): SessionViewModel {
  const decoded = goldenView(doc, viewName);
  if (decoded.view.case !== "session") throw new Error(`Expected session view for ${viewName}`);
  return decoded.view.value;
}

function renderSessionGolden(vm: SessionViewModel, overrides: Partial<SessionScreenViewProps> = {}) {
  const props: SessionScreenViewProps = {
    vm,
    wide: false,
    onBack: vi.fn(),
    draft: "",
    onDraft: vi.fn(),
    onSend: vi.fn(),
    onSubmitCommand: vi.fn(),
    onAbandonCommand: vi.fn(),
    onCancelTrayItem: vi.fn(),
    onIgnoreTrayItem: vi.fn(),
    onPromoteTrayItem: vi.fn(),
    onRestoreTrayItem: vi.fn(),
    onRetryTrayItem: vi.fn(),
    onResumeTrayItem: vi.fn(),
    onCancelAllPaused: vi.fn(),
    onUndoCancel: vi.fn(),
    onLoadEarlier: vi.fn(),
    onExpandGroup: vi.fn(),
    ...overrides,
  };
  if (root) {
    act(() => root!.render(<SessionScreenView {...props} />));
  } else {
    mount(<SessionScreenView {...props} />);
  }
}

describe("golden SessionScreenView layout and interaction", () => {
  it("renders approval flow states and dispatches approval command from session_approval_flow.golden.json", () => {
    const onSubmitCommand = vi.fn();
    const pendingVm = sessionGoldenView(sessionApprovalGolden as GoldenDoc, "pendingApprovalDock");
    renderSessionGolden(pendingVm, { onSubmitCommand });

    expect(host.textContent).toContain("Approval test");
    expect(host.textContent).toContain("git status && ls src");
    const approveBtn = [...host.querySelectorAll<HTMLButtonElement>(".pending button")].find((b) =>
      b.textContent?.includes(t("approval.allow")),
    )!;
    expect(approveBtn).toBeTruthy();
    act(() => approveBtn.click());
    expect(onSubmitCommand).toHaveBeenCalled();
    expect(onSubmitCommand.mock.calls[0][0]).toBe("approval:s-ap:ap-1");
    expect(onSubmitCommand.mock.calls[0][1].kind).toMatchObject({
      case: "resolveApproval",
      value: { sessionId: "s-ap", approvalId: "ap-1", allow: true },
    });

    const submittingVm = sessionGoldenView(sessionApprovalGolden as GoldenDoc, "approvalSubmitting");
    renderSessionGolden(submittingVm);
    expect(host.querySelector(".pending.submitting")).toBeTruthy();
    expect(host.textContent).toContain(t("approval.allowed"));

    const mergedVm = sessionGoldenView(sessionApprovalGolden as GoldenDoc, "approvalMergedOnToolCall");
    renderSessionGolden(mergedVm);
    expect(host.querySelector(".pending")).toBeNull();
    expect(host.textContent).toContain("git status");
    expect(host.textContent).toContain(t("approval.always_allowed"));
    expect(host.textContent).toContain(t("session.origin_elsewhere"));
  });

  it("renders CLI error turns and interrupted markers from session_cli_error_turn.golden.json", () => {
    const vm = sessionGoldenView(sessionCliErrorGolden as GoldenDoc, "cliErrorTurns");
    renderSessionGolden(vm);

    expect(host.textContent).toContain("refactor auth");
    expect(host.textContent).toContain("You've hit your usage limit. Try again later.");
    expect(host.textContent).toContain("rate_limit_exceeded");
    expect(host.textContent).toContain("abort manually");
    expect(host.textContent).toContain(t("session.interrupted"));
    expect(host.querySelectorAll(".sysline.fail").length).toBeGreaterThanOrEqual(2);
  });

  it("renders windowed history slices and triggers onLoadEarlier from session_long_window.golden.json", () => {
    const onLoadEarlier = vi.fn();
    const tailVm = sessionGoldenView(sessionLongWindowGolden as GoldenDoc, "tail10");
    renderSessionGolden(tailVm, { onLoadEarlier });

    expect(host.textContent).toContain("Long session");
    const moreBtn = host.querySelector<HTMLButtonElement>(".load-earlier .more-row")!;
    expect(moreBtn).toBeTruthy();
    expect(moreBtn.textContent).toBe(t("schedule.earlier"));
    act(() => moreBtn.click());
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);

    const earlierVm = sessionGoldenView(sessionLongWindowGolden as GoldenDoc, "earlierBefore109");
    renderSessionGolden(earlierVm, { onLoadEarlier });
    expect([...host.querySelectorAll(".me")].length).toBeGreaterThanOrEqual(3);
    expect(host.textContent).toContain(t("schedule.earlier"));
  });

  it("renders question steps, plan dock, unknown frozen retry & abandon from session_question_plan.golden.json", () => {
    const onSubmitCommand = vi.fn();
    const onAbandonCommand = vi.fn();

    const q0Vm = sessionGoldenView(sessionQuestionPlanGolden as GoldenDoc, "questionStep0");
    renderSessionGolden(q0Vm, { onSubmitCommand, onAbandonCommand });
    expect(host.textContent).toContain("Which package?");
    const coreOpt = [...host.querySelectorAll<HTMLButtonElement>(".pending button")].find((b) =>
      b.textContent?.includes("Core"),
    )!;
    expect(coreOpt).toBeTruthy();
    act(() => coreOpt.click());
    expect(onSubmitCommand).toHaveBeenCalled();

    const q1Vm = sessionGoldenView(sessionQuestionPlanGolden as GoldenDoc, "questionStep1");
    renderSessionGolden(q1Vm, { onSubmitCommand, onAbandonCommand });
    expect(host.textContent).toContain("Select checks");

    const planOpenVm = sessionGoldenView(sessionQuestionPlanGolden as GoldenDoc, "planDockOpen");
    renderSessionGolden(planOpenVm, { onSubmitCommand, onAbandonCommand });
    expect(host.textContent).toContain("Step A");
    expect(host.textContent).toContain(t("dock.plan_implement_record"));

    const frozenVm = sessionGoldenView(sessionQuestionPlanGolden as GoldenDoc, "planUnknownFrozen");
    renderSessionGolden(frozenVm, { onSubmitCommand, onAbandonCommand });
    expect(host.textContent).toContain(t("errors.the_computer_s_reply_could_not_be_verified_command"));
    const retryBtn = [...host.querySelectorAll<HTMLButtonElement>(".pending button")].find((b) =>
      b.textContent?.includes(t("common.retry")),
    )!;
    expect(retryBtn).toBeTruthy();
    act(() => retryBtn.click());
    expect(onSubmitCommand).toHaveBeenCalledWith(
      "plan:s-qp",
      expect.objectContaining({
        kind: expect.objectContaining({ case: "answerPlan", value: expect.objectContaining({ sessionId: "s-qp" }) }),
      }),
    );

    const abandonedVm = sessionGoldenView(sessionQuestionPlanGolden as GoldenDoc, "planAbandoned");
    renderSessionGolden(abandonedVm, { onSubmitCommand, onAbandonCommand });
    expect(host.textContent).toContain(t("dock.plan_implement_record"));
  });

  it("renders failed/unsure tray rows and pending queue from session_queue_retry.golden.json", () => {
    const onCancelTrayItem = vi.fn();
    const onIgnoreTrayItem = vi.fn();
    const onRestoreTrayItem = vi.fn();
    const onRetryTrayItem = vi.fn();

    const bothVm = sessionGoldenView(sessionQueueRetryGolden as GoldenDoc, "outgoingFailedAndUnknown");
    renderSessionGolden(bothVm, { onCancelTrayItem, onIgnoreTrayItem, onRestoreTrayItem, onRetryTrayItem });

    expect(host.querySelector(".ptray")?.textContent).toContain("steer failed msg");
    expect(host.querySelector(".ptray")?.textContent).toContain("queued second");
    expect(host.querySelector(".ptray-gl.red")).toBeTruthy();
    expect(host.querySelector(".ptray-gl.orange")).toBeTruthy();
    expect(host.querySelector(".ptray")?.textContent).toContain(t("errors.disconnected_from_server"));
    expect(host.querySelector(".ptray")?.textContent).toContain(
      t("errors.the_computer_s_reply_could_not_be_verified_command"),
    );

    const rows = [...host.querySelectorAll<HTMLElement>(".ptray-row")];
    act(() => rows[0].querySelector<HTMLElement>(".ptray-hit")!.click());
    const restoreBtn = [...rows[0].querySelectorAll<HTMLButtonElement>(".ptray-opts button")].find((b) =>
      b.textContent?.includes(t("composer.restore")),
    )!;
    act(() => restoreBtn.click());
    expect(onRestoreTrayItem).toHaveBeenCalledWith("msg-fail");

    const retryBtn = [...rows[0].querySelectorAll<HTMLButtonElement>(".ptray-acts button")].find((b) =>
      b.textContent?.includes(t("common.retry")),
    )!;
    act(() => retryBtn.click());
    expect(onRetryTrayItem).toHaveBeenCalledWith("msg-fail");

    const ignoreBtn = [...rows[1].querySelectorAll<HTMLButtonElement>(".ptray-acts button")].find((b) =>
      b.textContent?.includes(t("queue.ignore")),
    )!;
    act(() => ignoreBtn.click());
    expect(onIgnoreTrayItem).toHaveBeenCalledWith("msg-q");

    const queuedVm = sessionGoldenView(sessionQueueRetryGolden as GoldenDoc, "queuedPendingAndFailedOutgoing");
    renderSessionGolden(queuedVm, { onCancelTrayItem, onIgnoreTrayItem, onRestoreTrayItem, onRetryTrayItem });
    const queuedRow = [...host.querySelectorAll<HTMLElement>(".ptray-row")].find((el) =>
      el.textContent?.includes("queued second"),
    )!;
    expect(host.querySelector(".ptray")?.textContent).toContain(t("queue.group_queue"));
    const cancelQueueBtn = queuedRow.querySelector<HTMLButtonElement>(".ptray-acts button.x")!;
    act(() => cancelQueueBtn.click());
    expect(onCancelTrayItem).toHaveBeenCalledWith("msg-q");
  });

  it("renders folded quiet scheduled runs and dispatches onExpandGroup from session_scheduled_fold.golden.json", () => {
    const onExpandGroup = vi.fn();
    const foldedVm = sessionGoldenView(sessionScheduledFoldGolden as GoldenDoc, "foldedQuietRuns");
    renderSessionGolden(foldedVm, { onExpandGroup });

    const foldBtn = host.querySelector<HTMLButtonElement>("button.schedule-quiet")!;
    expect(foldBtn).toBeTruthy();
    expect(host.textContent).toContain("Alert: disk usage 95%!");
    // The agent's short push renders as a receipt line that cannot open.
    expect(host.querySelector(".nt:not(.can) .tx")?.textContent).toMatch(/ · Disk warning sent$/);
    act(() => foldBtn.click());
    expect(onExpandGroup).toHaveBeenCalledWith("quiet-run-1");

    const revealedVm = sessionGoldenView(sessionScheduledFoldGolden as GoldenDoc, "revealedRun2");
    renderSessionGolden(revealedVm, { onExpandGroup });
    expect(host.querySelector("button.schedule-quiet")).toBeNull();
    expect([...host.querySelectorAll(".me")].filter((m) => m.textContent?.includes("Check health"))).toHaveLength(4);
  });

  it("renders schedules list, detail, and session strip from schedules_inbox_detail.golden.json", () => {
    const listVm = goldenView(schedulesInboxDetailGolden as GoldenDoc, "schedules_1");
    expect(listVm.view.case).toBe("schedules");
    if (listVm.view.case !== "schedules") return;

    const onSelectTask = vi.fn();
    const onMarkAllRead = vi.fn();
    mount(
      <SchedulesListView
        vm={listVm.view.value}
        actions={{
          onSelectTask,
          onCreateTask: vi.fn(),
          onToggleEnded: vi.fn(),
          onMarkAllRead,
          onMarkRead: vi.fn(),
          onMarkUnread: vi.fn(),
          onOperate: vi.fn(),
          onRetryMachine: vi.fn(),
        }}
      />,
    );
    expect(host.textContent).toContain("One-shot deploy check");
    expect(host.querySelector(".rc.warn")).toBeTruthy();
    expect(host.querySelector(".sec-act")).toBeNull();
    mount(
      <SchedulesListView
        vm={{ ...listVm.view.value, canMarkAllRead: true }}
        actions={{
          onSelectTask,
          onCreateTask: vi.fn(),
          onToggleEnded: vi.fn(),
          onMarkAllRead,
          onMarkRead: vi.fn(),
          onMarkUnread: vi.fn(),
          onOperate: vi.fn(),
          onRetryMachine: vi.fn(),
        }}
      />,
    );
    act(() => host.querySelector<HTMLButtonElement>(".sec-act")!.click());
    expect(onMarkAllRead).toHaveBeenCalledTimes(1);
    act(() => host.querySelector<HTMLButtonElement>(".schedule-line")!.click());
    expect(onSelectTask).toHaveBeenCalledWith("m1", "t-att");

    const detailVm = goldenView(schedulesInboxDetailGolden as GoldenDoc, "scheduleDetail_4");
    expect(detailVm.view.case).toBe("scheduleDetail");
    if (detailVm.view.case !== "scheduleDetail") return;
    const onOperate = vi.fn();
    const onOpenEdit = vi.fn();
    mount(
      <ScheduleDetailView
        vm={detailVm.view.value}
        actions={{
          onBack: vi.fn(),
          onOperate,
          onMarkRead: vi.fn(),
          onMarkUnread: vi.fn(),
          onOpenSession: vi.fn(),
          onShowLatestResult: vi.fn(),
          onToggleQuietGroup: vi.fn(),
          onShowAllRuns: vi.fn(),
          onOpenRun: vi.fn(),
          onSetNotify: vi.fn(),
          onRetryMachine: vi.fn(),
          onOpenEdit,
          onRetryMutation: vi.fn(),
          onAbandonMutation: vi.fn(),
        }}
      />,
    );
    expect(host.querySelector(".navbar .title b")?.textContent).toBe("Daily summary");
    expect(host.querySelector(".schedule-result .schedule-unread-dot")).toBeTruthy();
    const editMsgBtn = [...host.querySelectorAll<HTMLButtonElement>("button.link")].find((b) =>
      b.textContent === t("tools.android_edit"),
    )!;
    act(() => editMsgBtn.click());
    expect(onOpenEdit).toHaveBeenCalledWith(ScheduleFormMode.EDIT_MESSAGE);
    act(() => host.querySelector<HTMLButtonElement>('[aria-label="更多"]')!.click());
    const runBtn = [...host.querySelectorAll<HTMLButtonElement>(".schedule-menu button")].find(
      (b) => b.textContent === t("schedule.run_once_now"),
    )!;
    act(() => runBtn.click());
    expect(onOperate).toHaveBeenCalledWith(ScheduleOperationKind.RUN);

    const stripVm = goldenView(schedulesInboxDetailGolden as GoldenDoc, "scheduleSessionStrip_2");
    expect(stripVm.view.case).toBe("scheduleSessionStrip");
    if (stripVm.view.case !== "scheduleSessionStrip") return;
    const onStripSelect = vi.fn();
    mount(<ScheduleStripView vm={stripVm.view.value} onSelectTask={onStripSelect} />);
    const stripBtn = host.querySelector<HTMLButtonElement>(".schedule-strip")!;
    expect(stripBtn.textContent).toContain("08:00");
    act(() => stripBtn.click());
    expect(onStripSelect).toHaveBeenCalledTimes(1);
  });

  it("renders schedule form, timing draft, and detail from schedules_form_lifecycle.golden.json", () => {
    const form2 = goldenView(schedulesFormLifecycleGolden as GoldenDoc, "scheduleForm_2");
    expect(form2.view.case).toBe("scheduleForm");
    if (form2.view.case !== "scheduleForm") return;

    const onSetMessage = vi.fn();
    const onSubmit = vi.fn();
    const onOpenTiming = vi.fn();
    mount(
      <ScheduleFormView
        vm={form2.view.value}
        actions={{
          onSetMessage,
          onSetTitle: vi.fn(),
          onSetNotify: vi.fn(),
          onOpenTiming,
          onOpenProjectPicker: vi.fn(),
          onSelectAgent: vi.fn(),
          onOpenConfig: vi.fn(),
          onSubmit,
          onRetryMutation: vi.fn(),
          onAbandonMutation: vi.fn(),
        }}
      />,
    );
    expect(host.querySelector("textarea")?.value).toBe("Weekly audit. Run linters.");
    expect(host.querySelector(".schedule-name input")?.getAttribute("placeholder")).toContain("Weekly audit");
    const submitBtn = host.querySelector<HTMLButtonElement>("button.pri")!;
    act(() => submitBtn.click());
    expect(onSubmit).toHaveBeenCalledWith(false);

    const onUpdateTiming = vi.fn();
    mount(
      <TimingSheetView
        draft={form2.view.value.timing!}
        onUpdate={onUpdateTiming}
        onClose={vi.fn()}
      />,
    );
    const doneBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("common.done"),
    )!;
    act(() => doneBtn.click());
    expect(onUpdateTiming).toHaveBeenCalledWith(expect.objectContaining({ apply: true, zoneValid: true }));

    const detail5 = goldenView(schedulesFormLifecycleGolden as GoldenDoc, "scheduleDetail_5");
    expect(detail5.view.case).toBe("scheduleDetail");
    if (detail5.view.case !== "scheduleDetail") return;
    mount(
      <ScheduleDetailView
        vm={detail5.view.value}
        actions={{
          onBack: vi.fn(),
          onOperate: vi.fn(),
          onMarkRead: vi.fn(),
          onMarkUnread: vi.fn(),
          onOpenSession: vi.fn(),
          onShowLatestResult: vi.fn(),
          onToggleQuietGroup: vi.fn(),
          onShowAllRuns: vi.fn(),
          onOpenRun: vi.fn(),
          onSetNotify: vi.fn(),
          onRetryMachine: vi.fn(),
          onOpenEdit: vi.fn(),
          onRetryMutation: vi.fn(),
          onAbandonMutation: vi.fn(),
        }}
      />,
    );
    expect(host.querySelector(".navbar .title b")?.textContent).toBe("Weekly audit");
  });

  it("renders settings_overview.golden.json screens (Settings, Devices, ClientDetail) and dispatches actions", () => {
    const settingsVm = goldenView(settingsOverviewGolden as GoldenDoc, "settings");
    expect(settingsVm.view.case).toBe("settings");
    if (settingsVm.view.case !== "settings") return;

    const onCheck = vi.fn();
    const onApply = vi.fn();
    const onTheme = vi.fn();
    const onGo = vi.fn();
    mount(
      <SettingsScreenView
        vm={settingsVm.view.value}
        go={onGo}
        serverHost="relay.example.com"
        connTone="online"
        connText="在线"
        onCheckForUpdates={onCheck}
        onApplyUpdate={onApply}
        onSetTheme={onTheme}
        onSetNotificationPref={vi.fn()}
        onEnablePushNotifications={vi.fn()}
      />,
    );
    expect(host.textContent).toContain("BuildBox");
    const applyBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes(t("home.update_ready")),
    )!;
    expect(applyBtn).toBeTruthy();
    act(() => applyBtn.click());
    expect(onApply).toHaveBeenCalledTimes(1);

    const darkRadio = [...host.querySelectorAll<HTMLButtonElement>("[role='radio']")].find((b) =>
      b.textContent === t("settings.dark"),
    )!;
    act(() => darkRadio.click());
    expect(onTheme).toHaveBeenCalledWith(ThemePreference.DARK);

    const checkingVm = goldenView(settingsOverviewGolden as GoldenDoc, "checkingUpdate");
    expect(checkingVm.view.case).toBe("settings");
    if (checkingVm.view.case !== "settings") return;
    const baseCheck = {
      ...checkingVm.view.value,
      update: checkingVm.view.value.update ? { ...checkingVm.view.value.update, hasUpdate: false, checking: false } : undefined,
    };
    act(() =>
      root!.render(
        <SettingsScreenView
          vm={baseCheck}
          go={onGo}
          serverHost="relay.example.com"
          connTone="online"
          connText="在线"
          onCheckForUpdates={onCheck}
          onApplyUpdate={onApply}
          onSetTheme={onTheme}
          onSetNotificationPref={vi.fn()}
          onEnablePushNotifications={vi.fn()}
        />,
      ),
    );
    const checkBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes(t("settings.check_for_updates")),
    )!;
    act(() => checkBtn.click());
    expect(onCheck).toHaveBeenCalledTimes(1);

    act(() =>
      root!.render(
        <SettingsScreenView
          vm={{
            ...baseCheck,
            update: baseCheck.update ? { ...baseCheck.update, checking: true } : undefined,
          }}
          go={onGo}
          serverHost="relay.example.com"
          connTone="online"
          connText="在线"
          onCheckForUpdates={onCheck}
          onApplyUpdate={onApply}
          onSetTheme={onTheme}
          onSetNotificationPref={vi.fn()}
          onEnablePushNotifications={vi.fn()}
        />,
      ),
    );
    const disabledCheck = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes(t("settings.check_for_updates")),
    )!;
    expect(disabledCheck.disabled).toBe(true);

    const devicesVm = goldenView(settingsOverviewGolden as GoldenDoc, "devices");
    expect(devicesVm.view.case).toBe("devices");
    if (devicesVm.view.case !== "devices") return;
    const devView = devicesVm.view.value;
    act(() => root!.render(<DevicesScreenView vm={devView} go={onGo} onBack={vi.fn()} onRetry={vi.fn()} />));
    expect(host.textContent).not.toContain("BuildBox");
    expect(host.querySelectorAll(".row.client").length).toBeGreaterThan(0);

    const authVm = goldenView(settingsOverviewGolden as GoldenDoc, "authClient");
    expect(authVm.view.case).toBe("clientDetail");
    if (authVm.view.case !== "clientDetail") return;
    const authView = authVm.view.value;
    act(() => root!.render(<ClientDetailScreenView vm={authView} rename={{ saving: false, error: "" }} onBack={vi.fn()} onUpdateName={vi.fn()} />));
    const renameBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(t("settings.name")),
    )!;
    expect(renameBtn.disabled).toBe(true);

    const selfVm = goldenView(settingsOverviewGolden as GoldenDoc, "selfClient");
    expect(selfVm.view.case).toBe("clientDetail");
    if (selfVm.view.case !== "clientDetail") return;
    const selfView = selfVm.view.value;
    const onRename = vi.fn();
    act(() => root!.render(<ClientDetailScreenView vm={selfView} rename={{ saving: false, error: "" }} onBack={vi.fn()} onUpdateName={onRename} />));
    const selfRenameBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(t("settings.name")),
    )!;
    expect(selfRenameBtn.disabled).toBe(false);
    act(() => selfRenameBtn.click());
    const input = document.querySelector<HTMLInputElement>("input.field")!;
    expect(input).toBeTruthy();
    const saveBtn = [...document.querySelectorAll<HTMLButtonElement>(".sheet button.pri")].find((b) =>
      b.textContent === t("common.save"),
    )!;
    act(() => saveBtn.click());
    expect(onRename).toHaveBeenCalledTimes(1);
  });

  it("renders machine_settings_edit.golden.json lifecycle and dispatches agent/daemon updates", () => {
    const initVm = goldenView(machineSettingsEditGolden as GoldenDoc, "initialLoaded");
    expect(initVm.view.case).toBe("machineSettings");
    if (initVm.view.case !== "machineSettings") return;

    const onSelectAgent = vi.fn();
    const onUpdateDefaults = vi.fn();
    const onUpdateDaemon = vi.fn();
    const onRetry = vi.fn();
    const renderMS = (v: typeof initVm.view.value) => (
      <MachineSettingsScreenView id={v.machineId} vm={v} go={vi.fn()} onBack={vi.fn()}
        onRetrySettings={onRetry} onSelectAgent={onSelectAgent} onUpdateAgentDefaults={onUpdateDefaults} onUpdateDaemonSettings={onUpdateDaemon} />
    );

    mount(renderMS(initVm.view.value));
    expect(host.textContent).toContain(initVm.view.value.name);
    const claudeRow = [...host.querySelectorAll<HTMLButtonElement>(".cli-row")].find((b) =>
      b.textContent?.includes("Claude Code"),
    )!;
    expect(claudeRow).toBeTruthy();
    act(() => claudeRow.click());
    expect(document.querySelector(".sheet")?.textContent).toContain(t("settings.cli_sheet_model"));
    const modeRow = [...document.querySelectorAll<HTMLButtonElement>(".sheet button.row")].find((b) =>
      b.textContent?.includes(t("settings.cli_sheet_mode")),
    )!;
    act(() => modeRow.click());
    const firstChoice = document.querySelector<HTMLButtonElement>(".sheet [role='radio']");
    if (firstChoice) {
      act(() => firstChoice.click());
      expect(onUpdateDefaults).toHaveBeenCalledWith(Agent.CLAUDE, expect.any(Object));
    }
    const doneBtn = [...document.querySelectorAll<HTMLButtonElement>(".sheet .sheet-head button")].find(
      (b) => b.textContent === t("common.done"),
    )!;
    act(() => doneBtn.click());

    const timeoutBtn = [...host.querySelectorAll<HTMLButtonElement>("button.row")].find((b) =>
      b.textContent?.includes(t("settings.approval_timeout")),
    )!;
    act(() => timeoutBtn.click());
    const opt = document.querySelector<HTMLButtonElement>(".sheet .list .mi");
    expect(opt).toBeTruthy();
    act(() => opt!.click());
    expect(onUpdateDaemon).toHaveBeenCalledTimes(1);

    const savingVm = goldenView(machineSettingsEditGolden as GoldenDoc, "savingClaudeAndClean");
    expect(savingVm.view.case).toBe("machineSettings");
    if (savingVm.view.case !== "machineSettings") return;
    const savingView = savingVm.view.value;
    act(() => root!.render(renderMS(savingView)));
    expect(host.textContent).toContain(savingView.name);

    const unknownVm = goldenView(machineSettingsEditGolden as GoldenDoc, "unknownTriggersReread");
    expect(unknownVm.view.case).toBe("machineSettings");
    if (unknownVm.view.case !== "machineSettings") return;
    const unknownView = unknownVm.view.value;
    expect(unknownView.saveUnknown).toBe(true);
    expect(unknownView.settingsLoading).toBe(true);
    act(() => root!.render(renderMS(unknownView)));
    expect(host.textContent).toContain(unknownView.name);

    const failedVm = goldenView(machineSettingsEditGolden as GoldenDoc, "relayFailedDiag");
    expect(failedVm.view.case).toBe("machineSettings");
    if (failedVm.view.case !== "machineSettings") return;
    const failedView = failedVm.view.value;
    act(() => root!.render(renderMS(failedView)));
    expect(failedView.connection).toBeDefined();
    expect(host.textContent).toContain(failedView.name);
  });

  it("renders machine_status_lifecycle.golden.json states and dispatches tab/cleanup/retry", () => {
    const loadVm = goldenView(machineStatusLifecycleGolden as GoldenDoc, "loading");
    expect(loadVm.view.case).toBe("machineStatus");
    if (loadVm.view.case !== "machineStatus") return;

    const onRetry = vi.fn();
    const onSelectTab = vi.fn();
    const onScrub = vi.fn();
    const onCleanup = vi.fn();
    const renderStatus = (v: typeof loadVm.view.value) => (
      <MachineStatusScreenView id={v.machineId} vm={v} reduced go={vi.fn()} onBack={vi.fn()}
        onRetry={onRetry} onSelectTab={onSelectTab} onScrub={onScrub} onCleanup={onCleanup} />
    );

    mount(renderStatus(loadVm.view.value));
    expect(host.querySelector(".ms-title")?.textContent).toBe(loadVm.view.value.machineName);
    expect(host.querySelector(".ms-live .ms-dot.idle")).toBeTruthy();
    expect(host.querySelector(".ms-live")?.textContent).toContain(t("machine_status.loading"));

    const critVm = goldenView(machineStatusLifecycleGolden as GoldenDoc, "liveCriticalDisk");
    expect(critVm.view.case).toBe("machineStatus");
    if (critVm.view.case !== "machineStatus") return;
    const critView = critVm.view.value;
    act(() => root!.render(renderStatus(critView)));
    expect(host.querySelector(".ms-hero")).toBeTruthy();
    const cleanupBtn = [...host.querySelectorAll<HTMLButtonElement>(".ms-rows button")].find((b) =>
      b.textContent?.includes(t("machine_status.cleanup_action")),
    )!;
    expect(cleanupBtn).toBeTruthy();
    act(() => cleanupBtn.click());
    expect(onCleanup).toHaveBeenCalledTimes(1);

    const procVm = goldenView(machineStatusLifecycleGolden as GoldenDoc, "processesAndScrub");
    expect(procVm.view.case).toBe("machineStatus");
    if (procVm.view.case !== "machineStatus") return;
    const procView = procVm.view.value;
    act(() => root!.render(renderStatus(procView)));
    const tabBtn = [...host.querySelectorAll<HTMLButtonElement>(".ms-sec button.link")][0];
    expect(tabBtn.textContent).toBe(t("machine_status.show_sessions"));
    act(() => tabBtn.click());
    expect(onSelectTab).toHaveBeenCalledWith(MachineUsageTab.SESSIONS);

    const staleVm = goldenView(machineStatusLifecycleGolden as GoldenDoc, "staleAfter3Timeouts");
    expect(staleVm.view.case).toBe("machineStatus");
    if (staleVm.view.case !== "machineStatus") return;
    const staleView = staleVm.view.value;
    expect(staleView.pollState).toBe(MachinePollState.STALE);
    act(() => root!.render(renderStatus(staleView)));
    expect(host.querySelector(".ms-page.is-stale")).toBeTruthy();
    expect(host.querySelector(".ms-live")?.textContent).toContain("46");
    act(() => root!.render(renderStatus({ ...staleView, showRetry: true })));
    const retryBtn = host.querySelector<HTMLButtonElement>(".ms-live button.link")!;
    expect(retryBtn).toBeTruthy();
    act(() => retryBtn.click());
    expect(onRetry).toHaveBeenCalledTimes(1);

    const recVm = goldenView(machineStatusLifecycleGolden as GoldenDoc, "recoveredFreshCurve");
    expect(recVm.view.case).toBe("machineStatus");
    if (recVm.view.case !== "machineStatus") return;
    const recView = recVm.view.value;
    act(() => root!.render(renderStatus(recView)));
    expect(host.querySelector(".ms-page.is-stale")).toBeNull();
  });

  it("renders machine_quota.golden.json sections cleanly", () => {
    const allVm = goldenView(machineQuotaGolden as GoldenDoc, "allQuotas");
    expect(allVm.view.case).toBe("quota");
    if (allVm.view.case !== "quota") return;
    mount(<QuotaScreenView vm={allVm.view.value} onBack={vi.fn()} />);
    expect(host.textContent).toContain("Codex");
    expect(host.textContent).toContain("15%");
    expect(host.textContent).not.toContain("%%");
    expect(host.querySelectorAll(".bar i").length).toBeGreaterThan(0);

    const codexVm = goldenView(machineQuotaGolden as GoldenDoc, "codexOnly");
    expect(codexVm.view.case).toBe("quota");
    if (codexVm.view.case !== "quota") return;
    const codexView = codexVm.view.value;
    act(() => root!.render(<QuotaScreenView vm={codexView} onBack={vi.fn()} />));
    expect([...host.querySelectorAll(".label")].map((e) => e.textContent)).not.toContain("Claude Code");
  });

  it("renders session_shared_files_images.golden.json shared files, image chips, and viewer navigation", () => {
    const initialVm = goldenView(sessionSharedFilesImagesGolden as GoldenDoc, "session_initial");
    expect(initialVm.view.case).toBe("session");
    if (initialVm.view.case !== "session") return;

    const onOpenImageViewer = vi.fn();
    const onNavigateImageViewer = vi.fn();
    const onCloseImageViewer = vi.fn();
    const renderSession = (vm: SessionViewModel) => (
      <SessionScreenView
        vm={vm}
        wide={false}
        onBack={vi.fn()}
        draft=""
        onDraft={vi.fn()}
        onSend={vi.fn()}
        onSubmitCommand={vi.fn()}
        onAbandonCommand={vi.fn()}
        onLoadEarlier={vi.fn()}
        onExpandGroup={vi.fn()}
        onOpenImageViewer={onOpenImageViewer}
        onNavigateImageViewer={onNavigateImageViewer}
        onCloseImageViewer={onCloseImageViewer}
      />
    );

    mount(renderSession(initialVm.view.value));
    expect(host.querySelectorAll(".sf-fc").length).toBeGreaterThan(0);
    expect(host.querySelectorAll(".ichip").length).toBeGreaterThan(0);
    const pic = host.querySelector<HTMLButtonElement>(".pic")!;
    expect(pic).toBeTruthy();
    act(() => pic.click());
    expect(onOpenImageViewer).toHaveBeenCalledTimes(1);

    const openVm = goldenView(sessionSharedFilesImagesGolden as GoldenDoc, "session_en_viewer_open");
    expect(openVm.view.case).toBe("session");
    if (openVm.view.case !== "session") return;
    const openView = openVm.view.value;
    act(() => root!.render(renderSession(openView)));
    const viewer = host.querySelector<HTMLElement>(".viewer")!;
    expect(viewer).toBeTruthy();
    expect(viewer.textContent).toContain("3 / 4");
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" })));
    expect(onNavigateImageViewer).toHaveBeenCalledWith(1);
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" })));
    expect(onNavigateImageViewer).toHaveBeenCalledWith(-1);
    const closeBtn = viewer.querySelector<HTMLButtonElement>(`button[aria-label="${t("common.close")}"]`)!;
    act(() => closeBtn.click());
    expect(onCloseImageViewer).toHaveBeenCalledTimes(1);

    const closedVm = goldenView(sessionSharedFilesImagesGolden as GoldenDoc, "session_viewer_closed");
    expect(closedVm.view.case).toBe("session");
    if (closedVm.view.case !== "session") return;
    const closedView = closedVm.view.value;
    act(() => root!.render(renderSession(closedView)));
    expect(host.querySelector(".viewer")).toBeNull();
  });

  it("renders session_attachments_draft.golden.json draft preparing, ready, viewer, and post-send session", () => {
    const preparingVm = goldenView(sessionAttachmentsDraftGolden as GoldenDoc, "draft_preparing");
    expect(preparingVm.view.case).toBe("attachmentDraft");
    if (preparingVm.view.case !== "attachmentDraft") return;
    mount(<TrayView draft={preparingVm.view.value.draft} />);
    expect(host.querySelector(".att-tray .ring.spin")).toBeTruthy();
    expect(host.querySelector(".att-meta")?.textContent).toContain("1 / 6");

    const readyVm = goldenView(sessionAttachmentsDraftGolden as GoldenDoc, "draft_ready");
    expect(readyVm.view.case).toBe("attachmentDraft");
    if (readyVm.view.case !== "attachmentDraft") return;
    const readyDraft = readyVm.view.value.draft;
    const onOpenViewer = vi.fn();
    const onSetOriginal = vi.fn();
    act(() =>
      root!.render(
        <TrayView
          draft={readyDraft}
          onOpenViewer={onOpenViewer}
          onSetOriginal={onSetOriginal}
        />,
      ),
    );
    expect(host.querySelector(".att-tray .ring.spin")).toBeNull();
    act(() => host.querySelector<HTMLButtonElement>(".draft-open")!.click());
    expect(onOpenViewer).toHaveBeenCalledWith(0);
    act(() => host.querySelector<HTMLButtonElement>(".orig")!.click());
    expect(onSetOriginal).toHaveBeenCalledWith(true);

    const viewerVm = goldenView(sessionAttachmentsDraftGolden as GoldenDoc, "draft_viewer_open");
    expect(viewerVm.view.case).toBe("attachmentDraft");
    if (viewerVm.view.case !== "attachmentDraft") return;
    const viewerDraft = viewerVm.view.value.draft;
    const onCloseViewer = vi.fn();
    act(() =>
      root!.render(
        <TrayView draft={viewerDraft} onCloseViewer={onCloseViewer} />,
      ),
    );
    const dialog = document.querySelector<HTMLElement>(".viewer, .lightbox");
    expect(dialog).toBeTruthy();
    expect(dialog?.textContent).toContain("1 / 1");
    const closeBtn = dialog!.querySelector<HTMLButtonElement>(`button[aria-label="${t("common.close")}"]`)!;
    act(() => closeBtn.click());
    expect(onCloseViewer).toHaveBeenCalledTimes(1);

    const afterSendVm = sessionGoldenView(sessionAttachmentsDraftGolden as GoldenDoc, "session_after_send");
    renderSessionGolden(afterSendVm);
    expect(host.querySelector(".ptray")?.textContent).toContain("review screenshot");
  });
});
