import { useEffect, useRef, useState, type ReactNode } from "react";
import { t } from "../i18n";
import { Swap, useGrow } from "../motion";
import { useCoreHost, useDirectError, useMachineSettingsView } from "../core/host";
import { renderText } from "../core/text";
import { ConnectionGroupStage, type ConnectionGroupView } from "../gen/remoteagent/core/v1/settings_view_pb";
import { Check, Copy, LoaderCircle } from "../icons";
import { Banner, Sheet, StateMark, Switch, useRemote } from "../ui";
import type { Remote } from "../remote";
import { plainDiagView } from "./direct-diag-view";
import { stageToMark } from "./direct-status";

const TRYING_MS = 10_000;

function GroupRow({ className, enter, children }: { className: string; enter: boolean; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useGrow(ref, { enter });
  return <div ref={ref} className={className}>{children}</div>;
}

function SubLine({ text, enter }: { text: string; enter: boolean }) {
  const ref = useRef<HTMLElement>(null);
  useGrow(ref, { enter });
  return <small ref={ref}><Swap k={text} tier="base">{text}</Swap></small>;
}

/** Unified Direct Settings Sheet: switch + route status + inline 3-row self-check diagnostics & retry/copy. */
export function DirectSettingsSheet({
  remote,
  id,
  connection: passed,
  onClose,
}: {
  remote: Remote;
  id: string;
  connection?: ConnectionGroupView;
  onClose: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote);
  const vm = useMachineSettingsView(host, id);
  const error = useDirectError(host, id);
  const [, tick] = useState(0);
  const [turnedOn, setTurnedOn] = useState<number>();
  const [copied, setCopied] = useState<"" | "ok" | "fail">("");
  const [cooldown, setCooldown] = useState(false);
  const [liveMsg, setLiveMsg] = useState("");
  const armed = useRef(false);
  const prevRunning = useRef(false);
  const wasDiagRef = useRef(false);

  useEffect(() => {
    armed.current = true;
  }, []);

  useEffect(() => {
    remote.refreshDirectStatuses();
  }, [remote, id]);

  useEffect(() => {
    if (turnedOn === undefined) return;
    const timer = setTimeout(() => setTurnedOn(undefined), TRYING_MS);
    return () => clearTimeout(timer);
  }, [turnedOn]);

  const cg = passed ?? vm.connection;
  const engine = remote.direct;
  const rawStatus = engine.status(id);
  const data = engine.diagnostics(id);
  const connectedDirect =
    rawStatus.state === "connected" && (rawStatus.read === "direct" || rawStatus.file === "direct");
  const running =
    rawStatus.state === "connecting" || (data.rounds.length > 0 && data.rounds[0].end === undefined);
  const diagView = plainDiagView({
    ...data,
    now: Date.now(),
    browser: typeof navigator === "undefined" ? undefined : navigator.userAgent,
    reason: rawStatus.reason,
    connectedNow: connectedDirect,
    trying: running,
  });

  useEffect(() => {
    engine.refreshDiagnostics(id);
    if (prevRunning.current && !running && !diagView.connected) {
      setCooldown(true);
      setLiveMsg(diagView.title);
      const cd = setTimeout(() => setCooldown(false), 2000);
      prevRunning.current = false;
      return () => clearTimeout(cd);
    }
    prevRunning.current = running;
    if (!running) return;
    const timer = setInterval(() => {
      engine.refreshDiagnostics(id);
      tick((n) => n + 1);
    }, 1000);
    return () => clearInterval(timer);
  }, [engine, id, running, diagView.connected, diagView.title]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), copied === "fail" ? 2500 : 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  if (!cg) return null;

  const browserUnsupported = rawStatus.state === "unsupported";
  const tryingPending =
    turnedOn !== undefined &&
    rawStatus.state === "none" &&
    (!rawStatus.reason || rawStatus.reason === "starting" || rawStatus.reason === "security_pending");
  const on = !browserUnsupported && cg.toggleChecked;
  const rawDetailText = renderText(cg.detailText);
  const daemonDisabled = cg.stage === ConnectionGroupStage.DISABLED && !on && !!rawDetailText;
  const showStatus =
    browserUnsupported ||
    daemonDisabled ||
    (on && cg.stage !== ConnectionGroupStage.OFFLINE && cg.stage !== ConnectionGroupStage.DISABLED);
  const enter = armed.current;

  const stage = tryingPending ? ConnectionGroupStage.CONNECTING : cg.stage;
  const statusText = browserUnsupported
    ? t("transport.status_unsupported")
    : tryingPending
      ? t("transport.status_connecting")
      : renderText(cg.statusText);
  const detailText = browserUnsupported
    ? t("transport.detail_relay_only")
    : tryingPending
      ? ""
      : rawDetailText;
  const metricText = browserUnsupported ? "" : renderText(cg.metricText);
  const footer = renderText(cg.footerText) || t("transport.footer");
  const canDiagNow = on && !tryingPending && (cg.showDiagnosticsEntry || (wasDiagRef.current && running && !diagView.connected));
  if (on && !tryingPending && cg.showDiagnosticsEntry) {
    wasDiagRef.current = true;
  } else if (!running && !cg.showDiagnosticsEntry) {
    wasDiagRef.current = false;
  }
  const showDiag = canDiagNow;

  const copyDiag = () => {
    if (typeof navigator === "undefined" || !navigator.clipboard) {
      setCopied("fail");
      setLiveMsg(t("transport.diag_copy_permission"));
      return;
    }
    navigator.clipboard.writeText(diagView.report).then(
      () => {
        setCopied("ok");
        setLiveMsg(t("transport.row_diag_copied"));
      },
      () => {
        setCopied("fail");
        setLiveMsg(t("transport.diag_copy_permission"));
      },
    );
  };

  const retryDirect = () => {
    if (running || cooldown || diagView.connected || diagView.peerOutdated || browserUnsupported) return;
    setLiveMsg(t("transport.diag_retrying"));
    if (!engine.retryNow(id)) {
      setCooldown(true);
      setTimeout(() => setCooldown(false), 2000);
    }
    tick((n) => n + 1);
  };

  return (
    <Sheet onClose={onClose} title={t("transport.direct")} action={{ label: t("common.done"), onClick: onClose }}>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="grp conn-grp" aria-live="polite">
        <div className="row">
          {t("transport.row_allow_direct")}
          <Switch
            on={on}
            disabled={browserUnsupported || !cg.toggleEnabled}
            onChange={(next) => {
              setTurnedOn(next ? Date.now() : undefined);
              host.updateDirectPreference(id, next);
            }}
            label={t("transport.row_allow_direct")}
          />
        </div>
        {showStatus && (
          <GroupRow className="row dx" enter={enter}>
            <span className="dx-mark">
              <StateMark kind={stageToMark(stage)} />
            </span>
            <span className="dx-text">
              <Swap k={`${stage}:${statusText}`} tier="base">{statusText}</Swap>
              {detailText && <SubLine text={detailText} enter={enter} />}
            </span>
            {metricText && <span className="v dx-rtt">{metricText}</span>}
          </GroupRow>
        )}
      </div>
      {showDiag ? (
        <>
          <div className="fl">{t("transport.diag_section_auto")}</div>
          <div className="diag-checks">
            {diagView.checks.map((c, i) => (
              <div key={i} className="diag-check">
                <StateMark kind={c.mark} />
                <div className="diag-check-b">
                  <b>{c.label}</b>
                  {c.detail && <small>{c.detail}</small>}
                </div>
                <span className="diag-check-st">{c.status}</span>
              </div>
            ))}
          </div>
          <div className="diag-actions">
            <button
              type="button"
              className="diag-retry"
              disabled={running || cooldown || diagView.connected || diagView.peerOutdated || browserUnsupported}
              onClick={retryDirect}
            >
              {running && <LoaderCircle />}
              <span>{running ? t("transport.diag_retrying") : t("transport.row_retry_direct")}</span>
            </button>
            <button
              type="button"
              className={"diag-copy" + (copied === "ok" ? " ok" : copied === "fail" ? " err" : "")}
              onClick={copyDiag}
            >
              {copied === "ok" ? <Check /> : !copied ? <Copy /> : null}
              <span>
                {copied === "ok"
                  ? t("transport.row_diag_copied")
                  : copied === "fail"
                    ? t("transport.diag_copy_permission")
                    : t("transport.row_diag_copy_support")}
              </span>
            </button>
          </div>
          <p className="gf">{t("transport.diag_privacy_footer")}</p>
          <span className="sr-only" aria-live="polite">{liveMsg}</span>
        </>
      ) : (
        <p className="gf">{footer}</p>
      )}
    </Sheet>
  );
}
