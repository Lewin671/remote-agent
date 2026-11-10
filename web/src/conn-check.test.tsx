// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "./dev/demo";
import { ConnCheck, connWord } from "./views/ConnCheck";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

it("shows a disconnected push stream even when server and computer round trips succeed", async () => {
  const remote = DemoRemote.create(new URLSearchParams());
  remote.conn = "offline";
  remote.everOnline = true;
  remote.listed = true;
  remote.connError = "同步设备状态超时，正在重连";
  vi.spyOn(remote, "showWaiting", "get").mockReturnValue(false);
  const server = vi.spyOn(remote, "pingServer").mockResolvedValue(12);
  const machine = vi.spyOn(remote, "pingMachine").mockResolvedValue(34);
  const reconnect = vi.spyOn(remote, "reconnectNow").mockImplementation(() => {});
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const push = () => [...host.querySelectorAll(".conn-check .mi")].find(row => row.textContent?.startsWith("实时推送"));
  try {
    await act(async () => root.render(<ConnCheck remote={remote} onClose={() => {}} />));
    expect(server).toHaveBeenCalledTimes(3); expect(machine).toHaveBeenCalled();
    expect(host.textContent).toContain("12 ms"); expect(host.textContent).toContain("34 ms");
    // Diagnostics never suppress the waiting row with the cached-content delay.
    expect(connWord(remote).text).toBe("");
    expect(push()?.textContent).toBe("实时推送重连中");
    expect(host.textContent).toContain("实时推送未连接，会话变化可能延迟");
    expect(host.textContent).toContain(remote.connError);
    const probes = machine.mock.calls.length;
    const retry = [...host.querySelectorAll("button")].find(b => b.textContent === "立即重连")!;
    await act(async () => retry.click());
    expect(reconnect).toHaveBeenCalledExactlyOnceWith(true);
    expect(server).toHaveBeenCalledTimes(3); expect(machine).toHaveBeenCalledTimes(probes);
    await act(async () => { remote.conn = "online"; remote.connError = ""; remote.touch(); });
    expect(push()?.textContent).toBe("实时推送已连接");
    expect(host.textContent).not.toContain("实时推送未连接");
    expect(host.textContent).not.toContain("立即重连");
    expect(server).toHaveBeenCalledTimes(3);
    const measure = [...host.querySelectorAll("button")].find(b => b.textContent === "再测一次")!;
    await act(async () => measure.click());
    expect(server).toHaveBeenCalledTimes(6);
    expect(reconnect).toHaveBeenCalledTimes(1);
  } finally {
    act(() => root.unmount()); host.remove(); remote.stop();
  }
});

it("shows the current attempt's step and ticking seconds without a failure warning", async () => {
  vi.useFakeTimers(); vi.setSystemTime(10_000);
  const remote = DemoRemote.create(new URLSearchParams());
  remote.conn = "connecting"; remote.everOnline = true;
  let startedAt = 7_000;
  let step = "同步设备状态";
  vi.spyOn(remote, "connStartedAt", "get").mockImplementation(() => startedAt);
  vi.spyOn(remote, "connStep", "get").mockImplementation(() => step);
  vi.spyOn(remote, "pingServer").mockResolvedValue(12);
  vi.spyOn(remote, "pingMachine").mockResolvedValue(34);
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<ConnCheck remote={remote} onClose={() => {}} />));
    expect(host.textContent).toContain("正在重连：同步设备状态，已 3 秒。");
    expect(host.textContent).not.toContain("会话变化可能延迟");
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(host.textContent).toContain("已 4 秒。");
    await act(async () => { step = "建立推送连接"; remote.touch(); });
    expect(host.textContent).toContain("正在重连：建立推送连接，已 4 秒。");
    await act(async () => { startedAt = Date.now(); remote.touch(); });
    expect(host.textContent).toContain("已 0 秒。");
    await act(async () => { remote.connError = "连接超时"; remote.touch(); });
    expect(host.textContent).toContain("正在重连：建立推送连接，已 0 秒。上一次失败：连接超时");
    expect(host.textContent).not.toContain("会话变化可能延迟");
    await act(async () => { step = ""; remote.touch(); });
    expect(host.textContent).toContain("最近一次失败：连接超时");
    expect(host.textContent).not.toContain("正在重连：");
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    await act(async () => root.unmount()); host.remove(); remote.stop();
  }
});
