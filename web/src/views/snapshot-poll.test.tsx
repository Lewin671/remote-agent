// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { SessionStatus } from "../gen/remoteagent/v1/event_pb";
import type { Remote } from "../remote";
import { useDerived } from "./actions";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined; host?.remove(); vi.restoreAllMocks(); vi.useRealTimers();
});
function Probe({ remote }: { remote: Remote }) { useDerived(remote, "s-run"); return null; }
it("polls only while visible, connected, online and not ended, refreshing once on eligibility", async () => {
  const remote = DemoRemote.create(new URLSearchParams());
  remote.start();
  let hidden = true;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  vi.spyOn(remote, "retainHistory").mockReturnValue(() => {});
  const refresh = vi.spyOn(remote, "refreshLiveSnapshot").mockResolvedValue(undefined);
  vi.useFakeTimers();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<Probe remote={remote} />));
  const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(15000); });
  const change = (f: () => void) => act(async () => { f(); remote.touch(); });
  const visible = () => change(() => { hidden = false; document.dispatchEvent(new Event("visibilitychange")); });
  await tick(); expect(refresh).toHaveBeenCalledTimes(0);
  await visible(); expect(refresh).toHaveBeenCalledTimes(1);
  await change(() => {}); expect(refresh).toHaveBeenCalledTimes(1);
  await tick(); expect(refresh).toHaveBeenCalledTimes(4);
  await change(() => { hidden = true; document.dispatchEvent(new Event("visibilitychange")); });
  await tick(); expect(refresh).toHaveBeenCalledTimes(4);
  await change(() => { remote.conn = "offline"; });
  await visible(); await tick(); expect(refresh).toHaveBeenCalledTimes(4);
  await change(() => { remote.conn = "online"; });
  expect(refresh).toHaveBeenCalledTimes(5);
  const session = remote.sessions.get("s-run")!;
  const machine = remote.machines.get(session.machineId)!;
  await change(() => { machine.online = false; });
  await tick(); expect(refresh).toHaveBeenCalledTimes(5);
  await change(() => { machine.online = true; });
  expect(refresh).toHaveBeenCalledTimes(6);
  await change(() => { session.info!.status = SessionStatus.EXITED; });
  await tick(); expect(refresh).toHaveBeenCalledTimes(6);
  await change(() => { session.info!.status = SessionStatus.IDLE; session.endedMs = 1n; });
  await tick(); expect(refresh).toHaveBeenCalledTimes(6);
  await act(async () => root!.unmount()); root = undefined;
  await tick(); expect(refresh).toHaveBeenCalledTimes(6);
  remote.stop();
});
