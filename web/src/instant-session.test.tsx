// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "./dev/demo";
import Workspace from "./Workspace";
import { SessionPage } from "./views/Session";
import type { SessionView } from "./remote";
import { setView } from "./view";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(() => { act(() => root?.unmount()); host?.remove(); root = undefined; history.replaceState(null, "", "/"); localStorage.clear(); vi.restoreAllMocks(); });
it("keeps the same input, draft and focus when an unresolved route becomes a verified session", async () => {
  setView("term");
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  const known = [...remote.sessions.values()].find(s => s.info && !s.endedMs)!;
  const opening: SessionView = { id: known.id, machineId: '', metaRev: 0n, lastSeq: 0n, updatedMs: 0n, endedMs: 0n };
  vi.spyOn(remote, 'history').mockReturnValue({ loaded: false, ready: false, error: '', broken: false });
  host = document.createElement('div'); document.body.append(host); root = createRoot(host);
  const render = (session: SessionView, pending: boolean) => act(() => root!.render(<SessionPage remote={remote} session={session} pending={pending} wide={false} onBack={() => undefined} />));
  render(opening, true);
  const input = host.querySelector<HTMLTextAreaElement>('.draft')!;
  expect(input).not.toBeNull(); expect(input.disabled).toBe(false);
  await act(async () => { remote.drafts.set(known.id, 'typed during lookup'); remote.touch(); });
  input.focus();
  expect(host.querySelector<HTMLButtonElement>('.snd')!.disabled).toBe(true);
  render(known, false);
  expect(host.querySelector('.draft')).toBe(input);
  expect(input.value).toBe('typed during lookup');
  expect(document.activeElement).toBe(input);
  expect(host.textContent).not.toContain('正在打开会话');
  expect(host.querySelector('.navbar')?.textContent).toContain(known.info!.title);
});

it("draws an unresolved link as a chat page before lookup returns", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.listed = true;
  const lookup = vi.spyOn(remote, "ensureSession").mockImplementation(() => new Promise(() => undefined));
  history.replaceState(null, "", "/#/s/linked-session");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<Workspace remote={remote} />));
  expect(lookup).toHaveBeenCalledWith("linked-session");
  expect(host.querySelector(".session .navbar, .session .dh")).not.toBeNull();
  expect(host.querySelector<HTMLTextAreaElement>(".draft")!.disabled).toBe(false);
  expect(host.querySelector<HTMLButtonElement>(".snd")!.disabled).toBe(true);
  expect(host.textContent).not.toContain("正在打开会话");
});

it("shows a verified session even if an earlier lookup marked it missing", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  const session = [...remote.sessions.values()].find(s => s.info && !s.endedMs)!;
  remote.missing.add(session.id);
  history.replaceState(null, "", "/#/s/" + session.id);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  await act(async () => root!.render(<Workspace remote={remote} />));
  expect(host.querySelector(".session")).not.toBeNull();
  expect(host.textContent).not.toContain("找不到这个会话");
});
