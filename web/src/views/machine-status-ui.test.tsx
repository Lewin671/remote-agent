// @vitest-environment happy-dom
// 电脑状态 and the disk protection screens, rendered against the demo
// Remote and clicked through as a user would.

import { create } from "@bufbuild/protobuf";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import { ProjectSummarySchema } from "../gen/remoteagent/v1/remote_pb";
import { localeStore, t as message } from "../i18n";
import { machineCleanupPrompt } from "../machine-status";
import { CommandError, type CommandKind } from "../remote";
import { DiskReminders } from "./DiskReminders";
import { MachinePage } from "./Settings";
import { MachineRings } from "./MachineRings";
import { MachineStatusPage } from "./MachineStatus";
import { Roll } from "./machine-status-hooks";
import { ProjectPage } from "./Project";
import Workspace from "../Workspace";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
let reduced = false;

beforeEach(() => {
  localeStore.overrideForTesting("zh-CN");
  reduced = false;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      matches: query.includes("reduce") ? reduced : false, media: query, onchange: null,
      addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
    }),
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  localeStore.overrideForTesting(undefined);
  vi.useRealTimers();
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
});

function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
}

function demo(query = ""): DemoRemote {
  const r = DemoRemote.create(new URLSearchParams(query), async () => undefined);
  r.start();
  return r;
}

const button = (text: string | RegExp, within: ParentNode = host) =>
  [...within.querySelectorAll("button")].find((b) => (typeof text === "string" ? b.textContent === text : text.test(b.textContent ?? ""))) as HTMLButtonElement | undefined;

async function click(el: HTMLElement | undefined | null) {
  expect(el).toBeTruthy();
  await act(async () => el!.click());
}

/** The first answer is in: the numbers and the ranking are drawn. */
const ready = () => vi.waitFor(() => expect(host.querySelector(".ms-list")).toBeTruthy());
const rankTitles = () => [...host.querySelectorAll(".ms-u .ms-rt > span:first-child")].map((e) => e.textContent);
const none = () => undefined;

describe("the rings at the top of a computer's page", () => {
  it("draws CPU, memory and free disk space, and opens the status", async () => {
    const r = demo();
    const go = vi.fn();
    mount(<MachineRings remote={r} id="mac" go={go} />);
    await vi.waitFor(() => expect(host.querySelectorAll(".ms-ring b")[2].textContent).toBe("42 GB"));
    expect([...host.querySelectorAll(".ms-ring small")].map((e) => e.textContent)).toEqual(["CPU", "内存", "磁盘可用"]);
    expect(host.querySelectorAll(".ms-ring .warn, .ms-ring.warn, .ms-ring.crit")).toHaveLength(0);
    // Each ring reads on its own.
    expect([...host.querySelectorAll(".ms-ring .ms-sr")].map((e) => e.textContent)).toEqual([expect.stringMatching(/^CPU，\d+%$/), expect.stringMatching(/^内存，\d+%$/), "磁盘可用，42 GB"]);
    await click(host.querySelector("button"));
    expect(go).toHaveBeenCalledWith("machine-status/mac");
  });

  it("turns only the tight ring red, and the one that needs a look orange", async () => {
    const r = demo("ms=tight");
    mount(<MachineRings remote={r} id="mac" go={none} />);
    await vi.waitFor(() => expect(host.querySelectorAll(".ms-ring b")[2].textContent).toBe("1.2 GB"));
    const rings = [...host.querySelectorAll(".ms-ring")];
    expect(rings.map((e) => (e.classList.contains("crit") ? "crit" : e.classList.contains("warn") ? "warn" : ""))).toEqual(["", "warn", "crit"]);
  });

  it("is only tracks and dashes, faded and not openable, for a computer that is offline", () => {
    const r = demo();
    const go = vi.fn();
    mount(<MachineRings remote={r} id="mini" go={go} />);
    const card = host.querySelector<HTMLButtonElement>("button.ms-rings")!;
    expect(card.disabled).toBe(true);
    expect(card.classList.contains("off")).toBe(true);
    expect([...host.querySelectorAll(".ms-ring b")].map((e) => e.textContent)).toEqual(["—", "—", "—"]);
    expect(host.querySelectorAll("circle.p")).toHaveLength(0);
    card.click();
    expect(go).not.toHaveBeenCalled();
  });

  it("is not there for a daemon that cannot report, and asks for nothing", () => {
    const r = demo();
    const ask = vi.spyOn(r, "getMachineStatus");
    mount(<MachineRings remote={r} id="vps" go={none} />);
    expect(host.querySelector(".ms-rings")).toBeNull();
    expect(ask).not.toHaveBeenCalled();
  });
});

