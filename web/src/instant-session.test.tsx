import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { Code, ConnectError } from "@connectrpc/connect";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost, sessionDraftScope } from "./core/host";
import { DemoRemote } from "./dev/demo";
import { SyntheticRemote, patchSyncState, demoSessions } from "./test/remote-fixture";
import Workspace from "./Workspace";
import { SessionPage } from "./views/Session";
import { setView } from "./view";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined, host: HTMLDivElement;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  history.replaceState(null, "", "/");
  localStorage.clear();
  vi.restoreAllMocks();
});

it("keeps the same input, draft and focus across Core session updates", async () => {
  setView("term");
  const remote = await SyntheticRemote.create();
  const openingId = "opening-route-session";
  const pendingTitle = "Resolved session title";
  const resolvedRow = await remote.sessionRow(openingId, pendingTitle);
  let releaseLookup!: () => void;
  (remote as unknown as { api: Record<string, unknown> }).api = {
    listRemoteSessions: async (req: { sessionIds?: string[] }) => {
      if (req.sessionIds?.length) {
        await new Promise<void>((res) => {
          releaseLookup = res;
        });
        return { sessions: [resolvedRow] };
      }
      return { sessions: [] };
    },
  };
  patchSyncState(remote, { conn: "online" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<SessionPage remote={remote} sessionId={openingId} wide={false} onBack={() => undefined} />));
  const input = host.querySelector<HTMLTextAreaElement>(".draft")!;
  expect(input).not.toBeNull();
  expect(input.disabled).toBe(false);
  await act(async () => {
    getCoreHost(remote).draftSetText(sessionDraftScope(openingId), "typed during lookup");
  });
  input.focus();
  expect(host.querySelector<HTMLButtonElement>(".snd")!.disabled).toBe(true);
  await act(async () => {
    releaseLookup();
  });
  await vi.waitFor(() => expect(host.querySelector(".navbar")?.textContent).toContain(pendingTitle));
  expect(host.querySelector(".draft")).toBe(input);
  expect(input.value).toBe("typed during lookup");
  expect(document.activeElement).toBe(input);
});

it("draws an unresolved link as a chat page before lookup returns", async () => {
  const remote = await SyntheticRemote.create();
  (remote as unknown as { api: Record<string, unknown> }).api = {
    listRemoteSessions: async (req: { sessionIds?: string[] }) =>
      req.sessionIds?.length ? new Promise<never>(() => undefined) : { sessions: [] },
  };
  patchSyncState(remote, { conn: "online" });
  const core = getCoreHost(remote);
  const openSpy = vi.spyOn(core, "sessionOpen");
  history.replaceState(null, "", "/#/s/linked-session");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Workspace remote={remote} />));
  expect(openSpy).toHaveBeenCalledWith("linked-session", true, true);
  expect(host.querySelector(".session .navbar, .session .dh")).not.toBeNull();
  expect(host.querySelector<HTMLTextAreaElement>(".draft")!.disabled).toBe(false);
  expect(host.textContent).not.toContain("正在打开会话");
});

it("distinguishes lookup failure (with retry) from missing session", async () => {
  const remote = await SyntheticRemote.create();
  let failLookup = true;
  (remote as unknown as { api: Record<string, unknown> }).api = {
    listRemoteSessions: async (req: { sessionIds?: string[] }) => {
      if (req.sessionIds?.length) {
        if (failLookup) throw new ConnectError("rpc timeout", Code.Unavailable);
        return { sessions: [] };
      }
      return { sessions: [] };
    },
  };
  patchSyncState(remote, { conn: "online" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<SessionPage remote={remote} sessionId="missing-one" wide={false} onBack={() => undefined} />));
  await vi.waitFor(() => expect(host.textContent).toContain("rpc timeout"));
  const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === message("common.retry"))!;
  expect(retry).toBeTruthy();
  failLookup = false;
  await act(async () => retry.click());
  await vi.waitFor(() => expect(host.textContent).toContain(message("session.ios_missing")));
});

it("shows a verified session in Workspace", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.start();
  const session = [...demoSessions(remote).values()].find((s) => s.info && !s.endedMs)!;
  history.replaceState(null, "", "/#/s/" + session.id);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Workspace remote={remote} />));
  expect(host.querySelector(".session")).not.toBeNull();
  expect(host.textContent).not.toContain(message("session.ios_missing"));
});
