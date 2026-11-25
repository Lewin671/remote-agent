import * as l10n from "./i18n";
import {
  AppRootStage,
  CoreHost,
  getCoreHost,
  OnboardingMode,
  useAppRootView,
  type AccountRuntimeChangedEffect,
} from "./core/host";
import { ensureCore } from "./core/runtime";
import { renderText } from "./core/text";
import { useLocale } from "./i18n";
import { clearMarkdownCache } from "./markdown";
import { Startup } from "./Startup";
import { RetainedIdentityDeniedScreen } from "./Rejoin";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { SetupReset } from "./SetupReset";
import { Remote, errorText } from "./remote";
import { bindNetworkChanges, stableNetworkSignature, type NetworkConnection } from "./transport/network";
import { watchAccountLogout } from "./storage";
import { guardUpdateInput } from "./update";
import { page, preloadRoute, RemovedScreen } from "./views/pages";

// Authenticated views are fetched only after a local identity has loaded.
const loadWorkspace = () => import("./Workspace");
const WorkspaceFallback = page(loadWorkspace);

// Entry/onboarding views are fetched only when needed.
const AccountEntry = lazy(() => import("./views/AuthoritySetup").then((m) => ({ default: m.AccountEntry })));
const loading = <Startup />;
// Keep brief tab/app switches from rebuilding the stream and terminal.
const HIDDEN_GRACE_MS = 60_000;

// ?demo in development shows made-up machines and sessions in every state
// the mockup draws, with no server. It is compiled out of builds.
const demo =
  import.meta.env.DEV && new URLSearchParams(location.search).has("demo");

function readDefaultServer(): string {
  try {
    return localStorage.getItem("ra-server") || import.meta.env.VITE_DEFAULT_SERVER || "";
  } catch {
    return import.meta.env.VITE_DEFAULT_SERVER || "";
  }
}

export { bindNetworkChanges, stableNetworkSignature };

type WorkspaceModule = Awaited<ReturnType<typeof loadWorkspace>>;