describe("电脑状态", () => {
  it("shows the CPU chart, the two small cards and the sessions that use most", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    expect(host.querySelector(".ms-title")!.textContent).toBe("MacBook");
    expect(host.querySelector(".ms-live")!.textContent).toBe("实时");
    expect(host.querySelector(".ms-hero .ms-kick")!.textContent).toBe("CPU");
    expect(host.querySelector(".ms-hero .ms-aside")!.textContent).toBe("3.2 负载10 核");
    const tiles = [...host.querySelectorAll(".ms-tile")];
    expect(tiles.map((e) => e.querySelector(".ms-kick")!.textContent)).toEqual(["内存", "磁盘"]);
    expect(tiles[0].textContent).toContain("压力正常");
    expect(tiles[0].textContent).toContain("共 16 GB，交换 1.3 GB");
    expect(tiles[1].textContent).toContain("空间充足");
    expect(tiles[1].querySelector(".ms-say")!.innerHTML).toContain("共 460 GB<br>remote-agent 占 3.1 GB");
    expect(rankTitles()).toEqual(["给登录接口加限流", "修复登录超时", "梳理状态栏组件"]);
    expect(host.querySelector(".ms-foot")!.textContent).toBe("已开机 3 天 4 小时 · 电池 81%，充电中");
    // The chart is real samples only, with a dot for now.
    expect(host.querySelector(".ms-plot path[d]")).toBeTruthy();
    expect(host.querySelector(".ms-rows")).toBeNull();
    expect(host.querySelector(".ms-axis")!.textContent).toBe("2 分钟前1 分钟前现在");
  });

  it("draws its frame at once, with dashes and \"正在读取\", no spinner and no ranking, until the first answer", async () => {
    const r = demo("ms-full");
    const real = r.getMachineStatus.bind(r);
    let answer!: () => void;
    const held = new Promise<void>((done) => { answer = done; });
    r.getMachineStatus = async (...args) => { await held; return real(...args); };
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    expect(host.querySelector(".ms-title")!.textContent).toBe("MacBook");
    expect(host.querySelector(".ms-live")!.textContent).toBe("正在读取");
    expect(host.querySelector(".ms-dot")!.classList.contains("idle")).toBe(true);
    expect([...host.querySelectorAll(".ms-xl, .ms-lg")].map((e) => e.textContent)).toEqual(["—", "—", "—"]);
    expect(host.querySelector(".ms-plot svg")).toBeNull();
    expect(host.querySelector(".ms-sec, .ms-list")).toBeNull();
    expect(host.querySelector(".spin, [role=progressbar]")).toBeNull();
    await act(async () => answer());
    await ready();
    expect(host.querySelector(".ms-live")!.textContent).toBe("实时");
  });

  it("starts with one point, and no line, when the daemon had nobody looking", async () => {
    const r = demo();
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    expect(host.querySelector(".ms-plot path:not([fill='none'])")?.getAttribute("d") ?? "").toBe("");
    expect(host.querySelector(".ms-plot circle[visibility='visible']")).toBeTruthy();
  });

  it("makes memory the main chart when its card is tapped, and back again, reordering the ranking", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    await click(button("显示进程"));
    expect(rankTitles().slice(0, 3)).toEqual(["java", "xcodebuild", "Google Chrome"]);
    await click(host.querySelectorAll<HTMLElement>(".ms-tile")[0]);
    expect(host.querySelector(".ms-hero .ms-kick")!.textContent).toBe("内存");
    expect(host.querySelector(".ms-hero .ms-xl small")!.textContent).toBe("GB 已用");
    expect(host.querySelector(".ms-hero .ms-aside")!.textContent).toBe("共 16 GB交换 1.3 GB");
    // The pressure sits above the axis; CPU is a small card now, with its load and cores, and no scale.
    expect(host.querySelector(".ms-hero .ms-pr")!.textContent).toBe("压力正常");
    const small = host.querySelectorAll(".ms-tile")[0];
    expect(small.querySelector(".ms-kick")!.textContent).toBe("CPU");
    expect(small.textContent).toContain("负载 3.2");
    expect(small.querySelector(".ms-uv")).toBeNull();
    expect(rankTitles().slice(0, 3)).toEqual(["java", "Google Chrome", "xcodebuild"]);
    await click(host.querySelectorAll<HTMLElement>(".ms-tile")[0]);
    expect(host.querySelector(".ms-hero .ms-kick")!.textContent).toBe("CPU");
    expect(rankTitles().slice(0, 3)).toEqual(["java", "xcodebuild", "Google Chrome"]);
  });

  it("remembers the main chart for this visit only", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    await click(host.querySelectorAll<HTMLElement>(".ms-tile")[0]);
    act(() => root!.unmount());
    host.remove();
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    expect(host.querySelector(".ms-hero .ms-kick")!.textContent).toBe("CPU");
  });

  it("opens a session from its row, but not a process", async () => {
    const r = demo("ms-full");
    const go = vi.fn();
    mount(<MachineStatusPage remote={r} id="mac" go={go} onBack={none} />);
    await ready();
    await click(host.querySelector<HTMLElement>("button.ms-u"));
    expect(go).toHaveBeenCalledWith("s/s-claude");
    await click(button("显示进程"));
    expect(host.querySelectorAll("button.ms-u")).toHaveLength(0);
    // A process in a session's tree names the session after its other figure.
    expect(host.querySelector(".ms-u .ms-rm small")!.textContent).toBe("2.1 GB · 给登录接口加限流");
  });

  it("opens the attachment storage from the disk card", async () => {
    const r = demo("ms-full");
    const go = vi.fn();
    mount(<MachineStatusPage remote={r} id="mac" go={go} onBack={none} />);
    await ready();
    await click(host.querySelectorAll<HTMLElement>(".ms-tile")[1]);
    expect(go).toHaveBeenCalledWith("m/mac/storage");
  });

  it("holds the number of the moment under the pointer and says how long ago it was", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    const plot = host.querySelector<HTMLElement>(".ms-plot")!;
    plot.getBoundingClientRect = () => ({ left: 0, top: 0, right: 358, bottom: 104, width: 358, height: 104, x: 0, y: 0, toJSON() {} });
    const move = (x: number) => {
      const e = new MouseEvent("pointermove", { bubbles: true, clientX: x });
      Object.defineProperty(e, "pointerType", { value: "mouse" });
      plot.dispatchEvent(e);
    };
    await act(async () => move(179));
    expect(host.querySelector(".ms-live")!.textContent).toMatch(/^(5[68]|6[024]) 秒前$/);
    expect(host.querySelector(".ms-live .ms-dot")).toBeTruthy();
    await act(async () => plot.dispatchEvent(new MouseEvent("pointerup", { bubbles: true })));
    expect(host.querySelector(".ms-live")!.textContent).toBe("实时");
  });

  it("steps through the samples from the keyboard and reads the chart without announcing each point", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    const plot = host.querySelector<HTMLElement>(".ms-plot")!;
    expect(plot.getAttribute("role")).toBe("slider");
    expect(plot.getAttribute("aria-label")).toMatch(/^最近 2 分钟，最高 \d+%，现在 \d+%$/);
    expect(plot.hasAttribute("aria-live")).toBe(false);
    expect([...plot.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["上一个采样点", "下一个采样点"]);
    const key = (k: string) => act(async () => void plot.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
    await key("ArrowLeft");
    await key("ArrowLeft");
    expect(host.querySelector(".ms-live")!.textContent).toBe("4 秒前");
    expect(plot.getAttribute("aria-valuetext")).toMatch(/4 秒前/);
    await key("ArrowRight");
    expect(host.querySelector(".ms-live")!.textContent).toBe("2 秒前");
    await key("Escape");
    expect(host.querySelector(".ms-live")!.textContent).toBe("实时");
  });

  it("reads the small cards as buttons that make them the main chart", async () => {
    const r = demo("ms-full&ms=tight");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    const [memory, disk] = [...host.querySelectorAll<HTMLElement>(".ms-tile")];
    expect(memory.tagName).toBe("BUTTON");
    expect(memory.getAttribute("aria-label")).toMatch(/^内存，1[23]\.\d GB 已用，压力偏高，设为主图$/);
    expect(disk.getAttribute("aria-label")).toBe("磁盘，1.2 GB 可用，快满了");
  });
});

