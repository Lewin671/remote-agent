// @vitest-environment happy-dom
// 连接检测: golden breakdown rendering from clientcore, event wiring, and server median.

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { create, fromBinary } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnProbeState } from "../core/host";
import { renderText } from "../core/text";
import { DemoRemote } from "../dev/demo";
import { ScreenGoldenFileSchema, type ScreenGoldenFile } from "../gen/remoteagent/core/v1/golden_pb";
import { ConnState } from "../gen/remoteagent/core/v1/home_view_pb";
import {
  ConnCheckSummaryViewSchema,
  MachineLatencySegmentSchema,
  MachineLatencyState,
  MachineLatencyViewSchema,
  MachineListCardViewSchema,
  type SettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { TextSchema } from "../gen/remoteagent/core/v1/text_pb";
import { localeStore, t as message } from "../i18n";
import { ConnCheck, ConnCheckScreenView, median } from "./ConnCheck";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const GOLDENS_DIR = resolve(__dirname, "../../../testdata/screens");

function loadSettingsGolden(viewName: string): SettingsViewModel {
  const bytes = readFileSync(resolve(GOLDENS_DIR, "settings_overview.golden.pb"));
  const doc: ScreenGoldenFile = fromBinary(ScreenGoldenFileSchema, new Uint8Array(bytes));
  for (const step of doc.steps) {
    for (const v of step.views) {
      if (v.name === viewName && v.viewModel?.view.case === "settings") {
        return v.viewModel.view.value;
      }
    }
  }
  throw new Error(`Golden settings view '${viewName}' not found`);
}

let root: Root | undefined;
let host: HTMLElement;
const remotes: DemoRemote[] = [];

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
  localeStore.overrideForTesting(undefined);
});

function mount(node: React.ReactNode) {
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(node));
}

function machineRow(id: string): HTMLElement {
  const el = host.querySelector<HTMLElement>(`.conn-check .mi[data-machine-id="${id}"]`);
  if (!el) throw new Error(`Machine row '${id}' not found`);
  return el;
}

function segmentsOf(row: HTMLElement) {
  return [...row.querySelectorAll<HTMLElement>(".conn-lg > div")].map((div) => ({
    label: div.querySelector("dt")?.firstChild?.textContent?.trim() ?? "",
    slowTag: div.querySelector("dt .tg")?.textContent?.trim() ?? "",
    value: div.querySelector("dd:not(.why)")?.textContent?.trim() ?? "",
    detail: div.querySelector("dd.why")?.textContent?.trim() ?? "",
    emphasized: div.classList.contains("slow"),
  }));
}

