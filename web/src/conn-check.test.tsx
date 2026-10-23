// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "./dev/demo";
import { ConnCheck, connWord } from "./views/ConnCheck";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.restoreAllMocks());

it("shows a disconnected push stream even when server and computer round trips succeed", async () => {
  const remote = DemoRemote.create(new URLSearchParams());
  remote.conn = "offline";
  remote.everOnline = true;
  remote.listed = true;
  remote.connError = "同步设备状态超时，正在重连";
  vi.spyOn(remote, "showWaiting", "get").mockReturnValue(false);
  const server = vi.spyOn(remote, "pingServer").mockResolvedValue(12);
  const machine = vi.spyOn(remote, "pingMachine").mockResolvedValue(34);
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const push = () => [...host.querySelectorAll(".sr")].find(row => row.textContent?.startsWith("实时推送"));
  try {
    await act(async () => root.render(<ConnCheck remote={remote} onClose={() => {}} />));
    expect(server).toHaveBeenCalledTimes(3); expect(machine).toHaveBeenCalled();
    expect(host.textContent).toContain("12 ms"); expect(host.textContent).toContain("34 ms");
    // Diagnostics never suppress the waiting row with the cached-content delay.
    expect(connWord(remote).text).toBe("");
    expect(push()?.textContent).toBe("实时推送重连中");
    expect(host.textContent).toContain("实时推送未连接，会话变化可能延迟");
    expect(host.textContent).toContain(remote.connError);
    await act(async () => { remote.conn = "online"; remote.connError = ""; remote.touch(); });
    expect(push()?.textContent).toBe("实时推送已连接");
    expect(host.textContent).not.toContain("实时推送未连接");
    expect(server).toHaveBeenCalledTimes(3);
  } finally {
    act(() => root.unmount()); host.remove(); remote.stop();
  }
});