describe("a disk that is nearly full", () => {
  it("colours only that card, and offers the two actions under the small cards", async () => {
    const r = demo("ms-full&ms=tight");
    const go = vi.fn();
    mount(<MachineStatusPage remote={r} id="mac" go={go} onBack={none} />);
    await ready();
    const disk = host.querySelectorAll(".ms-tile")[1];
    expect(disk.querySelector(".ms-lg")!.classList.contains("crit")).toBe(true);
    expect(disk.querySelector(".ms-stor")!.classList.contains("crit")).toBe(true);
    expect(disk.textContent).toContain("快满了");
    expect(disk.textContent).toContain("新会话可能启动不了");
    // CPU never changes colour; memory's pressure is only its words.
    expect(host.querySelector(".ms-hero .warn, .ms-hero .crit")).toBeNull();
    expect(host.querySelector(".ms-tile .ms-say b.warn")!.textContent).toBe("压力偏高");
    const rows = host.querySelector(".ms-rows")!;
    expect(rows.textContent).toBe("让 Agent 帮我清理remote-agent 的占用3.1 GB");
    await click(button(/remote-agent 的占用/, rows));
    expect(go).toHaveBeenCalledWith("m/mac/storage");
  });

  it("opens a new session on this computer in its latest project with the prompt filled in, and sends nothing", async () => {
    const r = demo("ms-full&ms=tight");
    r.machines.get("mac")!.meta!.projects = [
      create(ProjectSummarySchema, { path: "/Users/me/Code/old", latestCreatedMs: 1n }),
      create(ProjectSummarySchema, { path: "/Users/me/Code/foo", latestEndedMs: 5n }),
    ];
    const sent = vi.spyOn(r, "machineCommand");
    const go = vi.fn();
    mount(<MachineStatusPage remote={r} id="mac" go={go} onBack={none} />);
    await ready();
    await click(button("让 Agent 帮我清理"));
    expect(go).toHaveBeenCalledWith("p/" + encodeURIComponent("/Users/me/Code/foo"));
    expect(r.drafts.get("new:/Users/me/Code/foo")).toBe(machineCleanupPrompt());
    expect(r.drafts.get("new:/Users/me/Code/foo")).toBe("这台电脑磁盘空间不够了。帮我看看是什么占的，按占用从大到小列出来，说明哪些可以安全清理。先不要删除任何东西，等我确认。");
    expect(r.draftMachines.get("/Users/me/Code/foo")).toBe("mac");
    expect(sent.mock.calls.filter((call) => (call[1] as CommandKind | undefined)?.case === "createSession")).toHaveLength(0);
    // The new-session page shows it in the box, ready to be edited, not sent.
    act(() => root!.unmount());
    host.remove();
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={none} onBack={none} />);
    expect(host.querySelector<HTMLTextAreaElement>("textarea")!.value).toBe(machineCleanupPrompt());
  });

  it("adds the prompt after a draft already there, once: a draft that has it is left as it is", async () => {
    const r = demo("ms-full&ms=tight");
    const go = vi.fn();
    r.drafts.set("new:/Users/me/Code/foo", "先看看 node_modules");
    mount(<MachineStatusPage remote={r} id="mac" go={go} onBack={none} />);
    await ready();
    await click(button("让 Agent 帮我清理"));
    const once = "先看看 node_modules\n\n" + machineCleanupPrompt();
    expect(r.drafts.get("new:/Users/me/Code/foo")).toBe(once);
    await click(button("让 Agent 帮我清理"));
    expect(r.drafts.get("new:/Users/me/Code/foo")).toBe(once);
  });

  it("asks for a folder on this computer when none has been used", async () => {
    const r = demo("ms-full&ms=tight");
    r.machines.get("mac")!.meta!.projects = [];
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    await click(button("让 Agent 帮我清理"));
    expect(document.querySelector(".sheet")).toBeTruthy();
  });

  it("turns the card orange, with the actions, when space is only getting low", async () => {
    const r = demo("ms-full&ms=low");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    const disk = host.querySelectorAll(".ms-tile")[1];
    expect(disk.querySelector(".ms-lg")!.classList.contains("warn")).toBe(true);
    expect(disk.textContent).toContain("空间不多了");
    expect(disk.querySelector(".ms-say")!.innerHTML).toContain("共 460 GB<br>remote-agent 占 3.1 GB");
    expect(host.querySelector(".ms-rows")).toBeTruthy();
  });
});

