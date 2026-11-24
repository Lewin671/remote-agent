import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { ConnState } from "./gen/remoteagent/core/v1/home_view_pb";
import {
  ConnCheckSummaryViewSchema,
  MachineListCardViewSchema,
  type ConnCheckSummaryView,
  type MachineListCardView,
} from "./gen/remoteagent/core/v1/settings_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";
import { ConnCheckScreenView, connWord } from "./views/ConnCheck";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

function plain(literal: string) {
  return create(TextSchema, { literal });
}

function makeMachines(): MachineListCardView[] {
  return [
    create(MachineListCardViewSchema, { machineId: "mac", name: "MacBook", online: true, member: true }),
    create(MachineListCardViewSchema, { machineId: "vps", name: "VPS-dev", online: true, member: true }),
    create(MachineListCardViewSchema, { machineId: "mini", name: "Mac mini", online: false, member: true }),
  ];
}

function makeSummary(overrides: MessageInitShape<typeof ConnCheckSummaryViewSchema> = {}): ConnCheckSummaryView {
  return create(ConnCheckSummaryViewSchema, {
    connState: ConnState.CONNECTED,
    networkAvailable: true,
    livePushConnected: true,
    everOnline: true,
    ...overrides,
  });
}

it("shows a disconnected push stream even when server and computer round trips succeed", async () => {
  const machines = makeMachines();
  const initial = makeSummary({
    connState: ConnState.RECONNECTING,
    isConnecting: true,
    displayGrace: true,
    everOnline: true,
    lastFailureText: plain("同步设备状态超时，正在重连"),
  });
  const server = vi.fn().mockResolvedValue(12);
  const machine = vi.fn().mockResolvedValue(34);
  const reconnect = vi.fn();
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const push = () => [...host.querySelectorAll(".conn-check .mi")].find(row => row.textContent?.startsWith(message("connection.live_push")));
  const render = (cc: ConnCheckSummaryView) => (
    <ConnCheckScreenView
      cc={cc}
      machines={machines}
      serverHost="example.com"
      pingServer={server}
      pingMachine={machine}
      onReconnect={reconnect}
      onClose={() => {}}
    />
  );
  try {
    await act(async () => root.render(render(initial)));
    expect(server).toHaveBeenCalledTimes(3); expect(machine).toHaveBeenCalled();
    expect(host.textContent).toContain("12 ms"); expect(host.textContent).toContain("34 ms");
    // Diagnostics never suppress the waiting row with the cached-content delay.
    expect(connWord(initial).text).toBe("");
    expect(push()?.textContent).toBe("实时推送重连中");
    expect(host.textContent).toContain("实时推送未连接，会话变化可能延迟");
    expect(host.textContent).toContain("同步设备状态超时，正在重连");
    const probes = machine.mock.calls.length;
    const retry = [...host.querySelectorAll("button")].find(b => b.textContent === message("connection.reconnect_now"))!;
    await act(async () => retry.click());
    expect(reconnect).toHaveBeenCalledTimes(1);
    expect(server).toHaveBeenCalledTimes(3); expect(machine).toHaveBeenCalledTimes(probes);
    await act(async () => root.render(render(makeSummary({ connState: ConnState.CONNECTED, everOnline: true }))));
    expect(push()?.textContent).toBe("实时推送已连接");
    expect(host.textContent).not.toContain("实时推送未连接");
    expect(host.textContent).not.toContain(message("connection.reconnect_now"));
    expect(server).toHaveBeenCalledTimes(3);
    const measure = [...host.querySelectorAll("button")].find(b => b.textContent === message("connection.test_again"))!;
    await act(async () => measure.click());
    expect(server).toHaveBeenCalledTimes(6);
    expect(reconnect).toHaveBeenCalledTimes(1);
  } finally {
    act(() => root.unmount()); host.remove();
  }
});

it("probes directReady machines even when server ping fails", async () => {
  const machines = [
    create(MachineListCardViewSchema, { machineId: "mac", name: "MacBook", online: true, member: true, directReady: true }),
    create(MachineListCardViewSchema, { machineId: "vps", name: "VPS-dev", online: true, member: true, directReady: false }),
  ];
  const server = vi.fn().mockRejectedValue(new Error("服务器没有回应"));
  const machine = vi.fn().mockResolvedValue(22);
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(
      <ConnCheckScreenView
        cc={makeSummary()}
        machines={machines}
        serverHost="example.com"
        pingServer={server}
        pingMachine={machine}
        onReconnect={() => {}}
        onClose={() => {}}
      />,
    ));
    expect(machine).toHaveBeenCalledExactlyOnceWith("mac", expect.any(AbortSignal));
    expect(host.textContent).toContain("22 ms");
    expect(host.textContent).toContain(message("connection.not_checked"));
  } finally {
    act(() => root.unmount()); host.remove();
  }
});

it("shows the current attempt's step and ticking seconds without a failure warning", async () => {
  vi.useFakeTimers(); vi.setSystemTime(10_000);
  const machines = makeMachines();
  const server = vi.fn().mockResolvedValue(12);
  const machine = vi.fn().mockResolvedValue(34);
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const render = (cc: ConnCheckSummaryView) => (
    <ConnCheckScreenView
      cc={cc}
      machines={machines}
      serverHost="example.com"
      pingServer={server}
      pingMachine={machine}
      onReconnect={() => {}}
      onClose={() => {}}
    />
  );
  try {
    await act(async () => root.render(render(makeSummary({
      connState: ConnState.RECONNECTING,
      isConnecting: true,
      everOnline: true,
      connStartedAtMs: 7_000n,
      connStep: plain("同步设备状态"),
    }))));
    expect(host.textContent).toContain("正在重连：同步设备状态，已 3 秒。");
    expect(host.textContent).not.toContain("会话变化可能延迟");
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(host.textContent).toContain("已 4 秒。");
    await act(async () => root.render(render(makeSummary({
      connState: ConnState.RECONNECTING,
      isConnecting: true,
      everOnline: true,
      connStartedAtMs: 7_000n,
      connStep: plain("建立推送连接"),
    }))));
    expect(host.textContent).toContain("正在重连：建立推送连接，已 4 秒。");
    const restartAt = BigInt(Date.now());
    await act(async () => root.render(render(makeSummary({
      connState: ConnState.RECONNECTING,
      isConnecting: true,
      everOnline: true,
      connStartedAtMs: restartAt,
      connStep: plain("建立推送连接"),
    }))));
    expect(host.textContent).toContain("已 0 秒。");
    await act(async () => root.render(render(makeSummary({
      connState: ConnState.RECONNECTING,
      isConnecting: true,
      everOnline: true,
      connStartedAtMs: restartAt,
      connStep: plain("建立推送连接"),
      lastFailureText: plain("连接超时"),
    }))));
    expect(host.textContent).toContain("正在重连：建立推送连接，已 0 秒。上一次失败：连接超时");
    expect(host.textContent).not.toContain("会话变化可能延迟");
    await act(async () => root.render(render(makeSummary({
      connState: ConnState.RECONNECTING,
      isConnecting: true,
      everOnline: true,
      connStartedAtMs: restartAt,
      lastFailureText: plain("连接超时"),
    }))));
    expect(host.textContent).toContain("最近一次失败：连接超时");
    expect(host.textContent).not.toContain("正在重连：");
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    await act(async () => root.unmount()); host.remove();
  }
});