function CoreDrivenApp({
  host,
  initialWorkspace,
}: {
  host: CoreHost;
  initialWorkspace?: WorkspaceModule;
}) {
  const appRoot = useAppRootView(host);
  const [remote, setRemote] = useState(null as Remote | null);
  const [workspace, setWorkspace] = useState(initialWorkspace as WorkspaceModule | undefined);
  const activeGen = useRef(0);
  const currentRemote = useRef(null as Remote | null);
  currentRemote.current = remote;

  useEffect(() => {
    const onRuntime = (eff: AccountRuntimeChangedEffect) => {
      const gen = ++activeGen.current;
      if (!eff.active) {
        if (!eff.removed) {
          host.detachRemote();
          clearMarkdownCache();
          setRemote(null);
          const obMode = host.viewOnboarding().mode;
          location.hash = obMode === OnboardingMode.JOIN ? "/pair?" : "";
        }
        return;
      }
      if (!eff.server || !eff.deviceId) return;
      if (location.hash.replace(/^#\/?/, "").startsWith("pair")) {
        location.hash = "";
      }
      const initialRoute = location.hash.replace(/^#\/?/, "");
      const acct = { server: eff.server, deviceId: eff.deviceId, accountId: eff.accountId };
      const status = host.securityClient.status;
      void (async () => {
        const [loaded, ws] = await Promise.all([
          Remote.load(acct, host, status),
          loadWorkspace().catch(() => undefined),
          preloadRoute(initialRoute).catch(() => undefined),
        ]);
        if (gen !== activeGen.current) {
          loaded?.dispose(false);
          return;
        }
        if (loaded) {
          await host.warmLocalCache(initialRoute);
          if (gen !== activeGen.current) return;
        }
        if (ws) setWorkspace(ws);
        setRemote(loaded ?? null);
      })();
    };
    host.setOnAccountRuntimeChanged(onRuntime);
    return () => host.setOnAccountRuntimeChanged(undefined);
  }, [host]);

  useEffect(() => watchAccountLogout(() => host.externalLogout()), [host]);

  useEffect(() => {
    if (!remote) return;
    return guardUpdateInput(
      () =>
        [...remote.approvalReasons.values()].some((text) => text.length > 0) ||
        host.hasUnsavedDrafts() ||
        host.hasUnsavedScheduleDrafts(),
    );
  }, [host, remote]);

  // Keep Core's foreground state synced across both pre-login (pairing/recovery polls) and active sessions.
  useEffect(() => {
    if (!remote) {
      const onVis = () => host.setForeground(!document.hidden);
      const onHide = () => host.setForeground(false);
      document.addEventListener("visibilitychange", onVis);
      addEventListener("pagehide", onHide);
      addEventListener("pageshow", onVis);
      return () => {
        document.removeEventListener("visibilitychange", onVis);
        removeEventListener("pagehide", onHide);
        removeEventListener("pageshow", onVis);
      };
    }
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
      remote.setForeground(true);
      remote.start();
      netBinding.report();
    };
    const visibility = () => {
      if (!document.hidden) {
        resume();
        return;
      }
      remote.setForeground(false);
      if (running && hiddenTimer === undefined) {
        hiddenTimer = setTimeout(pause, HIDDEN_GRACE_MS);
      }
    };
    document.addEventListener("visibilitychange", visibility);
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
  }, [host, remote]);

  if (appRoot.stage === AppRootStage.LOADING || appRoot.stage === AppRootStage.UNSPECIFIED) {
    return <div className="center muted">{l10n.t("security.loading")}</div>;
  }
  if (appRoot.stage === AppRootStage.BOOTSTRAP_ERROR) {
    return (
      <div className="center">
        <p>{renderText(appRoot.bootstrapError?.message)}</p>
        {appRoot.bootstrapError?.canRetry !== false && (
          <button onClick={() => host.appRootRetryBootstrap()}>
            {l10n.t("common.retry")}
          </button>
        )}
        {appRoot.bootstrapError?.canReset && <SetupReset host={host} />}
      </div>
    );
  }
  if (appRoot.stage === AppRootStage.RETAINED_DENIED) {
    return <RetainedIdentityDeniedScreen host={host} />;
  }
  if (appRoot.stage === AppRootStage.ONBOARDING) {
    return (
      <Suspense fallback={loading}>
        <AccountEntry host={host} />
      </Suspense>
    );
  }
  if (appRoot.stage === AppRootStage.REMOVED) {
    return (
      <Suspense fallback={loading}>
        <RemovedScreen host={host} remote={remote ?? undefined} />
      </Suspense>
    );
  }
  if (!remote) return loading;
  const Workspace = workspace?.default ?? WorkspaceFallback;
  return (
    <Suspense fallback={loading}>
      <Workspace remote={remote} />
    </Suspense>
  );
}

export function App() {
  useLocale();
  const [host, setHost] = useState(undefined as CoreHost | undefined);
  const [demoRemote, setDemoRemote] = useState(undefined as Remote | null | undefined);
  const [workspace, setWorkspace] = useState(undefined as WorkspaceModule | undefined);
  const [coreError, setCoreError] = useState("");
  const [bootAttempt, setBootAttempt] = useState(0);

  useEffect(() => {
    let alive = true;
    let ownedHost: CoreHost | undefined;
    setCoreError("");

    void (async () => {
      try {
        await ensureCore();
        if (!alive) return;
        const initialRoute = location.hash.replace(/^#\/?/, "");
        if (import.meta.env.DEV && demo) {
          const ws = await loadWorkspace().catch(() => undefined);
          await preloadRoute(initialRoute).catch(() => undefined);
          const { DemoRemote } = await import("./dev/demo");
          const loaded = DemoRemote.create(new URLSearchParams(location.search));
          if (!alive) return;
          await getCoreHost(loaded).warmLocalCache(initialRoute);
          if (!alive) return;
          setWorkspace(ws);
          setDemoRemote(loaded);
          return;
        }

        // Production single-core startup: exactly 1 CoreHost / WasmAppCore for the entire app lifecycle.
        const h = new CoreHost(undefined);
        ownedHost = h;
        h.clientInit({
          initialUrl: location.hash,
          defaultServer: readDefaultServer(),
          requestStoragePersist: true,
        });
        if (!alive) {
          h.dispose();
          return;
        }
        setHost(h);
      } catch (error) {
        if (!alive) return;
        setCoreError(l10n.t("errors.core_load_failed", { reason: errorText(error) }));
      }
    })();

    return () => {
      alive = false;
      ownedHost?.dispose();
    };
  }, [bootAttempt]);

  if (coreError) {
    return (
      <div className="center">
        <p>{coreError}</p>
        <button onClick={() => setBootAttempt((n) => n + 1)}>
          {l10n.t("common.retry")}
        </button>
      </div>
    );
  }
  if (demo && demoRemote) {
    const Workspace = workspace?.default ?? WorkspaceFallback;
    return (
      <Suspense fallback={loading}>
        <Workspace remote={demoRemote} />
      </Suspense>
    );
  }
  if (!host) return loading;
  return <CoreDrivenApp host={host} initialWorkspace={workspace} />;
}