describe("when the numbers cannot be read", () => {
  it("says so in the middle of the page, and asks again on 重试", async () => {
    const r = demo("ms=none");
    const ask = vi.spyOn(r, "getMachineStatus");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await vi.waitFor(() => expect(host.querySelector(".ms-fail")).toBeTruthy());
    expect(host.querySelector(".ms-fail")!.textContent).toBe("读不到这台电脑的状态重试");
    const before = ask.mock.calls.length;
    await click(button("重试"));
    await vi.waitFor(() => expect(ask.mock.calls.length).toBeGreaterThan(before));
  });

  it("keeps the last numbers, grey, with an orange dot and how old they are, then goes on by itself", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
    const r = demo("ms=stale&ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await act(async () => void (await vi.advanceTimersByTimeAsync(20_000)));
    const page = host.querySelector(".ms-page")!;
    expect(page.classList.contains("is-stale")).toBe(true);
    expect(host.querySelector(".ms-dot.off")).toBeTruthy();
    expect(host.querySelector(".ms-live")!.textContent).toMatch(/^\d+ 秒前的数据$/);
    // The numbers stay; the curve and its axis are gone.
    expect(host.querySelector(".ms-hero .ms-xl")!.textContent).toMatch(/\d/);
    expect(host.querySelector(".ms-plot")).toBeNull();
    expect(host.querySelector(".ms-axis")).toBeNull();
    await act(async () => void (await vi.advanceTimersByTimeAsync(100_000)));
    expect(host.querySelector(".ms-live")!.textContent).toMatch(/^1 分钟前的数据$/);
    // It keeps trying while it is in front.
    const ask = vi.spyOn(r, "getMachineStatus");
    await act(async () => void (await vi.advanceTimersByTimeAsync(6000)));
    expect(ask).toHaveBeenCalled();
  });

  it("goes stale at once when the computer goes offline", async () => {
    const r = demo("ms-full");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    act(() => {
      r.machines.get("mac")!.online = false;
      (r as unknown as { changed(): void }).changed();
    });
    await vi.waitFor(() => expect(host.querySelector(".ms-live")!.textContent).toMatch(/前的数据$/));
  });

  it("leaves out what the system does not give: no battery, load or pressure, no word of unknown", async () => {
    const r = demo("ms-full&ms-nobattery");
    const real = r.getMachineStatus.bind(r);
    r.getMachineStatus = async (...args) => {
      const s = await real(...args);
      s.memoryPressure = 0;
      s.swapUsedBytes = undefined;
      s.agentBytes = undefined;
      return s;
    };
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    expect(host.querySelector(".ms-foot")!.textContent).toBe("已开机 3 天 4 小时");
    expect(host.querySelector(".ms-hero .ms-aside")!.textContent).toBe("10 核");
    const memory = host.querySelectorAll(".ms-tile")[0];
    expect(memory.querySelector(".ms-uv")).toBeNull();
    expect(memory.querySelector(".ms-say")!.textContent).toBe("共 16 GB");
    expect(host.querySelectorAll(".ms-tile")[1].textContent).toContain("共 460 GB");
    expect(host.textContent).not.toMatch(/未知|unknown/i);
  });

  it("says there are no running sessions", async () => {
    const r = demo("ms-full");
    const real = r.getMachineStatus.bind(r);
    r.getMachineStatus = async (...args) => {
      const s = await real(...args);
      s.sessions = [];
      return s;
    };
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    expect(host.querySelector(".ms-list")!.textContent).toBe("没有正在运行的会话");
  });
});

