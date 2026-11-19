import * as l10n from "./i18n";
import { startTransition, Suspense, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { Remote } from "./remote";
import { useRemote } from "./ui";
import { BottomBar, Home, Sidebar, WideHome, type Go } from "./views/Home";
import { ProjectPage } from "./views/Project";
import { ManagePage } from "./views/Projects";
import { SessionPage } from "./views/Session";
import {
  ClientPage, DevicesPage, MachinePage, MachineStatusPage, PairCodePage, QuotaPage, RemovedScreen,
  ScheduleCreate, ScheduleDetail, SchedulesPage, SecurityPage, SettingsPage, UsagePage, inOwnFile, page,
} from "./views/pages";
import { routeForTask, useCoreHost, useSchedulesView } from "./core/host";
import { Plus } from "./icons";

const Setup = page(() => import("./views/Pairing").then(m => ({ default: m.Setup })));
const loading = <div className="page" aria-hidden="true" />;

function parseRoute(route: string) {
  const [kind, ...rest] = route.split("/");
  const arg = decodeURIComponent(rest.join("/"));
  const sub = rest[0] ? decodeURIComponent(rest[0]) : "";
  const isTab = !kind || (kind === "tasks" && !sub);
  const isSheet = kind === "p" || (kind === "tasks" && sub === "new");
  const depth = isTab ? 0 : kind === "settings" ? (sub ? 2 : 1) : kind === "devices" ? 2 : kind === "projects" || kind === "s" || kind === "p" || kind === "tasks" ? 1 : 3;
  return { kind, rest, arg, isTab, isSheet, depth };
}

/** Routes live in the hash: #/, #/p/<project>, #/s/<session>, #/settings,
 * #/m/<machine>. */
function useRoute(): [string, Go] {
  const read = () => location.hash.replace(/^#\/?/, "");
  const [route, setRoute] = useState(read);
  useEffect(() => {
    // A page in a file of its own: the one being left stays until it is here.
    // Any other route changes at once, not at a transition's pace.
    const on = () => {
      const next = read();
      if (inOwnFile(next)) startTransition(() => setRoute(next));
      else setRoute(next);
    };
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return [route, (r: string, replace?: boolean) => {
    if (replace) location.replace(location.pathname + location.search + "#/" + r);
    else location.hash = "/" + r;
  }];
}

const wideQuery = matchMedia("(min-width: 900px)");

function subscribeWide(fn: () => void) {
  wideQuery.addEventListener("change", fn);
  return () => wideQuery.removeEventListener("change", fn);
}

/** Whether the window is wide enough for the sidebar layout. */
function useWide() {
  return useSyncExternalStore(subscribeWide, () => wideQuery.matches);
}

/** The wide main area's toolbar gets a hairline under it once the page
 * beneath has scrolled (the stylesheet draws it over the content, so it takes
 * no room): data-scrolled is set while the page's own scroller is not at its
 * top, and cleared when the route changes, as a new page starts at the top. */
function useMainScrolled(route: string, wide: boolean) {
  const main = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = main.current;
    if (!el) return;
    el.removeAttribute("data-scrolled");
    const on = (e: Event) => {
      const t = e.target;
      // Only a page with a toolbar over its scroller has a line to show.
      if (t instanceof HTMLElement && t.classList.contains("body") && el.contains(t) && el.querySelector(".dh, .navbar"))
        t.scrollTop > 0 ? el.setAttribute("data-scrolled", "") : el.removeAttribute("data-scrolled");
    };
    el.addEventListener("scroll", on, true);
    return () => el.removeEventListener("scroll", on, true);
  }, [route, wide]);
  return main;
}

export default function Workspace({ remote }: { remote: Remote }) {
  useRemote(remote);
  const [route, go] = useRoute();
  const host = useCoreHost(remote, go);
  const schedulesVm = useSchedulesView(host, 1);
  const wide = useWide();
  const scheduling = schedulesVm.canCreate;
  const parsed = parseRoute(route);
  const { kind, rest, arg } = parsed;
  const session = kind === "s" ? remote.sessions.get(arg) : undefined;
  const scheduleSession = session?.info?.taskOwned && session.info.scheduledTaskId ? routeForTask(session.machineId, session.info.scheduledTaskId) : undefined;
  useEffect(() => {
    if (kind === "tasks" && wide && rest[0] !== "new" && !rest[1]) host.scheduleSelectTask("", "");
  }, [host, kind, wide, rest[0], rest[1]]);
  const back = () => (history.length > 1 ? history.back() : go(""));
  const main = useMainScrolled(route, wide);
  const prevRouteRef = useRef(parsed);
  const navDirRef = useRef("" as "" | "fwd" | "back");
  if (prevRouteRef.current.kind !== kind || prevRouteRef.current.arg !== arg) {
    const prev = prevRouteRef.current;
    prevRouteRef.current = parsed;
    navDirRef.current =
      (prev.isTab && parsed.isTab) || parsed.isSheet ? ""
      : prev.isSheet ? "back"
      : parsed.depth >= prev.depth ? "fwd" : "back";
  }
  if (remote.removed) return <Suspense fallback={loading}><RemovedScreen remote={remote} /></Suspense>;

  let page;
  if (kind === "s")
    page = (
      <SessionPage
        key={arg}
        remote={remote}
        sessionId={arg}
        onBack={back}
        wide={wide}
        go={go}
      />
    );
  else if (kind === "tasks") {
    const [machineId, taskId] = rest.map(decodeURIComponent);
    page = machineId === "new" ? <ScheduleCreate remote={remote} go={go} onBack={() => go("tasks")} />
      : taskId ? <ScheduleDetail key={machineId + taskId} remote={remote} machineId={machineId} id={taskId} go={go} onBack={() => go("tasks")} />
      // Selecting a task marks its results read; a wide window starts with none selected.
      : wide ? (
        <div className="center muted schedule-selection-empty">
          <span>{l10n.t("home.ios_choose_task")}</span>
          <button className="btn sm" disabled={!scheduling} onClick={() => go("tasks/new")}>
            {l10n.t("home.new_schedule")}</button>
        </div>
      ) : <SchedulesPage remote={remote} go={go} />;
  }
  // A wide window has no project page: its sidebar lists sessions and the main area starts one.
  else if (kind === "p")
    page = wide ? (
      <WideHome key={arg} remote={remote} go={go} project={arg} />
    ) : (
      <ProjectPage key={arg} remote={remote} pkey={arg} go={go} onBack={back} />
    );
  else if (kind === "projects")
    page = <ManagePage remote={remote} go={go} onBack={back} />;
  else if (kind === "settings" && arg === "rejoin") page = <Suspense fallback={loading}><Setup existing={remote.account} onDone={() => location.reload()} /></Suspense>;
  else if (kind === "settings" && arg === "security") page = <SecurityPage remote={remote} onBack={back} />;
  else if (kind === "settings" && arg.startsWith("add-device")) page = <PairCodePage remote={remote} replaces={arg.split("/")[1]} onBack={back} />;
  else if (kind === "settings") page = <SettingsPage remote={remote} go={go} onBack={wide ? undefined : () => go("")} />;
  else if (kind === "devices")
    page = <DevicesPage remote={remote} go={go} onBack={back} />;
  else if (kind === "q")
    page = <QuotaPage remote={remote} id={arg} go={go} onBack={back} />;
  else if (kind === "c")
    page = <ClientPage remote={remote} id={arg} onBack={back} />;
  else if (kind === "m")
    page = <MachinePage remote={remote} id={arg.split("/")[0]} section={arg.split("/")[1]} go={go} onBack={back} />;
  else if (kind === "machine-status")
    page = <MachineStatusPage key={arg} remote={remote} id={arg} go={go} onBack={back} />;
  else if (kind === "u") {
    const [machineRaw = "", agentRaw = "", modelRaw = ""] = rest;
    const decode = (s: string) => { try { return decodeURIComponent(s); } catch { return s; } };
    const machineId = decode(machineRaw);
    const parsedAgent = agentRaw ? Number(decode(agentRaw)) : 0;
    const focusAgent = Number.isFinite(parsedAgent) ? (parsedAgent as import("./gen/remoteagent/v1/event_pb").Agent) : undefined;
    const focusModel = modelRaw ? decode(modelRaw) : undefined;
    page = <UsagePage key={`${machineId}/${focusAgent ?? ""}/${focusModel ?? ""}`} remote={remote} machineId={machineId} focusAgent={focusAgent} focusModel={focusModel} wide={wide} go={go} onBack={back} />;
  }
  else if (kind.startsWith("pair"))
    page = (
      <div className="center muted">
        {l10n.t("errors.this_browser_has_already_joined_an_account_open_this")}</div>
    );
  else
    page = wide ? (
      <WideHome remote={remote} go={go} />
    ) : (
      <Home remote={remote} go={go} />
    );

  if (wide)
    return (
      <div className="desk">
        <Sidebar
          remote={remote}
          go={go}
          active={kind === "tasks" || scheduleSession ? "tasks" : kind === "settings" ? "settings" : "sessions"}
          selectedTask={scheduleSession ?? route}
          selected={session?.id}
          project={kind === "p" ? arg : undefined}
        />
        <main className="dm" ref={main}><Suspense fallback={loading}>{page}</Suspense></main>
      </div>
    );
  // The home draws its own bottom row; the task list gets the same one,
  // with 新定时 on the right.
  const navDir = navDirRef.current;
  const stageClass = navDir ? `phone-stage mo-page-${navDir}` : "phone-stage";
  const stageKey = parsed.isTab ? `tab-${kind}` : route;
  return (
    <div className="phone">
      <div key={stageKey} className={stageClass}>
        <Suspense fallback={loading}>{page}</Suspense>
      </div>
      {kind === "tasks" && !arg && (
        <BottomBar remote={remote} go={go} active="tasks" action={{ label: l10n.t("home.new_schedule"), icon: <Plus />, onClick: () => go("tasks/new"), disabled: !scheduling }} />
      )}
    </div>
  );
}