describe("connection check golden rendering", () => {
  it("takes the middle sample for server /healthz probes", () => {
    expect(median([900, 80, 95])).toBe(95);
    expect(median([70])).toBe(70);
  });

  it("renders all five connCheckBreakdown golden rows (direct ok, slow computer, rerouted, outdated, timeout)", () => {
    const vm = loadSettingsGolden("connCheckBreakdown");
    mount(
      <ConnCheckScreenView
        cc={vm.connCheck}
        machines={vm.machines}
        serverHost="relay.example.com"
        onReconnect={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    // Server row rendered from golden cc (86 ms, online dot)
    const serverRow = [...host.querySelectorAll<HTMLElement>(".conn-check .mi")].find((e) =>
      e.querySelector(".mt")?.firstChild?.textContent === message("security.server"),
    )!;
    expect(serverRow.querySelector(".mv")?.textContent?.trim()).toBe("86 ms");
    expect(serverRow.querySelector(".mv .mark.online")).not.toBeNull();

    // 1. Direct normal (m-cc-1-ok)
    const okRow = machineRow("m-cc-1-ok");
    expect(okRow.classList.contains("pc")).toBe(true);
    expect(okRow.querySelector(".conn-top .mt small.direct-line")?.textContent).toBe("正在用直连 · 用于加载");
    expect(okRow.querySelector(".conn-top .mv")?.textContent?.trim()).toBe("38 ms");
    expect(okRow.querySelector(".conn-top .mv .mark.online")).not.toBeNull();
    expect(okRow.getAttribute("aria-label")).toContain("38");
    expect(segmentsOf(okRow)).toEqual([
      { label: "网络往返", slowTag: "", value: "11 ms", detail: "", emphasized: false },
      { label: "电脑处理", slowTag: "", value: "21 ms", detail: "", emphasized: false },
    ]);

    // 2. Slow on computer with detail line (m-cc-2-slow)
    const slowRow = machineRow("m-cc-2-slow");
    expect(slowRow.querySelector(".conn-top .mv")?.textContent?.trim()).toBe("1,250 ms · 较慢");
    expect(slowRow.querySelector(".conn-top .mv .mark.off")).not.toBeNull();
    const slowSegs = segmentsOf(slowRow);
    expect(slowSegs).toHaveLength(2);
    expect(slowSegs[0]).toEqual({ label: "网络往返", slowTag: "", value: "20 ms", detail: "", emphasized: false });
    expect(slowSegs[1].label).toBe("电脑处理");
    expect(slowSegs[1].slowTag).toBe("较慢");
    expect(slowSegs[1].emphasized).toBe(true);
    expect(slowSegs[1].detail).toContain("主要在排队");

    // 3. Rerouted to relay (m-cc-3-reroute): summary overridden + 3rd wait-direct segment
    const rerouteRow = machineRow("m-cc-3-reroute");
    expect(rerouteRow.querySelector(".conn-top .mt small.direct-line")?.textContent).toBe(
      message("connection.latency_rerouted"),
    );
    expect(rerouteRow.querySelector(".conn-top .mv .mark.off")).not.toBeNull();
    const rerouteSegs = segmentsOf(rerouteRow);
    expect(rerouteSegs).toHaveLength(3);
    expect(rerouteSegs[2].label).toBe(message("connection.latency_wait_direct"));
    expect(rerouteSegs[2].slowTag).toBe(message("connection.latency_slow"));
    expect(rerouteSegs[2].emphasized).toBe(true);

    // 4. Outdated computer (m-cc-4-outdated): no dot, trailing 版本较旧, note paragraph
    const oldRow = machineRow("m-cc-4-outdated");
    expect(oldRow.querySelector(".conn-top .mv")?.textContent?.trim()).toBe(message("connection.latency_outdated"));
    expect(oldRow.querySelector(".conn-top .mv .mark")).toBeNull();
    expect(oldRow.querySelector(".conn-nt")?.textContent).toBe(message("connection.latency_outdated_all"));

    // 5. Computer timeout (m-cc-5-timeout): red trailing error, stuck segment emphasized without 较慢 tag
    const timeoutRow = machineRow("m-cc-5-timeout");
    const timeoutMv = timeoutRow.querySelector(".conn-top .mv")!;
    expect(timeoutMv.classList.contains("red")).toBe(true);
    expect(timeoutMv.textContent?.trim()).toBe(message("connection.latency_computer_no_reply"));
    expect(timeoutMv.querySelector(".mark")).toBeNull();
    const timeoutSegs = segmentsOf(timeoutRow);
    expect(timeoutSegs).toHaveLength(2);
    expect(timeoutSegs[0]).toEqual({ label: "网络往返", slowTag: "", value: "302 ms", detail: "", emphasized: false });
    expect(timeoutSegs[1]).toEqual({ label: "电脑处理", slowTag: "", value: "超过 15 秒", detail: "", emphasized: true });

    // Footer renders text directly from core
    expect(host.querySelector(".gf")?.textContent).toBe(renderText(vm.connCheck?.footerText));
    expect(host.querySelector(".gf")?.textContent).toContain(
      message("connection.latency_is_one_round_trip_the_server_row_measures"),
    );
    expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false);
  });

  it("renders golden connCheckBreakdown cleanly in English", () => {
    localeStore.overrideForTesting("en");
    const vm = loadSettingsGolden("connCheckBreakdown");
    mount(
      <ConnCheckScreenView
        cc={vm.connCheck}
        machines={vm.machines}
        serverHost="relay.example.com"
        onReconnect={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    const slowRow = machineRow("m-cc-2-slow");
    expect(slowRow.querySelector(".conn-top .mv")?.textContent?.trim()).toBe("1,250 ms · Slow");
    const segs = segmentsOf(slowRow);
    expect(segs[0].label).toBe("Network");
    expect(segs[1].label).toBe("Computer");
    expect(segs[1].slowTag).toBe("Slow");
    expect(segs[1].detail).toContain("Mostly queued");
    expect(host.querySelector(".gf")?.textContent).toBe(renderText(vm.connCheck?.footerText));
    expect(host.querySelector(".gf")?.textContent).toContain(
      message("connection.latency_is_one_round_trip_the_server_row_measures"),
    );
  });

  it("renders measuring placeholders and disables test-again while cc.machinesBusy is true", () => {
    const busySummary = create(ConnCheckSummaryViewSchema, {
      connState: ConnState.CONNECTED,
      networkAvailable: true,
      livePushConnected: true,
      everOnline: true,
      serverState: ConnProbeState.WORKING,
      latencyMs: 42,
      machinesBusy: true,
    });
    const measuringMachine = create(MachineListCardViewSchema, {
      machineId: "m1",
      name: "MacBook",
      online: true,
      member: true,
      latency: create(MachineLatencyViewSchema, {
        state: MachineLatencyState.MEASURING,
        valueText: create(TextSchema, { key: "connection.checking" }),
        segments: [
          create(MachineLatencySegmentSchema, {
            labelText: create(TextSchema, { key: "connection.latency_network" }),
            valueText: create(TextSchema, { key: "connection.latency_pending" }),
          }),
          create(MachineLatencySegmentSchema, {
            labelText: create(TextSchema, { key: "connection.latency_computer" }),
            valueText: create(TextSchema, { key: "connection.latency_pending" }),
          }),
        ],
      }),
    });
    mount(
      <ConnCheckScreenView
        cc={busySummary}
        machines={[measuringMachine]}
        serverHost="relay.example.com"
        onReconnect={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    const row = machineRow("m1");
    expect(row.querySelector(".conn-top .mv .ic")).not.toBeNull();
    expect(row.querySelector(".conn-top .mv")?.textContent?.trim()).toBe(message("connection.checking"));
    expect(segmentsOf(row)).toEqual([
      { label: "网络往返", slowTag: "", value: "—", detail: "", emphasized: false },
      { label: "电脑处理", slowTag: "", value: "—", detail: "", emphasized: false },
    ]);
    expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(true);
  });
});

describe("connection check event wiring and core integration", () => {
  it("emits CHECKING then start on open, reports server median, and stops + aborts on unmount", async () => {
    const events: string[] = [];
    const signals: AbortSignal[] = [];
    const pings = [900, 80, 95];
    const pingServer = vi.fn(async (signal?: AbortSignal) => {
      if (signal) signals.push(signal);
      return pings.shift()!;
    });
    const onReportProbe = vi.fn((state: ConnProbeState, latencyMs?: number, lastFailure?: string) => {
      events.push(`probe:${state}:${latencyMs ?? 0}:${lastFailure ?? ""}`);
    });
    const onStartCheck = vi.fn(() => { events.push("start"); });
    const onStopCheck = vi.fn(() => { events.push("stop"); });

    const cc = create(ConnCheckSummaryViewSchema, {
      connState: ConnState.CONNECTED,
      networkAvailable: true,
      livePushConnected: true,
      everOnline: true,
    });
    mount(
      <ConnCheckScreenView
        cc={cc}
        machines={[]}
        serverHost="relay.example.com"
        pingServer={pingServer}
        onReportProbe={onReportProbe}
        onStartCheck={onStartCheck}
        onStopCheck={onStopCheck}
        onReconnect={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    await vi.waitFor(() => expect(onReportProbe).toHaveBeenCalledTimes(2));
    expect(events).toEqual([
      `probe:${ConnProbeState.CHECKING}:0:`,
      "start",
      `probe:${ConnProbeState.WORKING}:95:`,
    ]);
    expect(signals).toHaveLength(3);

    // Click "再测一次" with a pending ping, then unmount to verify abort + stop.
    pingServer.mockImplementationOnce(
      (signal?: AbortSignal) =>
        new Promise<number>((_resolve, reject) => {
          if (signal) signals.push(signal);
          signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
        }),
    );
    const btn = host.querySelector<HTMLButtonElement>(".conn-again button")!;
    act(() => btn.click());
    await vi.waitFor(() => expect(signals).toHaveLength(4));
    expect(signals[3].aborted).toBe(false);

    act(() => root?.unmount());
    root = undefined;
    expect(signals[3].aborted).toBe(true);
    expect(onStopCheck).toHaveBeenCalledTimes(1);
    expect(events.at(-1)).toBe("stop");
  });

  describe("live core smoke with DemoRemote", () => {
    const base = () =>
      message("common.sentence_join", {
        first: message("connection.latency_is_one_round_trip_the_server_row_measures"),
        second: message("connection.latency_footer_breakdown"),
      });
    const footer = () => host.querySelector(".gf")!.textContent!;
    const settle = () =>
      vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>(".conn-again button")!.disabled).toBe(false));

    beforeEach(() => vi.stubGlobal("RTCPeerConnection", class {}));
    afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

    function openDemo(query: string, patch?: (r: DemoRemote) => void) {
      const r = DemoRemote.create(new URLSearchParams(query), () => Promise.resolve());
      remotes.push(r);
      r.start();
      r.pingServer = async () => 40;
      patch?.(r);
      mount(<ConnCheck remote={r} onClose={() => undefined} />);
      return r;
    }

    it("measures via clientcore probe_latency and renders direct summary + two breakdown rows", async () => {
      // Demo runs no transport, so a direct machine has no keepalive samples
      // and the core waits its 3 s before probing; direct rows are covered by
      // the golden test above, so this smoke stays on the relay.
      openDemo("");
      await settle();

      const mac = machineRow("mac");
      expect(mac.querySelectorAll(".mark").length).toBe(1);
      expect(segmentsOf(mac).map((s) => s.label)).toEqual(["网络往返", "电脑处理"]);

      // VPS-dev in demo returns slow computer execution (1,790 ms)
      const vps = machineRow("vps");
      const vpsSegs = segmentsOf(vps);
      expect(vpsSegs[1].label).toBe("电脑处理");
      expect(vpsSegs[1].slowTag).toBe("较慢");
      expect(vpsSegs[1].detail).toContain("主要在执行");

      // Offline machine has no direct summary or breakdown
      const mini = machineRow("mini");
      expect(mini.classList.contains("pc")).toBe(false);
      expect(mini.querySelector("small.direct-line")).toBeNull();
      expect(mini.querySelector(".conn-lg")).toBeNull();
      expect(mini.querySelector(".mv")?.textContent?.trim()).toBe(message("common.ios_offline"));

      expect(footer()).toBe(base());
    });

    it("appends browser-unsupported direct note once in footer in zh and en", async () => {
      vi.stubGlobal("RTCPeerConnection", undefined);
      openDemo("");
      await settle();
      expect(machineRow("mac").querySelector("small.direct-line")).toBeNull();
      expect(footer()).toBe(base() + "这个浏览器不支持直连，已全部经中继连接。");

      act(() => root?.unmount());
      root = undefined;
      host.remove();
      localeStore.overrideForTesting("en");
      openDemo("");
      await settle();
      expect(footer()).toBe(`${base()} This browser does not support direct connections; everything connects via relay.`);
    });
  });
});
