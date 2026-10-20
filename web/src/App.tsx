import { usePageVisible } from "./ui";
import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { SetupReset } from "./SetupReset";
import { Remote, resumeRegistration, errorText } from "./remote";
import { loadAccount, type StoredAccount, watchAccountLogout } from "./storage";
import { securitySetupStatus } from "./device-security-store";
import { NavBar, useRemote } from "./ui";
import { Home, Sidebar, todoList, WideHome, type Go } from "./views/Home";
import { ProjectPage } from "./views/Project";
import { ManagePage } from "./views/Projects";
import { SessionPage } from "./views/Session";
import {
  ClientPage,
  DevicesPage,
  MachinePage,
  NameClientPrompt,
  QuotaPage,
  RemovedScreen,
  Settings,
} from "./views/Settings";
import { MessageSquare, Settings as SettingsIcon } from "./icons";

// Joining and pairing bring the QR code and curve code; fetched when used.
const Setup = lazy(() =>
  import("./views/Pairing").then((m) => ({ default: m.Setup })),
);
const loading = <div className="center muted">加载中…</div>;

// ?demo in development shows made-up machines and sessions in every state
// the mockup draws, with no server. It is compiled out of builds.
const demo =
  import.meta.env.DEV && new URLSearchParams(location.search).has("demo");

async function loadRemote(): Promise<Remote | undefined> {
  if (import.meta.env.DEV && demo) {
    const { DemoRemote } = await import("./dev/demo");
    return DemoRemote.create(new URLSearchParams(location.search));
  }
  return Remote.load();
}

export function App() {
  const visible = usePageVisible();
  const [remote, setRemote] = useState<Remote | null | undefined>(undefined);
  const [retained, setRetained] = useState<StoredAccount>();
  const [setupError, setSetupError] = useState("");
  const current = useRef(remote);
  current.current = remote;
  const load = async () => {
    try {
      setSetupError("");
      if (!demo) await resumeRegistration();
      const account = await loadAccount();
      if (account && !demo) {
        const status = await securitySetupStatus();
        if (status === "missing") {
          setRetained(account);
          setRemote(null);
          return;
        }
        if (status === "clock") {
          setSetupError(
            "本机时间比账户状态慢。请校准系统时间和时区，否则远程操作会被拒绝。",
          );
          setRemote(null);
          return;
        }
        if (status === "invalid") {
          setSetupError(
            "本机安全状态无法校验，远程操作已暂停。原密钥和历史已保留，请检查本机存储。",
          );
          setRemote(null);
          return;
        }
      }
      setRetained(undefined);
      setRemote((await loadRemote()) ?? null);
    } catch (error) {
      setSetupError(
        errorText(error) +
          "。原请求和密钥已保留，可重试；若批准已过期或被拒绝，可退出并重新配对或创建。",
      );
      setRemote(null);
    }
  };
  useEffect(
    () =>
      watchAccountLogout(() => {
        current.current?.stop();
        location.hash = "";
        location.reload();
      }),
    [],
  );
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (!remote || !visible) return;
    remote.start();
    const online = () => {
      remote.setNetwork(true);
      remote.reconnectNow(true);
    };
    const offline = () => remote.setNetwork(false);
    remote.setNetwork(navigator.onLine);
    addEventListener("online", online);
    addEventListener("offline", offline);
    return () => {
      removeEventListener("online", online);
      removeEventListener("offline", offline);
      remote.stop();
    };
  }, [remote, visible]);
  // Reading what this browser kept takes a moment; the words come only if it takes long.
  if (remote === undefined)
    return <div className="center muted late">加载中…</div>;
  if (setupError)
    return (
      <div className="center">
        <p>{setupError}</p>
        <button onClick={() => void load()}>重试</button>
        <SetupReset onDone={() => void load()} />
      </div>
    );
  if (remote === null)
    return (
      <Suspense fallback={loading}>
        <Setup existing={retained} onDone={() => void load()} />
      </Suspense>
    );
  return <Main remote={remote} />;
}

/** Routes live in the hash: #/, #/p/<project>, #/s/<session>, #/settings,
 * #/m/<machine>. */
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
  // A session opened from a link may not be among the pages of the
  // listing held here: it is asked for by its ID.
  const absent =
    kind === "s" && !session && remote.listed && !remote.created.has(arg);
  useEffect(() => {
    if (absent) void remote.ensureSession(arg);
  });
  if (remote.removed) return <RemovedScreen remote={remote} />;

  let page;
  if (session)
    page = (
      <SessionPage
        key={session.id}
        remote={remote}
        session={session}
        onBack={back}
        wide={wide}
      />
    );
  else if (kind === "s")
    page =
      absent && remote.missing.has(arg) ? (
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
      ) : (
        <div className="center muted">正在打开会话…</div>
      );
  // A wide window has no project page: the sidebar lists the project and the main area starts a session in it.
  else if (kind === "p")
    page = wide ? (
      <WideHome key={arg} remote={remote} go={go} project={arg} />
    ) : (
      <ProjectPage key={arg} remote={remote} pkey={arg} go={go} onBack={back} />
    );
  else if (kind === "projects")
    page = <ManagePage remote={remote} go={go} onBack={back} wide={wide} />;
  else if (kind === "settings") page = <Settings remote={remote} go={go} />;
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
        这个浏览器已经加入了账户。要让另一台设备加入，在它上面打开这个链接。
      </div>
    );
  else
    page = wide ? (
      <WideHome remote={remote} go={go} />
    ) : (
      <Home remote={remote} go={go} />
    );

  // Asked on the home and settings pages, not over a session being read.
  const naming = (kind === "" || kind === "settings") && (
    <NameClientPrompt remote={remote} />
  );
  if (wide)
    return (
      <div className="desk">
        <Sidebar
          remote={remote}
          go={go}
          selected={session?.id}
          project={kind === "p" ? arg : undefined}
        />
        <main className="dm">{page}</main>
        {naming}
      </div>
    );
  const tabs = kind === "" || kind === "settings";
  const todo = todoList(remote).length;
  return (
    <div className={"phone" + (kind === "settings" ? " gray" : "")}>
      {page}
      {naming}
      {tabs && (
        <nav className="tabbar">
          <button className={kind === "" ? "on" : ""} onClick={() => go("")}>
            <MessageSquare />
            会话
            {todo > 0 && <span className="n">{todo}</span>}
          </button>
          <button
            className={kind === "settings" ? "on" : ""}
            onClick={() => go("settings")}
          >
            <SettingsIcon />
            设置
          </button>
        </nav>
      )}
    </div>
  );
}
