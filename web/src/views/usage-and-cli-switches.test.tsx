// @vitest-environment happy-dom
// End-to-end Web UI & ViewModel coverage for Phase 2: CLI switches and passive Usage & Output Speed.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  buildUsageOpenParams,
  clampUsageCustomRange,
  getCoreHost,
  localTzOffsetMinutes,
  routeForUsage,
  UsageRangePreset,
  UsageSortColumn,
} from "../core/host";
import { DemoRemote } from "../dev/demo";
import { NewSessionNoticeViewSchema, NoticeActionKind, NoticeKind, NoticeSeverity, NewSessionViewModelSchema } from "../gen/remoteagent/core/v1/new_session_view_pb";
import { ScheduleDetailViewModelSchema } from "../gen/remoteagent/core/v1/schedules_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import {
  SessionActionsViewSchema,
  SessionHeaderViewSchema,
  SessionUsageEntryViewSchema,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { localeStore, t as message } from "../i18n";
import { SessionSheet } from "../sheets";
import { MachineStatusPage } from "./MachineStatus";
import { NewBoxView } from "./NewSession";
import { ScheduleDetailView, type ScheduleDetailActions } from "./Schedules";
import { MachinePage } from "./Settings";
import { UsagePage } from "./Usage";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let hostEl: HTMLElement;
const remotes: DemoRemote[] = [];

beforeEach(() => {
  localStorage.clear();
  localeStore.overrideForTesting("zh-CN");
  vi.stubGlobal("RTCPeerConnection", class {});
});

afterEach(() => {
  act(() => root?.unmount());
  hostEl?.remove();
  root = undefined;
  for (const r of remotes.splice(0)) r.dispose();
  localeStore.overrideForTesting(undefined);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  localStorage.clear();
});

function createDemo(query = ""): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
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
  hostEl = document.body.appendChild(document.createElement("div"));
  root = createRoot(hostEl);
  act(() => root!.render(node));
  await flushUi();
}

describe("Phase 2 bounds", () => {
  it("clamps custom date ranges to 90 days, uses 28 6-hour buckets for 7d, and reports east-of-UTC offset sign", () => {
    const now = new Date(2026, 10, 18, 12, 0, 0).getTime();
    const veryOld = new Date(2026, 0, 1).getTime();
    const clamped = clampUsageCustomRange(BigInt(veryOld), BigInt(now + 10 * 86_400_000), now);
    const minDay = new Date(2026, 10, 18 - 89).getTime();
    const todayStart = new Date(2026, 10, 18).getTime();
    expect(Number(clamped.startMs)).toBe(minDay);
    expect(Number(clamped.endMs)).toBe(todayStart);
    expect(localTzOffsetMinutes(now)).toBe(-new Date(now).getTimezoneOffset());

    const open7d = buildUsageOpenParams(UsageRangePreset.SEVEN_DAYS, 0n, 0n, now);
    expect(open7d.bucketBoundsMs).toHaveLength(29);
    const open30d = buildUsageOpenParams(UsageRangePreset.THIRTY_DAYS, 0n, 0n, now);
    expect(open30d.bucketBoundsMs).toHaveLength(31);
  });
});

const cliRow = (name: string) =>
  [...hostEl.querySelectorAll<HTMLButtonElement>(".cli-row")].find(
    (b) => b.querySelector(".row-name b")?.textContent === name,
  )!;
const closeSheet = async () => {
  const done = [...document.querySelectorAll<HTMLButtonElement>(".sheet .sheet-head button")].find(
    (b) => b.textContent === message("common.done"),
  )!;
  await act(async () => done.click());
  await flushUi();
};

