import { t as message } from "./i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { getCoreHost, sessionDraftScope } from "./core/host";
import { DemoRemote } from "./dev/demo";
import { patchSyncState } from "./test/remote-fixture";
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
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.start();
  const known = [...remote.sessions.values()].find((s) => s.info && !s.endedMs && remote.machines.get(s.machineId)?.online)!;
  const openingId = "opening-route-session";
  const origRpc = remote.executeRpc.bind(remote);
  vi.spyOn(remote, "executeRpc").mockImplementation((eff) =>
    eff.kind.case === "fetchSessionById" ? new Promise<never>(() => undefined) : origRpc(eff),
  );
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
    const nextSessions = new Map(remote.sessions);
    nextSessions.set(openingId, {
      ...known,
      id: openingId,
      info: { ...known.info!, id: openingId },
    });
    patchSyncState(remote, { sessions: nextSessions });
  });
  expect(host.querySelector(".draft")).toBe(input);
  expect(input.value).toBe("typed during lookup");
  expect(document.activeElement).toBe(input);
  expect(host.querySelector(".navbar")?.textContent).toContain(known.info!.title);
});

it("draws an unresolved link as a chat page before lookup returns", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.start();
  patchSyncState(remote, { listed: true });
  const origRpc = remote.executeRpc.bind(remote);
  vi.spyOn(remote, "executeRpc").mockImplementation((eff) =>
    eff.kind.case === "fetchSessionById" ? new Promise<never>(() => undefined) : origRpc(eff),
  );
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
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.start();
  patchSyncState(remote, { listed: true });
  const origRpc = remote.executeRpc.bind(remote);
  let failLookup = true;
  const { create } = await import("@bufbuild/protobuf");
  const { EventSchema, SessionByIdFetchedEventSchema } = await import("./gen/remoteagent/core/v1/app_pb");
  vi.spyOn(remote, "executeRpc").mockImplementation(async (eff) => {
    if (eff.kind.case === "fetchSessionById") {
      return create(EventSchema, {
        event: {
          case: "sessionByIdFetched",
          value: create(SessionByIdFetchedEventSchema, {
            opId: eff.opId,
            error: failLookup ? "rpc timeout" : "",
          }),
        },
      });
    }
    return origRpc(eff);
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<SessionPage remote={remote} sessionId="missing-one" wide={false} onBack={() => undefined} />));
  expect(host.textContent).toContain("rpc timeout");
  const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === message("common.retry"))!;
  expect(retry).toBeTruthy();
  failLookup = false;
  await act(async () => retry.click());
  expect(host.textContent).toContain(message("session.ios_missing"));
});

it("shows a verified session in Workspace", async () => {
  const remote = DemoRemote.create(new URLSearchParams(), async () => undefined);
  remote.start();
  const session = [...remote.sessions.values()].find((s) => s.info && !s.endedMs)!;
  history.replaceState(null, "", "/#/s/" + session.id);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Workspace remote={remote} />));
  expect(host.querySelector(".session")).not.toBeNull();
  expect(host.textContent).not.toContain(message("session.ios_missing"));
});