describe("while the page is out of sight", () => {
  it("asks for nothing, and asks again when it is back", async () => {
    const r = demo("ms-full");
    const ask = vi.spyOn(r, "getMachineStatus");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    const seen = ask.mock.calls.length;
    await act(async () => {
      Object.defineProperty(document, "hidden", { configurable: true, value: true });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await new Promise((done) => setTimeout(done, 50));
    expect(ask.mock.calls.length).toBe(seen);
    await act(async () => {
      Object.defineProperty(document, "hidden", { configurable: true, value: false });
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await vi.waitFor(() => expect(ask.mock.calls.length).toBeGreaterThan(seen));
  });
});

describe("opening it from a notification or a link", () => {
  it("lands on the computer's status at #/machine-status/<id>, and the way back leaves it", async () => {
    const r = demo("ms-full");
    location.hash = "#/machine-status/mac";
    mount(<Workspace remote={r} />);
    await vi.waitFor(() => expect(host.querySelector(".ms-title")?.textContent).toBe("MacBook"));
    await ready();
    expect(host.querySelector(".ms-live")!.textContent).toBe("实时");
    location.hash = "";
  });
});

describe("the digits", () => {
  function Counter({ start }: { start: string }) {
    const [text, setText] = useState(start);
    return (
      <>
        <button id="to" onClick={() => setText((x) => (x === start ? "24" : start))} />
        <Roll text={text} />
      </>
    );
  }

  it("roll up where they grow and down where they shrink, and only the digits that changed", async () => {
    mount(<Counter start="23" />);
    await click(host.querySelector("#to"));
    const rolled = [...host.querySelectorAll(".ms-digit")];
    expect(rolled).toHaveLength(1);
    expect(rolled[0].classList.contains("up")).toBe(true);
    await click(host.querySelector("#to"));
    expect(host.querySelector(".ms-digit")!.classList.contains("down")).toBe(true);
    // Read once, in whole, for a screen reader.
    expect(host.querySelector(".ms-roll .ms-sr")!.textContent).toBe("23");
  });

  it("change at once when the system asks for less motion", async () => {
    reduced = true;
    mount(<Counter start="23" />);
    await click(host.querySelector("#to"));
    expect(host.querySelector(".ms-digit")).toBeNull();
    expect(host.querySelector(".ms-roll")!.textContent).toContain("24");
  });
});

describe("the reminder on the home", () => {
  it("shows one card for each computer under 10 GB, the tightest first, and opens its status", async () => {
    const r = demo("ms=both");
    const go = vi.fn();
    mount(<DiskReminders remote={r} go={go} />);
    const cards = [...host.querySelectorAll<HTMLElement>(".ms-alert")];
    expect(cards.map((c) => c.textContent)).toEqual(["磁盘快满了iMac 只剩 1.2 GB，会话可能启动不了", "磁盘空间不多了MacBook 还剩 8.2 GB"]);
    expect(cards[0].querySelector(".blk")!.classList.contains("crit")).toBe(true);
    expect(cards[1].querySelector(".blk")!.classList.contains("crit")).toBe(false);
    // No way to close one: it goes when the space comes back.
    expect(host.querySelectorAll(".ms-alert button, .ms-alert [aria-label]")).toHaveLength(0);
    await click(cards[1]);
    expect(go).toHaveBeenCalledWith("machine-status/mac");
  });

  it("is not shown for a computer that is offline, has enough space or cannot report", () => {
    const r = demo("ms=both");
    r.machines.get("imac")!.online = false;
    r.machines.get("mac")!.meta!.capabilities!.machineStatus = false;
    mount(<DiskReminders remote={r} go={none} />);
    expect(host.querySelectorAll(".ms-alert")).toHaveLength(0);
    const fine = demo("ms=ok");
    act(() => root!.render(<DiskReminders remote={fine} go={none} />));
    expect(host.querySelectorAll(".ms-alert")).toHaveLength(0);
  });

  it("goes when the space comes back", () => {
    const r = demo("ms=low");
    mount(<DiskReminders remote={r} go={none} />);
    expect(host.querySelectorAll(".ms-alert")).toHaveLength(1);
    act(() => {
      r.machines.get("mac")!.meta!.diskStatus!.availableBytes = 30_000_000_000n;
      (r as unknown as { changed(): void }).changed();
    });
    act(() => root!.render(<DiskReminders remote={r} go={none} />));
    expect(host.querySelectorAll(".ms-alert")).toHaveLength(0);
  });
});

describe("a start that fails for lack of space", () => {
  const typeAndSend = async () => {
    await act(async () => {
      const input = host.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${message("new_session.first_message")}"]`)!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, "加限流");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(host.querySelector<HTMLButtonElement>(`button[aria-label="${message("new_session.start")}"]`));
  };

  it("shows the card, not the raw error, and its two buttons", async () => {
    const r = demo("disk-full");
    const go = vi.fn();
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={go} onBack={none} />);
    await typeAndSend();
    await vi.waitFor(() => expect(host.querySelector(".ms-fail-card")).toBeTruthy());
    const card = host.querySelector<HTMLElement>(".ms-fail-card")!;
    expect(card.querySelector("b")!.textContent).toBe("这台电脑磁盘满了");
    expect(card.querySelector("p")!.textContent).toBe("会话没能启动。腾出一些空间后再试。");
    expect([...card.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["查看占用", "重试"]);
    expect(host.textContent).not.toMatch(/no space left|mkdir/);
    await click(button("查看占用", card));
    expect(go).toHaveBeenCalledWith("machine-status/mac");
    const asked = vi.spyOn(r, "machineCommand");
    await click(button("重试", card));
    await vi.waitFor(() => expect(asked.mock.calls.filter((call) => (call[1] as CommandKind | undefined)?.case === "createSession")).toHaveLength(1));
  });

  it("writes the other failures from lack of space as the prefix and the plain reason", () => {
    expect(new CommandError("disk_full", "write /x: no space left on device").message).toBe("这台电脑磁盘满了");
    expect(message("new_session.start_failed", { reason: new CommandError("disk_full", "raw").message })).toBe("启动失败：这台电脑磁盘满了");
    expect(new CommandError("storage_exhausted", "raw").message).toBe(message("machine_status.storage_exhausted"));
  });

  it("shows a quota failure as an ordinary error, not as a full disk", async () => {
    const r = demo();
    const real = r.machineCommand.bind(r);
    r.machineCommand = async (id, kind, ...rest) => {
      if (kind?.case === "createSession") throw new CommandError("storage_exhausted", "raw", true,false,"executed");
      return real(id, kind, ...rest);
    };
    mount(<ProjectPage remote={r} pkey="/Users/me/Code/foo" go={none} onBack={none} />);
    await typeAndSend();
    await vi.waitFor(() => expect(host.textContent).toContain(message("machine_status.storage_exhausted")));
    expect(host.querySelector(".ms-fail-card")).toBeNull();
  });
});

describe("the emergency reserve in the attachment storage", () => {
  it("is listed with its size and its note, and has no way to clear it", async () => {
    const r = demo("ms-full");
    mount(<MachinePage remote={r} id="mac" section="storage" go={none} onBack={none} />);
    await vi.waitFor(() => expect(host.textContent).toContain("应急预留"));
    const row = [...host.querySelectorAll(".row")].find((e) => e.textContent?.includes("应急预留"))!;
    expect(row.textContent).toBe("应急预留256.0 MiB磁盘写满时自动释放，让你还能开会话处理。");
    expect(row.querySelector("button")).toBeNull();
    // The rings are the top of the page.
    expect(host.querySelector(".body")!.firstElementChild!.classList.contains("ms-rings")).toBe(true);
  });

  it("is not listed for a computer that cannot report, nor once the daemon has released it", async () => {
    const r = demo();
    r.machines.get("mac")!.meta!.diskStatus!.emergencyReserveBytes = 0n;
    mount(<MachinePage remote={r} id="mac" go={none} onBack={none} />);
    await vi.waitFor(() => expect(host.textContent).toContain(message("settings.attachment_storage")));
    expect(host.textContent).not.toContain("应急预留");
    act(() => root!.render(<MachinePage remote={r} id="vps" go={none} onBack={none} />));
    await vi.waitFor(() => expect(host.textContent).toContain(message("settings.attachment_storage")));
    expect(host.textContent).not.toContain("应急预留");
  });
});

describe("in English", () => {
  it("writes the whole page in English", async () => {
    localeStore.overrideForTesting("en");
    const r = demo("ms-full&ms=tight");
    mount(<MachineStatusPage remote={r} id="mac" go={none} onBack={none} />);
    await ready();
    // Session titles are the user's own words; everything else is the product's.
    const copy = host.cloneNode(true) as HTMLElement;
    copy.querySelectorAll(".ms-rt > span:first-child").forEach((e) => e.remove());
    const text = copy.textContent!;
    for (const part of ["Live", "CPU", "Memory", "Disk", "Pressure high", "Almost full", "New sessions may not start", "Ask the Agent to clean up", "remote-agent usage", "Top usage", "Show processes", "2 min ago", "Now", "Up 3 d 4 hr", "Battery 81%, charging", "GB used", "16 GB total, 4.8 GB swap", "1.2GB free", "3.1 GB"])
      expect(text).toContain(part);
    expect(text).not.toMatch(/[一-鿿]/);
  });
});