describe("Per-machine CLI switches and entry points", () => {
  it("opens CLI sheet with accessible switches, toggles agents, and locks the last remaining CLI", async () => {
    const r = createDemo("cliswitches=normal");
    await mountAsync(<MachinePage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);

    // Disable Antigravity in its CLI sheet
    await act(async () => cliRow("Antigravity").click());
    await flushUi();
    let sw = document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    await act(async () => sw.click());
    await flushUi();
    sw = document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(document.querySelector(".sheet")!.textContent).toContain(message("settings.cli_disabled_explanation"));
    await closeSheet();

    // Disable Claude -> Codex becomes locked last enabled
    await act(async () => cliRow("Claude Code").click());
    await flushUi();
    await act(async () => document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!.click());
    await flushUi();
    await closeSheet();

    await act(async () => cliRow("Codex").click());
    await flushUi();
    const codexSw = document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!;
    expect(codexSw.disabled).toBe(true);
    expect(codexSw.getAttribute("aria-checked")).toBe("true");
    expect(codexSw.textContent).toContain(message("settings.cli_switches_keep_one"));
    expect(codexSw.getAttribute("aria-label")).toContain(message("settings.cli_switches_keep_one"));
    expect(codexSw.querySelector(".sw.locked")).toBeTruthy();
  });

  it("shows offline, lease-expired, and conflict states and falls back cleanly on legacy machines", async () => {
    const rOffline = createDemo("cliswitches=offline");
    await mountAsync(<MachinePage remote={rOffline} id="mac" go={() => undefined} onBack={() => undefined} />);
    await act(async () => cliRow("Codex").click());
    await flushUi();
    expect(document.querySelector(".sheet")!.textContent).toContain(
      message("settings.cli_switches_offline_reason", { name: "MacBook" }),
    );
    expect(document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!.disabled).toBe(true);
    await closeSheet();

    act(() => root?.unmount());
    hostEl.remove();

    const rConflict = createDemo("cliswitches=conflict");
    await mountAsync(<MachinePage remote={rConflict} id="mac" go={() => undefined} onBack={() => undefined} />);
    await act(async () => cliRow("Codex").click());
    await flushUi();
    await act(async () => document.querySelector<HTMLButtonElement>('.sheet [role="switch"]')!.click());
    await flushUi();
    expect(document.querySelector(".sheet")!.textContent).toContain(message("settings.cli_switches_conflict"));
    await closeSheet();

    act(() => root?.unmount());
    hostEl.remove();

    // VPS has cliSwitches=false -> opens CLI sheet with readonly version and no enable switch
    await mountAsync(<MachinePage remote={rConflict} id="vps" go={() => undefined} onBack={() => undefined} />);
    await act(async () => cliRow("Codex").click());
    await flushUi();
    expect(document.querySelectorAll('.sheet [role="switch"]')).toHaveLength(0);
    expect(document.querySelector(".sheet")!.textContent).toContain(message("settings.cli_installed_version"));
    expect(document.querySelector(".sheet")!.textContent).toContain("Codex");
  });

  it("routes ENABLE_AGENT in NewBoxView and enable_cli in ScheduleDetailView to machine settings", async () => {
    const opened: Array<[string, Agent | undefined]> = [];
    const vm = create(NewSessionViewModelSchema, {
      projectKey: "p1",
      notices: [
        create(NewSessionNoticeViewSchema, {
          kind: NoticeKind.AGENT_DISABLED_FALLBACK,
          severity: NoticeSeverity.INFO,
          action: NoticeActionKind.ENABLE_AGENT,
          targetMachineId: "mac",
          targetAgent: Agent.CLAUDE,
          text: create(TextSchema, { literal: "disabled fallback" }),
        }),
      ],
    });
    await mountAsync(
      <NewBoxView
        vm={vm}
        onSelectMachine={() => undefined}
        onSelectAgent={() => undefined}
        onSelectModel={() => undefined}
        onRetryModels={() => undefined}
        onRetrySaveConfig={() => undefined}
        onSetPrompt={() => undefined}
        onSubmit={() => undefined}
        onAbandonStart={() => undefined}
        onOpenMachineSettings={(id, agent) => opened.push([id, agent])}
        onOpenDiskUsage={() => undefined}
        onReaddProject={() => undefined}
      />,
    );
    const btn = [...hostEl.querySelectorAll("button")].find((b) => b.textContent?.includes(message("new_session.enable_agent")));
    expect(btn).toBeTruthy();
    btn!.click();
    expect(opened).toEqual([["mac", Agent.CLAUDE]]);

    act(() => root?.unmount());
    hostEl.remove();

    let schedOpenedAgent: Agent | undefined;
    let schedOpened = false;
    const schedVm = create(ScheduleDetailViewModelSchema, {
      found: true,
      machineId: "mac",
      taskId: "t1",
      title: "nightly",
      attention: {
        show: false,
        showEnableCliAction: true,
        disabledAgent: Agent.CLAUDE,
        message: create(TextSchema, {
          key: "schedule.agent_disabled_pre_run",
          args: [
            { name: "machine", value: { case: "strValue", value: "MacBook" } },
            { name: "agent", value: { case: "strValue", value: "Claude Code" } },
          ],
        }),
      },
    });
    const actions: ScheduleDetailActions = {
      onBack: () => undefined,
      onOperate: () => undefined,
      onMarkRead: () => undefined,
      onMarkUnread: () => undefined,
      onOpenSession: () => undefined,
      onShowLatestResult: () => undefined,
      onToggleQuietGroup: () => undefined,
      onShowAllRuns: () => undefined,
      onOpenRun: () => undefined,
      onSetNotify: () => undefined,
      onRetryMachine: () => undefined,
      onOpenEdit: () => undefined,
      onRetryMutation: () => undefined,
      onAbandonMutation: () => undefined,
      onOpenMachineSettings: (agent) => {
        schedOpened = true;
        schedOpenedAgent = agent;
      },
    };
    await mountAsync(<ScheduleDetailView vm={schedVm} actions={actions} />);
    const enableCliBtn = [...hostEl.querySelectorAll("button")].find((b) => b.textContent?.includes(message("schedule.enable_cli")));
    expect(enableCliBtn).toBeTruthy();
    enableCliBtn!.click();
    expect(schedOpened).toBe(true);
    expect(schedOpenedAgent).toBe(Agent.CLAUDE);
  });

  it("renders usage entries on MachineStatusPage and SessionSheet and navigates to u/<machineId>", async () => {
    const r = createDemo("usage=normal");
    const routes: string[] = [];
    await mountAsync(<MachineStatusPage remote={r} id="mac" go={(route) => routes.push(route)} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("machine_status.model_usage_title"));
    const trendsBtn = [...hostEl.querySelectorAll("button")].find((b) => b.textContent === message("machine_status.model_usage_view_trends"));
    expect(trendsBtn).toBeTruthy();
    trendsBtn!.click();
    expect(routes).toContain("u/mac");

    act(() => root?.unmount());
    hostEl.remove();

    let sheetUsageOpened = false;
    await mountAsync(
      <SessionSheet
        header={create(SessionHeaderViewSchema, { machineId: "mac", agent: Agent.CODEX, agentName: "Codex", model: "gpt-6-astra" })}
        actions={create(SessionActionsViewSchema, {
          usageEntry: create(SessionUsageEntryViewSchema, {
            visible: true,
            supported: true,
            hasData: true,
            subtitle: create(TextSchema, { literal: "Codex · gpt-6-astra · 近 7 天" }),
            valueText: create(TextSchema, { literal: "118 tok/s" }),
          }),
        })}
        onClose={() => undefined}
        onRefreshModels={() => undefined}
        onSetModel={() => undefined}
        onCompact={() => undefined}
        onOpenUsage={() => {
          sheetUsageOpened = true;
        }}
      />,
    );
    const entryBtn = hostEl.querySelector<HTMLButtonElement>(".session-usage-entry button");
    expect(entryBtn).toBeTruthy();
    expect(entryBtn!.getAttribute("aria-label")).toContain(message("session.usage_entry_title"));
    entryBtn!.click();
    expect(sheetUsageOpened).toBe(true);
  });
});

