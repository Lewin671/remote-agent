// @vitest-environment happy-dom
// The session's status line says nothing of the connection until the lists
// are in: 未连接 for a moment and then the state would be a flash of the wrong word.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it } from "vitest";
import { DemoRemote } from "../dev/demo";
import { SessionPage } from "./Session";

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

it("shows no connection word before the lists are in", () => {
  open((r) => Object.assign(r, { conn: "connecting", listed: false, network: true, begun: true, waited: false }));
  expect(status()).not.toContain("未连接");
  expect(status()).not.toContain("空闲");
});

it("shows the state once the lists are in", () => {
  open((r) => Object.assign(r, { conn: "online", listed: true }));
  expect(status()).toContain("空闲");
});

it("still says 未连接 when the server cannot be reached", () => {
  open((r) => Object.assign(r, { conn: "offline", listed: false, network: true, begun: true, waited: true }));
  expect(status()).toContain("未连接");
});
