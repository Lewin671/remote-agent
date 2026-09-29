import { useEffect, useState, useSyncExternalStore } from "react";
import { Remote } from "./remote";
import { NavBar, useRemote } from "./ui";
import { Home, Sidebar, todoList, WideHome, type Go } from "./views/Home";
import { AddDevicePage, Setup } from "./views/Pairing";
import { ProjectPage } from "./views/Project";
import { SessionPage } from "./views/Session";
import { MachinePage, Settings } from "./views/Settings";

// ?demo in development shows made-up machines and sessions in every state
// the mockup draws, with no server. It is compiled out of builds.
const demo = import.meta.env.DEV && new URLSearchParams(location.search).has("demo");

async function loadRemote(): Promise<Remote | undefined> {
  if (import.meta.env.DEV && demo) {
    const { DemoRemote } = await import("./dev/demo");
    return DemoRemote.create(new URLSearchParams(location.search));
  }
  return Remote.load();
}

export function App() {
  const [remote, setRemote] = useState<Remote | null | undefined>(undefined);
  const load = () =>
    loadRemote().then(
      (r) => setRemote(r ?? null),
      () => setRemote(null),
    );
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (!remote) return;
    remote.start();
    return () => remote.stop();
  }, [remote]);
  if (remote === undefined) return <div className="center muted">加载中…</div>;
  if (remote === null) return <Setup onDone={() => void load()} />;
  return <Main remote={remote} />;
}

/** Routes live in the hash: #/, #/p/<project>, #/s/<session>, #/settings,
 * #/m/<machine>, #/add. */
function useRoute(): [string, Go] {
  const read = () => location.hash.replace(/^#\/?/, "");
  const [route, setRoute] = useState(read);
  useEffect(() => {
    const on = () => setRoute(read());
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return [route, (r: string) => (location.hash = "/" + r)];
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

function Main({ remote }: { remote: Remote }) {
  useRemote(remote);
  const [route, go] = useRoute();
  const wide = useWide();
  const [kind, ...rest] = route.split("/");
  const arg = decodeURIComponent(rest.join("/"));
  const session = kind === "s" ? remote.sessions.get(arg) : undefined;
  const back = () => (history.length > 1 ? history.back() : go(""));

  let page;
  if (session) page = <SessionPage key={session.id} remote={remote} session={session} onBack={back} wide={wide} />;
  else if (kind === "s")
    page =
      remote.listed && !remote.created.has(arg) ? (
        <div className="page">
          <NavBar onBack={() => go("")} />
          <div className="center">
            <div>
              <div className="et">找不到这个会话</div>
              <div className="muted small">它可能已经被删除，或者属于一台被移除的电脑。</div>
              <div className="big">
                <button className="btn pri" onClick={() => go("")}>
                  返回
                </button>
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className="center muted">正在打开会话…</div>
      );
  else if (kind === "p") page = <ProjectPage key={arg} remote={remote} pkey={arg} go={go} onBack={back} wide={wide} />;
  else if (kind === "settings") page = <Settings remote={remote} go={go} />;
  else if (kind === "m") page = <MachinePage remote={remote} id={arg} go={go} onBack={back} />;
  else if (kind === "add") page = <AddDevicePage remote={remote} onBack={back} />;
  else if (kind.startsWith("pair")) page = <div className="center muted">这个浏览器已经加入了账户。要让另一台设备加入，在它上面打开这个链接。</div>;
  else page = wide ? <WideHome remote={remote} go={go} /> : <Home remote={remote} go={go} />;

  if (wide)
    return (
      <div className="desk">
        <Sidebar remote={remote} go={go} selected={session?.id} project={kind === "p" ? arg : undefined} />
        <main className="dm">{page}</main>
      </div>
    );
  const tabs = kind === "" || kind === "settings";
  const todo = todoList(remote).length;
  return (
    <div className={"phone" + (kind === "settings" ? " gray" : "")}>
      {page}
      {tabs && (
        <nav className="tabbar">
          <button className={kind === "" ? "on" : ""} onClick={() => go("")}>
            <i>▦</i>会话
            {todo > 0 && <span className="n">{todo}</span>}
          </button>
          <button className={kind === "settings" ? "on" : ""} onClick={() => go("settings")}>
            <i>⚙︎</i>设置
          </button>
        </nav>
      )}
    </div>
  );
}