describe("Usage & Output Speed screen", () => {
  it("renders all 4 metrics simultaneously, composition, sorting, single-model focus, scrub, and en formatting", async () => {
    const r = createDemo("usage=normal");
    await mountAsync(<UsagePage remote={r} machineId="mac" wide={true} go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);

    // 4 stacked charts (1 bar chart + 3 strips), composition rows, and 6-row table rendered simultaneously
    expect(hostEl.querySelectorAll(".u-bar").length).toBeGreaterThan(0);
    expect(hostEl.querySelectorAll(".u-strip")).toHaveLength(3);
    expect(hostEl.querySelector(".u-comp")).toBeTruthy();
    expect(hostEl.querySelector(".u-comp-row.in2")?.textContent).toContain(message("usage.comp_reasoning_label"));
    expect(hostEl.querySelectorAll(".u-trow")).toHaveLength(6);

    // Row without speed samples renders en-dash
    expect(hostEl.querySelector(".u-l2 .na")?.textContent).toBe("–");

    // Click TTFT column header -> sorts ascending (fastest first, missing last)
    const ttftBtn = [...hostEl.querySelectorAll<HTMLButtonElement>(".u-thead button")].find((b) =>
      b.textContent?.includes(message("usage.col_ttft")),
    )!;
    await act(async () => ttftBtn.click());
    await flushUi();
    expect(ttftBtn.textContent).toBe(`${message("usage.col_ttft")} ↑`);
    const sortedModels = [...hostEl.querySelectorAll(".u-trow .nm")].map((n) => n.textContent);
    expect(sortedModels[0]).toBe("gpt-5.4-mini");
    expect(sortedModels[sortedModels.length - 1]).toBe("gpt-5.4-codex-mini");

    // Click a row -> selects single model (ghost bars, back sparkline, × button in scope)
    const periodBigBeforeFocus = hostEl.querySelector(".u-big")?.textContent;
    const targetRow = [...hostEl.querySelectorAll<HTMLButtonElement>(".u-trow")].find((b) =>
      b.textContent?.includes("claude-opus-5-5"),
    )!;
    await act(async () => targetRow.click());
    await flushUi();
    expect(hostEl.querySelector(".u-scope .u-x")).toBeTruthy();
    expect(hostEl.querySelectorAll(".u-bar.ghost").length).toBeGreaterThan(0);
    expect(hostEl.querySelectorAll(".u-ln.back").length).toBeGreaterThan(0);
    expect(hostEl.querySelector(".u-big")?.textContent).not.toBe(periodBigBeforeFocus);

    // Keyboard scrub with ArrowRight then Escape
    const chartsEl = hostEl.querySelector<HTMLDivElement>(".u-charts")!;
    await act(async () => {
      chartsEl.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    });
    await flushUi();
    expect(hostEl.querySelector(".u-cursor")).toBeTruthy();
    expect(hostEl.querySelector(".u-sub")?.textContent).toBe("");

    await act(async () => {
      chartsEl.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await flushUi();
    expect(hostEl.querySelector(".u-cursor")).toBeNull();
    expect(hostEl.querySelector(".u-sub")?.textContent).toContain("次");

    // Clear selected model via × button
    await act(async () => hostEl.querySelector<HTMLButtonElement>(".u-scope .u-x")!.click());
    await flushUi();
    expect(hostEl.querySelector(".u-scope .u-x")).toBeNull();
    expect(hostEl.querySelector(".u-big")?.textContent).toBe(periodBigBeforeFocus);

    // Switch locale to en -> k/M/B units and English strip labels
    await act(async () => localeStore.overrideForTesting("en"));
    await flushUi();
    expect(hostEl.textContent).toContain("Cache hit");
    expect(hostEl.textContent).toContain("Output speed");
    expect(hostEl.textContent).toMatch(/\d+(?:\.\d+)?[kMB]/);
  });

  it("handles 20-model crowd, empty state, and all edge states", async () => {
    const rCrowd = createDemo("usage=crowd");
    await mountAsync(<UsagePage remote={rCrowd} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.querySelectorAll(".u-trow")).toHaveLength(20);
    expect(hostEl.querySelector(".u-big")?.textContent).toContain("亿");

    act(() => root?.unmount());
    hostEl.remove();

    const rEmpty = createDemo("usage=empty");
    await mountAsync(<UsagePage remote={rEmpty} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.empty_no_records"));
    expect(hostEl.textContent).toContain(message("usage.scope_all_empty"));
    expect(hostEl.querySelectorAll(".u-trow")).toHaveLength(0);
    expect(hostEl.querySelectorAll(".u-strip")).toHaveLength(0);
    expect(hostEl.querySelector(".u-axis")).toBeTruthy();

    act(() => root?.unmount());
    hostEl.remove();

    const rOld = createDemo("usage=old");
    await mountAsync(<UsagePage remote={rOld} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.unsupported_title"));

    act(() => root?.unmount());
    hostEl.remove();

    const rUnavail = createDemo("usage=only-unavail");
    await mountAsync(<UsagePage remote={rUnavail} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.only_unavailable_title", { agents: "Antigravity" }));

    act(() => root?.unmount());
    hostEl.remove();

    const rErr = createDemo("usage=error");
    await mountAsync(<UsagePage remote={rErr} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.load_error_title"));
    expect([...hostEl.querySelectorAll("button")].some((b) => b.textContent === message("common.retry"))).toBe(true);

    act(() => root?.unmount());
    hostEl.remove();

    const rOffline = createDemo("usage=offline");
    await mountAsync(<UsagePage remote={rOffline} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain("gpt-6-astra");
    expect(hostEl.textContent).toContain(message("usage.subtitle_offline", { name: "MacBook" }));

    act(() => root?.unmount());
    hostEl.remove();

    const rOfflineNone = createDemo("usage=offline-none");
    await mountAsync(<UsagePage remote={rOfflineNone} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.offline_empty_title", { name: "MacBook" }));

    // Sanity check CoreHost usage exports
    expect(routeForUsage("mac", Agent.CODEX, "gpt-6")).toBe("u/mac/1/gpt-6");
    expect(UsageSortColumn.TOKENS).toBe(1);
    expect(UsageRangePreset.SEVEN_DAYS).toBe(2);
    expect(getCoreHost(rOfflineNone)).toBeTruthy();

    act(() => root?.unmount());
    hostEl.remove();

    await mountAsync(
      <SessionSheet
        header={create(SessionHeaderViewSchema, { machineId: "mac", agent: Agent.ANTIGRAVITY, agentName: "Antigravity", model: "gemini-4-hydrogen" })}
        actions={create(SessionActionsViewSchema, {
          usageEntry: create(SessionUsageEntryViewSchema, {
            visible: true,
            supported: true,
            hasData: true,
            totalTokens: 110_000n,
            hasCacheHit: true,
            cacheHitRate: 0.7,
            hasSpeed: false,
            valueText: create(TextSchema, { literal: "11万" }),
            subtitle: create(TextSchema, {
              key: "usage.entry_no_speed",
              args: [
                {
                  name: "detail",
                  value: {
                    case: "strValue",
                    value: `${message("usage.entry_last_7d")} · ${message("usage.entry_cache_pct", { pct: "70%" })}`,
                  },
                },
              ],
            }),
          }),
        })}
        onClose={() => undefined}
        onRefreshModels={() => undefined}
        onSetModel={() => undefined}
        onCompact={() => undefined}
        onOpenUsage={() => undefined}
      />,
    );
    expect(hostEl.textContent).toContain("11万");
    expect(hostEl.textContent).toContain(
      message("usage.entry_no_speed", {
        detail: `${message("usage.entry_last_7d")} · ${message("usage.entry_cache_pct", { pct: "70%" })}`,
      }),
    );
  });
});
