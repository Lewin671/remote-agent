// @vitest-environment happy-dom
// End-to-end Web UI & ViewModel coverage for Phase 2: CLI switches and passive Usage & Output Speed.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clampUsageCustomRange, getCoreHost, localTzOffsetMinutes, routeForUsage, UsageMetricTab, UsageRangePreset } from "../core/host";
import { DemoRemote } from "../dev/demo";
import { NewSessionNoticeViewSchema, NoticeActionKind, NoticeKind, NoticeSeverity, NewSessionViewModelSchema } from "../gen/remoteagent/core/v1/new_session_view_pb";
import { ScheduleDetailViewModelSchema } from "../gen/remoteagent/core/v1/schedules_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { UsageSeriesViewSchema } from "../gen/remoteagent/core/v1/usage_view_pb";
import { UsagePeriodSummarySchema } from "../gen/remoteagent/v1/local_pb";
import { seriesIsFewSamples } from "../usageChart";
import {
  SessionActionsViewSchema,
  SessionHeaderViewSchema,
  SessionUsageEntryViewSchema,
} from "../gen/remoteagent/core/v1/session_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { localeStore, t as message } from "../i18n";
import { SessionSheet } from "../sheets";
import { formatCacheHit, formatTokens, formatTtftDelta } from "../usageFormat";
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

describe("Phase 2 formatters and bounds", () => {
  it("formats zh and en token boundaries accurately", () => {
    expect(formatTokens(8_420, "zh-CN")).toBe("8,420");
    expect(formatTokens(8_420, "en")).toBe("8.4k");
    expect(formatTokens(999_600, "en")).toBe("1M");
    expect(formatTokens(99_960_000, "zh-CN")).toBe("1亿");
    expect(formatTokens(3_480_000_000, "zh-CN")).toBe("34.8亿");
    expect(formatTokens(3_480_000_000, "en")).toBe("3.5B");
    expect(formatCacheHit(0.004)).toBe("0.4%");
    expect(formatCacheHit(0.884)).toBe("88%");
    expect(formatCacheHit(0.996)).toBe("99.6%");
    expect(formatTtftDelta(520, 500, "zh-CN")).toBe(message("usage.flat"));
    expect(formatTtftDelta(300, 500, "zh-CN")).toBe("−200 ms");
    expect(formatTtftDelta(2_200, 1_000, "zh-CN")).toBe("+1.2 s");
  });

  it("clamps custom date ranges to 90 days and reports east-of-UTC offset sign", () => {
    const now = new Date(2026, 10, 18, 12, 0, 0).getTime();
    const veryOld = new Date(2026, 0, 1).getTime();
    const clamped = clampUsageCustomRange(BigInt(veryOld), BigInt(now + 10 * 86_400_000), now);
    const minDay = new Date(2026, 10, 18 - 89).getTime();
    const todayStart = new Date(2026, 10, 18).getTime();
    expect(Number(clamped.startMs)).toBe(minDay);
    expect(Number(clamped.endMs)).toBe(todayStart);
    expect(localTzOffsetMinutes(now)).toBe(-new Date(now).getTimezoneOffset());
  });
});

