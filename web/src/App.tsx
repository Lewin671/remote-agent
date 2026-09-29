import { useEffect, useState, useSyncExternalStore } from "react";
import { Remote } from "./remote";
import { useRemote } from "./ui";
import { Home, ProjectPage, Sidebar, WideHome, type Go } from "./views/Lists";
import { SessionPage } from "./views/Session";
import { AddDevicePage, Setup } from "./views/Pairing";
import { MachinePage, Settings } from "./views/Settings";

export function App() {
  const [remote, setRemote] = useState<Remote | null | undefined>(undefined);
  const load = () =>
    Remote.load().then(
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

/** Routes live in the hash: #/, #/p/<project>, #/s/<session>, #/settings. */
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
  addEventListener("resize", fn);
  return () => {
    wideQuery.removeEventListener("change", fn);
    removeEventListener("resize", fn);
  };
}

/** Whether the window is wide enough for the sidebar layout. */
function useWide() {
  return useSyncExternalStore(subscribeWide, () => wideQuery.matches);
}

function Main({ remote }: { remote: Remote }) {
  useRemote(remote);
  const [route, go] = useRoute();
  const wide = useWide();
  const [kind, arg] = [route.split("/")[0], decodeURIComponent(route.split("/").slice(1).join("/"))];
  const session = kind === "s" ? remote.sessions.get(arg) : undefined;
  const back = () => (history.length > 1 ? history.back() : go(""));

  let page;
  if (session) page = <SessionPage key={session.id} remote={remote} session={session} onBack={back} wide={wide} />;
  else if (kind === "s") page = <div className="center muted">正在读取会话…</div>;
  else if (kind === "p") page = <ProjectPage remote={remote} name={arg} go={go} onBack={back} />;
  else if (kind === "settings") page = <Settings remote={remote} go={go} />;
  else if (kind === "m") page = <MachinePage remote={remote} id={arg} go={go} onBack={back} />;
  else if (kind === "add") page = <AddDevicePage remote={remote} onBack={back} />;
  else if (kind.startsWith("pair")) page = <div className="center muted">这个浏览器已经加入了账户。要让另一台设备加入，在它上面打开这个链接。</div>;
  else page = wide ? <WideHome remote={remote} go={go} /> : <Home remote={remote} go={go} />;

  if (wide) {
    return (
      <div className="desk">
        <Sidebar remote={remote} go={go} selected={session?.id} />
        <main className="dm">{page}</main>
      </div>
    );
  }
  const tabs = kind === "" || kind === "settings";
  return (
    <div className="phone">
      {page}
      {tabs && (
        <nav className="tabbar">
          <button className={kind === "" ? "on" : ""} onClick={() => go("")}>
            <i>▦</i>项目
          </button>
          <button className={kind === "settings" ? "on" : ""} onClick={() => go("settings")}>
            <i>⚙︎</i>设置
          </button>
        </nav>
      )}
    </div>
  );
}
