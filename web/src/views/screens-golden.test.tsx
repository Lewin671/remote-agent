// @vitest-environment happy-dom
// Golden ViewModel layout and interaction tests rendered directly from
// testdata/screens/*.golden.json without TS rules or Remote.

import { fromJson } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import homeTimelineGolden from "../../../testdata/screens/home_timeline.golden.json";
import homeEmptyGolden from "../../../testdata/screens/home_empty_errors.golden.json";
import projectManageGolden from "../../../testdata/screens/project_manage_detail.golden.json";
import newSessionGolden from "../../../testdata/screens/new_session_lifecycle.golden.json";
import sessionApprovalGolden from "../../../testdata/screens/session_approval_flow.golden.json";
import sessionCliErrorGolden from "../../../testdata/screens/session_cli_error_turn.golden.json";
import sessionLongWindowGolden from "../../../testdata/screens/session_long_window.golden.json";
import sessionQuestionPlanGolden from "../../../testdata/screens/session_question_plan.golden.json";
import sessionQueueRetryGolden from "../../../testdata/screens/session_queue_retry.golden.json";
import sessionScheduledFoldGolden from "../../../testdata/screens/session_scheduled_fold.golden.json";
import schedulesInboxDetailGolden from "../../../testdata/screens/schedules_inbox_detail.golden.json";
import schedulesFormLifecycleGolden from "../../../testdata/screens/schedules_form_lifecycle.golden.json";
import { ViewModelSchema } from "../gen/remoteagent/core/v1/app_pb";
import { ScheduleFormMode, ScheduleOperationKind } from "../gen/remoteagent/core/v1/schedules_view_pb";
import type { SessionViewModel } from "../gen/remoteagent/core/v1/session_view_pb";
import { localeStore, t } from "../i18n";
import { HomeView, SidebarView } from "./Home";
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

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface GoldenStep {
  views?: Record<string, unknown>;
}
interface GoldenDoc {
  steps: GoldenStep[];
}

function goldenView(doc: GoldenDoc, viewName: string) {
  for (const step of doc.steps) {
    const raw = step.views?.[viewName];
    if (raw) {
      return fromJson(ViewModelSchema, raw as never);
    }
  }
  throw new Error(`Missing golden view: ${viewName}`);
}

let root: Root | undefined;
let host: HTMLDivElement;

function mount(node: React.ReactNode) {
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

  it("renders empty and error Home states from home_empty_errors.golden.json", () => {
    const firstHomeStep = (homeEmptyGolden as GoldenDoc).steps.find((s) =>
      Object.values(s.views ?? {}).some((v) => (v as { home?: unknown }).home),
    );
    expect(firstHomeStep?.views).toBeTruthy();
    const viewName = Object.keys(firstHomeStep!.views!).find(
      (k) => (firstHomeStep!.views![k] as { home?: unknown }).home,
    )!;
    const decoded = fromJson(ViewModelSchema, firstHomeStep!.views![viewName] as never);
    if (decoded.view.case !== "home") throw new Error("expected home view");

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
    expect(onSetPrompt).toHaveBeenCalledWith("Implement feature", 0, false);
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
    onDismissOutgoing: vi.fn(),
    onRestoreOutgoing: vi.fn(),
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

  it("renders failed/unknown outgoing bubbles and pending queue from session_queue_retry.golden.json", () => {
    const onSubmitCommand = vi.fn();
    const onRestoreOutgoing = vi.fn();
    const onDismissOutgoing = vi.fn();

    const bothVm = sessionGoldenView(sessionQueueRetryGolden as GoldenDoc, "outgoingFailedAndUnknown");
    renderSessionGolden(bothVm, { onSubmitCommand, onRestoreOutgoing, onDismissOutgoing });

    expect(host.textContent).toContain("steer failed msg");
    expect(host.textContent).toContain("queued second");
    expect(host.querySelector(".me.fail")).toBeTruthy();
    expect(host.querySelector(".me.warn")).toBeTruthy();
    expect(host.textContent).toContain(t("errors.disconnected_from_server"));
    expect(host.textContent).toContain(t("errors.the_computer_s_reply_could_not_be_verified_command"));

    const restoreBtn = host.querySelector<HTMLButtonElement>(".me.fail small button")!;
    act(() => restoreBtn.click());
    expect(onRestoreOutgoing).toHaveBeenCalledWith("msg-fail");

    const dismissBtn = [...host.querySelectorAll<HTMLButtonElement>(".me.fail small button")].find((b) =>
      b.textContent?.includes(t("session.dismiss_notice")),
    )!;
    act(() => dismissBtn.click());
    expect(onDismissOutgoing).toHaveBeenCalledWith("msg-fail");

    const queuedVm = sessionGoldenView(sessionQueueRetryGolden as GoldenDoc, "queuedPendingAndFailedOutgoing");
    renderSessionGolden(queuedVm, { onSubmitCommand, onRestoreOutgoing, onDismissOutgoing });
    expect(host.querySelector(".pending-bubble")?.textContent).toContain("queued second");
    expect(host.querySelector(".pending-bubble")?.textContent).toContain(t("queue.after_turn"));
    const cancelQueueBtn = [...host.querySelectorAll<HTMLButtonElement>(".pending-bubble button")].find((b) =>
      b.textContent?.includes(t("common.cancel")),
    )!;
    act(() => cancelQueueBtn.click());
    expect(onSubmitCommand).toHaveBeenCalledWith(
      "cancel_input:s-qr:msg-q",
      expect.objectContaining({
        kind: expect.objectContaining({ case: "cancelInput", value: expect.objectContaining({ sessionId: "s-qr", requestId: "msg-q" }) }),
      }),
    );
  });

  it("renders folded quiet scheduled runs and dispatches onExpandGroup from session_scheduled_fold.golden.json", () => {
    const onExpandGroup = vi.fn();
    const foldedVm = sessionGoldenView(sessionScheduledFoldGolden as GoldenDoc, "foldedQuietRuns");
    renderSessionGolden(foldedVm, { onExpandGroup });

    const foldBtn = host.querySelector<HTMLButtonElement>("button.schedule-quiet")!;
    expect(foldBtn).toBeTruthy();
    expect(host.textContent).toContain("Alert: disk usage 95%!");
    expect(host.textContent).toContain("Disk warning sent");
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
});