describe("Per-machine CLI switches and entry points", () => {
  it("renders accessible switches, toggles agents, and locks the last remaining CLI", async () => {
    const r = createDemo("cliswitches=normal");
    await mountAsync(<MachinePage remote={r} id="mac" go={() => undefined} onBack={() => undefined} />);
    const switches = [...hostEl.querySelectorAll<HTMLButtonElement>('.cli-sw-grp [role="switch"]')];
    expect(switches).toHaveLength(3);
    expect(switches[2].getAttribute("aria-checked")).toBe("true");

    // Disable Antigravity
    await act(async () => switches[2].click());
    await flushUi();
    expect(switches[2].getAttribute("aria-checked")).toBe("false");

    // Disable Claude -> Codex becomes locked last enabled
    await act(async () => switches[1].click());
    await flushUi();
    const refreshed = [...hostEl.querySelectorAll<HTMLButtonElement>('.cli-sw-grp [role="switch"]')];
    expect(refreshed[0].disabled).toBe(true);
    expect(refreshed[0].getAttribute("aria-checked")).toBe("true");
    expect(refreshed[0].textContent).toContain(message("settings.cli_switches_keep_one"));
    expect(refreshed[0].getAttribute("aria-label")).toContain(message("settings.cli_switches_keep_one"));
  });

  it("shows offline, lease-expired, and conflict states and falls back cleanly on legacy machines", async () => {
    const rOffline = createDemo("cliswitches=offline");
    await mountAsync(<MachinePage remote={rOffline} id="mac" go={() => undefined} onBack={() => undefined} />);
    expect(hostEl.textContent).toContain(message("settings.cli_switches_offline_reason", { name: "MacBook" }));
    expect([...hostEl.querySelectorAll<HTMLButtonElement>('.cli-sw-grp [role="switch"]')].every((el) => el.disabled)).toBe(true);

    act(() => root?.unmount());
    hostEl.remove();

    const rConflict = createDemo("cliswitches=conflict");
    await mountAsync(<MachinePage remote={rConflict} id="mac" go={() => undefined} onBack={() => undefined} />);
    const first = hostEl.querySelector<HTMLButtonElement>('.cli-sw-grp [role="switch"]')!;
    await act(async () => first.click());
    await flushUi();
    expect(hostEl.textContent).toContain(message("settings.cli_switches_conflict"));

    act(() => root?.unmount());
    hostEl.remove();

    // VPS has cliSwitches=false -> renders read-only cli_version row without switches
    await mountAsync(<MachinePage remote={rConflict} id="vps" go={() => undefined} onBack={() => undefined} />);
    expect(hostEl.querySelectorAll('.cli-sw-grp [role="switch"]')).toHaveLength(0);
    expect(hostEl.textContent).toContain("Codex");
  });

  it("routes ENABLE_AGENT in NewBoxView and enable_cli in ScheduleDetailView to machine settings", async () => {
    const openedMachines: string[] = [];
    const vm = create(NewSessionViewModelSchema, {
      projectKey: "p1",
      notices: [
        create(NewSessionNoticeViewSchema, {
          kind: NoticeKind.AGENT_DISABLED_FALLBACK,
          severity: NoticeSeverity.INFO,
          action: NoticeActionKind.ENABLE_AGENT,
          targetMachineId: "mac",
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
        onOpenMachineSettings={(id) => openedMachines.push(id)}
        onOpenDiskUsage={() => undefined}
        onReaddProject={() => undefined}
      />,
    );
    const btn = [...hostEl.querySelectorAll("button")].find((b) => b.textContent?.includes(message("new_session.enable_agent")));
    expect(btn).toBeTruthy();
    btn!.click();
    expect(openedMachines).toEqual(["mac"]);

    act(() => root?.unmount());
    hostEl.remove();

    let schedOpened = false;
    const schedVm = create(ScheduleDetailViewModelSchema, {
      found: true,
      machineId: "mac",
      taskId: "t1",
      title: "nightly",
      attention: {
        show: false,
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
      onOpenMachineSettings: () => {
        schedOpened = true;
      },
    };
    await mountAsync(<ScheduleDetailView vm={schedVm} actions={actions} />);
    const enableCliBtn = [...hostEl.querySelectorAll("button")].find((b) => b.textContent?.includes(message("schedule.enable_cli")));
    expect(enableCliBtn).toBeTruthy();
    enableCliBtn!.click();
    expect(schedOpened).toBe(true);
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
        sessionId="s1"
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
        onSubmitCommand={() => undefined}
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
  it("renders 4 metrics, composition bar, max-4 row overlay replacement, and en formatting", async () => {
    const r = createDemo("usage=normal");
    await mountAsync(<UsagePage remote={r} machineId="mac" wide={true} go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);

    // Tokens tab initially active with stacked bars and composition bar
    expect(hostEl.querySelectorAll(".u-bar1").length).toBeGreaterThan(0);
    expect(hostEl.querySelector(".u-cbar")).toBeTruthy();
    // Top 3 rows selected by default, 4th & 5th unselected
    expect(hostEl.querySelectorAll(".u-tbl tbody tr:not(.off-s)")).toHaveLength(3);
    expect(hostEl.querySelectorAll(".u-tbl tbody tr.off-s")).toHaveLength(2);

    // Click 4th row -> 4 selected
    const rows = [...hostEl.querySelectorAll<HTMLTableRowElement>(".u-tbl tbody tr")];
    await act(async () => rows[3].click());
    expect(hostEl.querySelectorAll(".u-tbl tbody tr:not(.off-s)")).toHaveLength(4);

    // Click 5th row -> still 4 selected, 5th active and 4th replaced
    await act(async () => rows[4].click());
    const afterRows = [...hostEl.querySelectorAll<HTMLTableRowElement>(".u-tbl tbody tr")];
    expect(afterRows[3].classList.contains("off-s")).toBe(true);
    expect(afterRows[4].classList.contains("off-s")).toBe(false);

    // Switch to Output Speed tab -> line SVG paths rendered
    const speedTab = [...hostEl.querySelectorAll("button")].find((b) => b.textContent === message("usage.tab_speed"))!;
    await act(async () => speedTab.click());
    await flushUi();
    expect(hostEl.querySelectorAll(".u-l1").length).toBeGreaterThan(0);

    // Switch to TTFT tab
    const ttftTab = [...hostEl.querySelectorAll("button")].find((b) => b.textContent === message("usage.tab_ttft"))!;
    await act(async () => ttftTab.click());
    await flushUi();
    expect(hostEl.textContent).toContain(message("usage.col_ttft_median"));

    // Switch locale to en -> k/M/B units
    const backToTok = [...hostEl.querySelectorAll("button")].find((b) => b.textContent === message("usage.tab_tokens"))!;
    await act(async () => backToTok.click());
    await act(async () => localeStore.overrideForTesting("en"));
    await flushUi();
    expect(hostEl.textContent).toMatch(/\d+(?:\.\d+)?[kMB]/);
  });

  it("handles few-samples scatter-only mode and all edge states", async () => {
    const rFew = createDemo("usage=few");
    await mountAsync(<UsagePage remote={rFew} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    const hitTab = [...hostEl.querySelectorAll("button")].find((b) => b.textContent === message("usage.tab_cache_hit"))!;
    await act(async () => hitTab.click());
    await flushUi();
    expect(hostEl.querySelectorAll(".u-svg .u-l1")).toHaveLength(0);
    expect(hostEl.querySelectorAll(".u-svg .u-m1").length).toBeGreaterThan(0);
    expect(hostEl.textContent).toContain(message("usage.few_samples_hint"));

    act(() => root?.unmount());
    hostEl.remove();

    const rEmpty = createDemo("usage=empty");
    await mountAsync(<UsagePage remote={rEmpty} machineId="mac" go={() => undefined} onBack={() => undefined} />);
    await flushUi(10);
    expect(hostEl.textContent).toContain(message("usage.empty_title"));

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
    expect(UsageMetricTab.TOKENS).toBe(1);
    expect(UsageRangePreset.SEVEN_DAYS).toBe(2);
    expect(getCoreHost(rOfflineNone)).toBeTruthy();

    const agyZeroSpeed = create(UsageSeriesViewSchema, {
      agent: Agent.ANTIGRAVITY,
      agentName: "Antigravity",
      model: "gemini-4-hydrogen",
      seriesKey: "agy:gemini-4-hydrogen",
      current: create(UsagePeriodSummarySchema, {
        uncachedInputTokens: 30_000n,
        cacheReadTokens: 70_000n,
        outputTokens: 10_000n,
        usageRequestCount: 5n,
        speedRequestCount: 0n,
      }),
    });
    expect(seriesIsFewSamples(agyZeroSpeed, UsageMetricTab.OUTPUT_SPEED)).toBe(false);
    expect(seriesIsFewSamples(agyZeroSpeed, UsageMetricTab.TTFT)).toBe(false);

    act(() => root?.unmount());
    hostEl.remove();

    await mountAsync(
      <SessionSheet
        sessionId="s-agy"
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
          }),
        })}
        onClose={() => undefined}
        onRefreshModels={() => undefined}
        onSubmitCommand={() => undefined}
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
