import { t as message } from "../i18n";
// @vitest-environment happy-dom
// Nothing is said of the connection until the lists are in: the last state
// heard shows, where 未连接 and 电脑离线 for a moment and then the state would
// be a flash of the wrong word.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getCoreHost } from "../core/host";
import { DemoRemote } from "../dev/demo";
import { ComputerReachability } from "../gen/remoteagent/core/v1/session_view_pb";
import { patchSyncState } from "../test/remote-fixture";
import { SessionPage } from "./Session";
import { SessionRow } from "./Home";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
const remotes: DemoRemote[] = [];
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  for (const r of remotes.splice(0)) r.dispose();
});

function open(setup: (r: DemoRemote) => void, query = "") {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  remotes.push(r);
  r.start();
  setup(r);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<SessionPage remote={r} sessionId="s-idle" onBack={() => undefined} wide />));
  return r;
}
const status = () => host.querySelector(".dh .st")?.textContent ?? "";

it("shows the last state heard, and no connection word, before the lists are in", () => {
  open((r) => patchSyncState(r, { conn: "connecting", listed: false }), "conn=connecting");
  expect(status()).not.toContain(message("list_state.unlinked"));
  expect(status()).not.toContain(message("list_state.offline"));
  expect(status()).toContain(message("list_state.idle"));
});

it("shows the state once the lists are in", () => {
  open((r) => patchSyncState(r, { conn: "online", listed: true }));
  expect(status()).toContain(message("list_state.idle"));
});

it("still says 未连接 when the server cannot be reached", () => {
  open((r) => patchSyncState(r, { conn: "offline", listed: false }), "conn=nonetwork");
  expect(status()).toContain(message("list_state.unlinked"));
});

describe("a list row", () => {
  function row(setup: (r: DemoRemote) => void, query = "") {
    const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
    r.start();
    const demo = r as unknown as { demoMachines: Map<string, { online: boolean }>; syncDemoToCore(): void };
    for (const m of demo.demoMachines.values()) m.online = false;
    const offlineMachines = new Map([...r.machines].map(([id, m]) => [id, { ...m, online: false }]));
    patchSyncState(r, { machines: offlineMachines });
    demo.syncDemoToCore();
    setup(r);
    const core = getCoreHost(r);
    const vm = core.viewHome(30).running.find((x) => x.sessionId === "s-idle")!;
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<SessionRow row={vm} onSelect={() => undefined} />));
    return host.textContent ?? "";
  }

  it("shows the last state heard, not 未连接 or 电脑离线, while the lists are on their way", () => {
    const text = row((r) => patchSyncState(r, { conn: "connecting", listed: false }), "conn=connecting");
    expect(text).not.toContain(message("list_state.unlinked"));
    expect(text).not.toContain(message("list_state.offline"));
    expect(text).toContain(message("list_state.idle"));
  });

  it("says 电脑离线 once the lists are in and the computer is not", () => {
    expect(row((r) => patchSyncState(r, { conn: "online", listed: true }))).toContain(message("list_state.offline"));
  });

  it("says 未连接 when the server cannot be reached", () => {
    expect(row((r) => patchSyncState(r, { conn: "offline", listed: false }), "conn=nonetwork")).toContain(message("list_state.unlinked"));
  });
});

it("does not overlay HistoryOpening skeleton or replace Composer with EndCard when cached rows exist during unknown reachability", async () => {
  vi.useFakeTimers();
  try {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    remotes.push(r);
    r.start();
    const core = getCoreHost(r);
    const origViewSession = core.viewSession.bind(core);
    let reachability: ComputerReachability = ComputerReachability.UNKNOWN;
    vi.spyOn(core, "viewSession").mockImplementation((sid, win) => {
      const vm = origViewSession(sid, win);
      return {
        ...vm,
        header: vm.header ? {
          ...vm.header,
          found: true,
          computerOnline: false,
          computerUnlinked: false,
          computerReachability: reachability,
        } : undefined,
        composer: vm.composer ? {
          ...vm.composer,
          enabled: reachability === ComputerReachability.UNKNOWN,
        } : undefined,
        timeline: vm.timeline ? {
          ...vm.timeline,
          loading: true,
          loaded: false,
        } : undefined,
      };
    });

    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<SessionPage remote={r} sessionId="s-idle" onBack={() => undefined} wide />));
    await act(async () => { await vi.advanceTimersByTimeAsync(400); });

    // Cached rows render without overlaying the grey loading skeleton, and Composer stays visible instead of EndCard.
    expect(host.querySelectorAll(".history-row").length).toBeGreaterThan(0);
    expect(host.querySelector(".history-placeholder")).toBeNull();
    expect(host.querySelector(".endcard")).toBeNull();
    expect(host.querySelector(".cbar")).not.toBeNull();

    // Once grace expires and computer is genuinely offline, Composer stays mounted & editable inside .cbar.offline instead of EndCard.
    reachability = ComputerReachability.OFFLINE;
    await act(async () => root!.render(<SessionPage key="offline" remote={r} sessionId="s-idle" onBack={() => undefined} wide />));
    expect(host.querySelector(".endcard")).toBeNull();
    expect(host.querySelector(".cbar.offline")).not.toBeNull();
    expect(host.querySelector<HTMLTextAreaElement>("textarea.draft")).not.toBeNull();
  } finally {
    vi.useRealTimers();
  }
});
