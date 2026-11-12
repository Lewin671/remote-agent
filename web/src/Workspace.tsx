import { lazy, startTransition, Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { Remote, SessionView } from "./remote";
import { Startup } from "./Startup";
import { NavBar, useRemote } from "./ui";
import { BottomBar, Home, Sidebar, WideHome, type Go } from "./views/Home";
import { ProjectPage } from "./views/Project";
import { ManagePage } from "./views/Projects";
import { SessionPage } from "./views/Session";
import {
  ClientPage, DevicesPage, MachinePage, PairCodePage, QuotaPage, RemovedScreen,
  ScheduleCreate, ScheduleDetail, SchedulesPage, SecurityPage, SettingsPage, inOwnFile,
} from "./views/pages";
import { taskRoute } from "./schedules";
import { Plus } from "./icons";

const Setup = lazy(() => import("./views/Pairing").then(m => ({ default: m.Setup })));
const loading = <Startup />;

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
  useLayoutEffect(() => { remote.setSchedulePage(route); }, [remote, route]);
  useEffect(() => { remote.syncSchedules(); });
  const wide = useWide();
  const scheduling = [...remote.machines.keys()].some((id) => remote.supportsSchedules(id));
  const [kind, ...rest] = route.split("/");
  const arg = decodeURIComponent(rest.join("/"));
  const session = kind === "s" ? (remote.sessions.get(arg) ?? remote.openingSessions.get(arg)) : undefined;
  const scheduleSession = session?.info?.taskOwned && session.info.scheduledTaskId ? taskRoute(session.machineId, session.info.scheduledTaskId) : undefined;
  const back = () => (history.length > 1 ? history.back() : go(""));
  const main = useMainScrolled(route, wide);
  // A session opened from a link may not be among the pages of the
  // listing held here: it is asked for by its ID.
  const absent =
    kind === "s" && !session && remote.listed && !remote.created.has(arg);
  useEffect(() => {
    if (absent) void remote.ensureSession(arg);
  });
  const opening = useMemo<SessionView>(() => ({ id: arg, machineId: "", metaRev: 0n, lastSeq: 0n, updatedMs: 0n, endedMs: 0n }), [arg]);
  if (remote.removed) return <Suspense fallback={loading}><RemovedScreen remote={remote} /></Suspense>;

  let page;
  if (kind === "s" && (session || !remote.missing.has(arg)))
    page = (
      <SessionPage
        key={arg}
        remote={remote}
        session={session ?? opening}
        pending={!session}
        onBack={session?.info?.taskOwned && session.info.scheduledTaskId ? () => go(taskRoute(session.machineId, session.info!.scheduledTaskId)) : back}
        wide={wide}
      />
    );
  else if (kind === "s")
    page = (
      <div className="page">
        <NavBar onBack={() => go("")} />
        <div className="center">
          <div>
            <div className="et">找不到这个会话</div>
            <div className="muted small">
              它可能已经被删除，或者属于一台被移除的电脑。
            </div>
            <div className="big">
              <button className="btn pri" onClick={() => go("")}>
                返回
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  else if (kind === "tasks") {
    const [machineId, taskId] = rest.map(decodeURIComponent);
    page = machineId === "new" ? <ScheduleCreate remote={remote} go={go} onBack={() => go("tasks")} />
      : taskId ? <ScheduleDetail key={machineId + taskId} remote={remote} machineId={machineId} id={taskId} go={go} onBack={() => go("tasks")} />
      // Selecting a task marks its results read; a wide window starts with none selected.
      : wide ? (
        <div className="center muted schedule-selection-empty">
          <span>选一个任务查看结果</span>
          <button className="btn sm" disabled={!scheduling || remote.unlinked} onClick={() => go("tasks/new")}>
            新定时
          </button>
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
    page = <MachinePage remote={remote} id={arg} go={go} onBack={back} />;
  else if (kind.startsWith("pair"))
    page = (
      <div className="center muted">
        这个浏览器已经加入了账户。要让新设备加入，在新设备上打开这个链接。
      </div>
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
  return (
    <div className="phone">
      <Suspense fallback={loading}>{page}</Suspense>
      {kind === "tasks" && !arg && (
        <BottomBar remote={remote} go={go} active="tasks" action={{ label: "新定时", icon: <Plus />, onClick: () => go("tasks/new"), disabled: !scheduling || remote.unlinked }} />
      )}
    </div>
  );
}
