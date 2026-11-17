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
import { ViewModelSchema } from "../gen/remoteagent/core/v1/app_pb";
import { localeStore, t } from "../i18n";
import { HomeView, SidebarView } from "./Home";
import { NewBoxView } from "./NewSession";
import { ManageProjectsScreen, RemovedNotice } from "./Projects";

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
