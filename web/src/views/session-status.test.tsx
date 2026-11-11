// @vitest-environment happy-dom
// Nothing is said of the connection until the lists are in: the last state
// heard shows, where 未连接 and 电脑离线 for a moment and then the state would
// be a flash of the wrong word.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { DemoRemote } from "../dev/demo";
import { SessionPage } from "./Session";
import { SessionRow } from "./Home";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | undefined;
let host: HTMLElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
});

function open(setup: (r: DemoRemote) => void) {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  r.start();
  setup(r);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<SessionPage remote={r} session={r.sessions.get("s-idle")!} onBack={() => undefined} wide />));
  return r;
}
const status = () => host.querySelector(".dh .st")?.textContent ?? "";

it("shows the last state heard, and no connection word, before the lists are in", () => {
  open((r) => Object.assign(r, { conn: "connecting", listed: false, network: true, begun: true, waited: false }));
  expect(status()).not.toContain("未连接");
  expect(status()).not.toContain("电脑离线");
  expect(status()).toContain("空闲");
});

it("shows the state once the lists are in", () => {
  open((r) => Object.assign(r, { conn: "online", listed: true }));
  expect(status()).toContain("空闲");
});

it("still says 未连接 when the server cannot be reached", () => {
  open((r) => Object.assign(r, { conn: "offline", listed: false, network: true, begun: true, waited: true }));
  expect(status()).toContain("未连接");
});

describe("a list row", () => {
  function row(setup: (r: DemoRemote) => void) {
    const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
    r.start();
    for (const m of r.machines.values()) m.online = false; // as the copy kept in the browser says
    setup(r);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<SessionRow remote={r} s={r.sessions.get("s-idle")!} go={() => undefined} />));
    return host.textContent ?? "";
  }

  it("shows the last state heard, not 未连接 or 电脑离线, while the lists are on their way", () => {
    const text = row((r) => Object.assign(r, { conn: "connecting", listed: false, network: true, begun: true, waited: false }));
    expect(text).not.toContain("未连接");
    expect(text).not.toContain("电脑离线");
    expect(text).toContain("空闲");
  });

  it("says 电脑离线 once the lists are in and the computer is not", () => {
    expect(row((r) => Object.assign(r, { conn: "online", listed: true }))).toContain("电脑离线");
  });

  it("says 未连接 when the server cannot be reached", () => {
    expect(row((r) => Object.assign(r, { conn: "offline", listed: false, network: true, begun: true, waited: true }))).toContain("未连接");
  });
});

it("counts a computer as the copy kept in the browser had it until the lists are in", () => {
  const r = DemoRemote.create(new URLSearchParams(), async () => undefined);
  r.start();
  const m = [...r.machines.values()][0];
  Object.assign(m, { online: false, keptOnline: true });
  Object.assign(r, { listed: false });
  expect(r.onlineOf(m)).toBe(true);
  expect(r.presence.get(m.id)).toBeUndefined();
  Object.assign(r, { listed: true });
  expect(r.onlineOf(m)).toBe(false);
  expect(r.presence.get(m.id)).toBe(m);
});
