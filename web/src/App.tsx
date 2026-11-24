import * as l10n from "./i18n";
import { getCoreHost } from "./core/host";
import { ensureCore } from "./core/runtime";
import { createStandaloneSecurityClient, SecurityWorkflowError } from "./core/security-client";
import { SecurityBootstrapKind, SecurityErrorCode, type SecurityWorkflowStatus } from "./gen/remoteagent/core/v1/security_pb";
import { useLocale } from "./i18n";
import { Startup } from "./Startup";
import { RetainedIdentityDeniedScreen } from "./Rejoin";
import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
} from "react";
import { SetupReset } from "./SetupReset";
import { Remote, errorText } from "./remote";
import { bindNetworkChanges, stableNetworkSignature, type NetworkConnection } from "./transport/network";
import { watchAccountLogout } from "./storage";
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
  const [entryStatus, setEntryStatus] = useState<SecurityWorkflowStatus>();
  const [retainedServer, setRetainedServer] = useState<string>();
  const [denied, setDenied] = useState(false);
  const loadGeneration = useRef(0);
  const [setupError, setSetupError] = useState("");
  const [setupCanReset, setSetupCanReset] = useState(true);
  const [coreError, setCoreError] = useState("");
  const current = useRef(remote);
  current.current = remote;
  const load = async () => {
    const generation = ++loadGeneration.current;
    const active = () => generation === loadGeneration.current;
    setRetainedServer(undefined);
    setEntryStatus(undefined);
    setDenied(false);
    let coreFailed = false;
    const corePromise = ensureCore().catch((error) => {
      coreFailed = true;
      throw error;
    });
    corePromise.catch(() => undefined);
    try {
      setSetupError("");
      setSetupCanReset(true);
      setCoreError("");
      const initialRoute = location.hash.replace(/^#\/?/, "");
      if (demo) {
        const [, loaded, ws] = await Promise.all([
          corePromise,
          loadRemote(),
          loadWorkspace().catch(() => undefined),
          preloadRoute(initialRoute).catch(() => undefined),
        ]);
        if (!active()) return;
        if (loaded) {
          await getCoreHost(loaded).warmLocalCache(initialRoute);
          if (!active()) return;
        }
        setWorkspace(ws);
        setRemote(loaded ?? null);
        return;
      }

      await corePromise;
      if (!active()) return;
      const standalone = createStandaloneSecurityClient();
      let st: SecurityWorkflowStatus;
      try {
        st = await standalone.client.bootstrap();
        if (st.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION) {
          const resumed = await standalone.client.resumePendingOp();
          if (resumed.status) st = resumed.status;
        }
        if (!active()) return;
        if (st.bootstrap === SecurityBootstrapKind.EMPTY && st.deviceId && st.server) {
          const isDenied = await standalone.client.checkRetainedDenied();
          if (!active()) return;
          setDenied(isDenied);
          setRetainedServer(st.server);
          setEntryStatus(st);
          setRemote(null);
          return;
        }
      } finally {
        standalone.dispose();
      }
      if (!active()) return;
      if (st.bootstrap === SecurityBootstrapKind.CLOCK_SKEWED) {
        setSetupError(
          l10n.t("errors.this_device_s_clock_is_behind_the_account_state"),
        );
        setRemote(null);
        return;
      }
      if (st.bootstrap === SecurityBootstrapKind.INVALID) {
        setSetupError(
          l10n.t("errors.this_device_s_security_state_could_not_be_verified"),
        );
        setRemote(null);
        return;
      }
      if (
        st.bootstrap === SecurityBootstrapKind.EMPTY ||
        st.bootstrap === SecurityBootstrapKind.PENDING_CREATE ||
        st.bootstrap === SecurityBootstrapKind.PENDING_RECOVERY
      ) {
        setEntryStatus(st);
        setRemote(null);
        return;
      }

      // READY or REMOVED: load Remote and workspace chunk concurrently.
      const [loaded, ws] = await Promise.all([
        Remote.load(st),
        loadWorkspace().catch(() => undefined),
        preloadRoute(initialRoute).catch(() => undefined),
      ]);
      if (!active()) return;
      if (loaded) {
        await getCoreHost(loaded).warmLocalCache(initialRoute);
        if (!active()) return;
      }
      setWorkspace(ws);
      setRemote(loaded ?? null);
    } catch (error) {
      if (!active()) return;
      if (coreFailed) {
        setCoreError(
          l10n.t("errors.core_load_failed", { reason: errorText(error) }),
        );
      } else {
        const transientStorage =
          error instanceof SecurityWorkflowError && error.code === SecurityErrorCode.STORAGE;
        setSetupCanReset(!transientStorage);
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
    return guardUpdateInput(() => [...remote.approvalReasons.values()].some(text => text.length > 0) ||
      getCoreHost(remote).hasUnsavedDrafts() || getCoreHost(remote).hasUnsavedScheduleDrafts());
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
        {!coreError && setupCanReset && <SetupReset onDone={() => void load()} />}
      </div>
    );
  if (remote === null && denied && retainedServer)
    return <RetainedIdentityDeniedScreen server={retainedServer} onDone={() => void load()} />;
  if (remote === null)
    return (
      <Suspense fallback={loading}>
        {retainedServer ? <Setup existingServer={retainedServer} onDone={() => void load()} /> : <AccountEntry initialStatus={entryStatus} onDone={() => void load()} />}
      </Suspense>
    );
  const Workspace = workspace?.default ?? WorkspaceFallback;
  return <Suspense fallback={loading}><Workspace remote={remote} /></Suspense>;
}
