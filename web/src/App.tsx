import { useEffect, useState } from "react";
import { Remote } from "./remote";
import { useRemote } from "./ui";
import { Home, ProjectPage, Sidebar, WideHome, type Go } from "./views/Lists";
import { SessionPage } from "./views/Session";
import { Settings, Setup } from "./views/Settings";

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

function useWide() {
  const q = matchMedia("(min-width: 900px)");
  const [wide, setWide] = useState(q.matches);
  useEffect(() => {
    const on = () => setWide(q.matches);
    q.addEventListener("change", on);
    return () => q.removeEventListener("change", on);
  }, [q]);
  return wide;
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
  else if (kind === "settings") page = <Settings remote={remote} />;
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
