import * as l10n from "./i18n";
import { getCoreHost } from "./core/host";
import { ensureCore } from "./core/runtime";
import { useLocale } from "./i18n";
import { Startup } from "./Startup";
import { RetainedIdentityDeniedScreen } from "./Rejoin";
import { authorityStartup } from "./authority-setup";
import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
} from "react";
import { SetupReset } from "./SetupReset";
import { Remote, resumeRegistration, errorText } from "./remote";
import { bindNetworkChanges, stableNetworkSignature, type NetworkConnection } from "./transport/network";
import { loadAccount, type StoredAccount, watchAccountLogout } from "./storage";
import { securitySetupStatus } from "./device-security-store";
import { guardUpdateInput } from "./update";
import { page, preloadRoute } from "./views/pages";

// Authenticated views are fetched only after a local identity has loaded.
const loadWorkspace = () => import("./Workspace");
const WorkspaceFallback = page(loadWorkspace);

// Joining and pairing bring the QR code and curve code; fetched when used.
const Setup = lazy(() =>
  import("./views/Pairing").then((m) => ({ default: m.Setup })),
);
const AccountEntry = lazy(() => import("./views/AuthoritySetup").then(m => ({ default: m.AccountEntry })));
const loading = <Startup />;
// Keep brief tab/app switches from rebuilding the stream and terminal.
const HIDDEN_GRACE_MS = 60_000;

// ?demo in development shows made-up machines and sessions in every state
// the mockup draws, with no server. It is compiled out of builds.
const demo =
  import.meta.env.DEV && new URLSearchParams(location.search).has("demo");

async function loadRemote(): Promise<Remote | undefined> {
  if (import.meta.env.DEV && demo) {
    if (new URLSearchParams(location.search).has("entry")) return undefined;
    const { DemoRemote } = await import("./dev/demo");
    return DemoRemote.create(new URLSearchParams(location.search));
  }
  return Remote.load();
}

export { bindNetworkChanges, stableNetworkSignature };

export function App() {
  useLocale();
  const [remote, setRemote] = useState<Remote | null | undefined>(undefined);
  const [workspace, setWorkspace] = useState<Awaited<ReturnType<typeof loadWorkspace>>>();
  const [retained, setRetained] = useState<StoredAccount>();
  const [denied, setDenied] = useState(false);
  const loadGeneration = useRef(0);
  const [setupError, setSetupError] = useState("");
  const [coreError, setCoreError] = useState("");
  const current = useRef(remote);
  current.current = remote;
  const load = async () => {
    const generation = ++loadGeneration.current;
    const active = () => generation === loadGeneration.current;
    setRetained(undefined);
    setDenied(false);
    let coreFailed = false;
    const corePromise = ensureCore().catch((error) => {
      coreFailed = true;
      throw error;
    });
    corePromise.catch(() => undefined);
    try {
      setSetupError("");
      setCoreError("");
      if (!demo) {
        const startup = await authorityStartup();
        if (startup.kind === "create" || startup.kind === "recovery") { setRemote(null); return; }
        await resumeRegistration();
      }
      const account = await loadAccount();
      if (account && !demo) {
        const status = await securitySetupStatus();
        if (status === "missing") {
          const { retainedIdentityDenied } = await import("./pairing");
          const denied = await retainedIdentityDenied(account);
          if (!active()) return;
          setDenied(denied);
          setRetained(account);
          setRemote(null);
          return;
        }
        if (status === "clock") {
          setSetupError(
            l10n.t("errors.this_device_s_clock_is_behind_the_account_state"),
          );
          setRemote(null);
          return;
        }
        if (status === "invalid") {
          setSetupError(
            l10n.t("errors.this_device_s_security_state_could_not_be_verified"),
          );
          setRemote(null);
          return;
        }
      }
      // Read the sealed snapshot while the workspace and current route chunk download.
      const initialRoute = location.hash.replace(/^#\/?/, "");
      const hasWorkspace = Boolean(account || demo);
      const [, loaded, workspace] = await Promise.all([
        corePromise,
        loadRemote(),
        hasWorkspace ? loadWorkspace().catch(() => undefined) : undefined,
        hasWorkspace ? preloadRoute(initialRoute).catch(() => undefined) : undefined,
      ]);
      if (!active()) return;
      if (loaded) {
        await getCoreHost(loaded).warmLocalCache(initialRoute);
        if (!active()) return;
      }
      setRetained(undefined);
      setWorkspace(workspace);
      setRemote(loaded ?? null);
    } catch (error) {
      if (!active()) return;
      if (coreFailed) {
        setCoreError(
          l10n.t("errors.core_load_failed", { reason: errorText(error) }),
        );
      } else {
        setSetupError(
          l10n.t("errors.setup_request_preserved", { reason: errorText(error) }),
        );
      }
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
    if (!remote) return;
    return guardUpdateInput(() => [...remote.drafts.values(), ...remote.approvalReasons.values()].some(text => text.length > 0) ||
      [...remote.trays.values()].some(tray => tray.items.length > 0) || getCoreHost(remote).hasUnsavedScheduleDrafts());
  }, [remote]);
  useEffect(() => {
    if (!remote) return;
    getCoreHost(remote);
    let running = false;
    let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
    const cancelHidden = () => {
      clearTimeout(hiddenTimer);
      hiddenTimer = undefined;
    };
    const pause = () => {
      cancelHidden();
      if (!running) return;
      running = false;
      remote.stop();
    };
    const netBinding = bindNetworkChanges(
      remote,
      navigator as Navigator & { connection?: NetworkConnection },
      window,
      () => running,
    );
    const resume = () => {
      if (document.hidden) return;
      cancelHidden();
      running = true;
      // start is idempotent and also replaces a stale retained stream.
      remote.setForeground(true);
      remote.start();
      netBinding.report();
    };
    const visibility = () => {
      remote.setForeground(!document.hidden);
      if (!document.hidden) resume();
      else if (running && hiddenTimer === undefined)
        hiddenTimer = setTimeout(pause, HIDDEN_GRACE_MS);
    };
    document.addEventListener("visibilitychange", visibility);
    // Frozen timers cannot enforce the grace period. Release sockets in
    // the event itself, including when entering the back/forward cache.
    document.addEventListener("freeze", pause);
    document.addEventListener("resume", resume);
    addEventListener("pagehide", pause);
    addEventListener("pageshow", resume);
    resume();
    return () => {
      netBinding.dispose();
      document.removeEventListener("visibilitychange", visibility);
      document.removeEventListener("freeze", pause);
      document.removeEventListener("resume", resume);
      removeEventListener("pagehide", pause);
      removeEventListener("pageshow", resume);
      cancelHidden();
      remote.stop();
    };
  }, [remote]);
  // Keep the HTML shell in place while reading the saved identity.
  if (remote === undefined)
    return loading;
  if (coreError || setupError)
    return (
      <div className="center">
        <p>{coreError || setupError}</p>
        <button onClick={() => void load()}>{l10n.t("common.retry")}</button>
        {!coreError && <SetupReset onDone={() => void load()} />}
      </div>
    );
  if (remote === null && denied && retained)
    return <RetainedIdentityDeniedScreen server={retained.server} onDone={() => void load()} />;
  if (remote === null)
    return (
      <Suspense fallback={loading}>
        {retained ? <Setup existing={retained} onDone={() => void load()} /> : <AccountEntry onDone={() => void load()} />}
      </Suspense>
    );
  const Workspace = workspace?.default ?? WorkspaceFallback;
  return <Suspense fallback={loading}><Workspace remote={remote} /></Suspense>;
}
