import { useEffect, useRef, useState, type ReactNode } from "react";
import { t } from "../i18n";
import { Swap, useGrow } from "../motion";
import {
  useCoreHost,
  useDirectDiagnosticsView,
  useDirectError,
  useMachineSettingsView,
} from "../core/host";
import { renderText as renderCoreMsg } from "../core/text";
import {
  ConnectionGroupStage,
  DiagCheckMark,
  type ConnectionGroupView,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { Check, Copy, LoaderCircle } from "../icons";
import { Banner, Sheet, StateMark, Switch, useRemote, type Mark } from "../ui";
import type { Remote } from "../remote";
import { stageToMark } from "./direct-status";

const TRYING_MS = 10_000;

function checkMarkKind(mark: DiagCheckMark): Mark {
  switch (mark) {
    case DiagCheckMark.ONLINE: return "online";
    case DiagCheckMark.RUN: return "run";
    default: return "off";
  }
}

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

function DiagCheckItem({ mark, label, detail, status }: { mark: DiagCheckMark; label: string; detail: string; status: string }) {
  return (
    <div className="diag-check">
      <StateMark kind={checkMarkKind(mark)} />
      <div className="diag-check-b">
        <b>{label}</b>
        {detail && <small>{detail}</small>}
      </div>
      <span className="diag-check-st">{status}</span>
    </div>
  );
}

/** Unified Direct Settings Sheet: switch + route status + inline Core-driven self-check diagnostics & retry/copy. */
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
  const diagVm = useDirectDiagnosticsView(host, id);
  const error = useDirectError(host, id);
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
    host.refreshDirectDiagnostics(id);
  }, [host, id]);

  useEffect(() => {
    if (turnedOn === undefined) return;
    const timer = setTimeout(() => setTurnedOn(undefined), TRYING_MS);
    return () => clearTimeout(timer);
  }, [turnedOn]);

  const cg = passed ?? vm.connection;
  const running = diagVm.trying || cg?.stage === ConnectionGroupStage.CONNECTING;
  const diagTitle = renderCoreMsg(diagVm.titleText);

  useEffect(() => {
    if (prevRunning.current && !running && !diagVm.connected) {
      setCooldown(true);
      if (diagTitle) setLiveMsg(diagTitle);
      const cd = setTimeout(() => setCooldown(false), 2000);
      prevRunning.current = false;
      return () => clearTimeout(cd);
    }
    prevRunning.current = running;
    if (!running) return;
    const timer = setInterval(() => {
      host.refreshDirectDiagnostics(id);
    }, 1000);
    return () => clearInterval(timer);
  }, [host, id, running, diagVm.connected, diagTitle]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), copied === "fail" ? 2500 : 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  if (!cg) return null;

  const browserUnsupported = !diagVm.supported || cg.stage === ConnectionGroupStage.UNSUPPORTED;
  const rawDetailText = renderCoreMsg(cg.detailText);
  const on = !browserUnsupported && cg.toggleChecked;
  const daemonDisabled = cg.stage === ConnectionGroupStage.DISABLED && !on && !!rawDetailText;
  const tryingPending =
    turnedOn !== undefined &&
    on &&
    !diagVm.connected &&
    cg.stage === ConnectionGroupStage.RELAY &&
    !cg.showDiagnosticsEntry;
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
      : renderCoreMsg(cg.statusText);
  const detailText = browserUnsupported
    ? t("transport.detail_relay_only")
    : tryingPending
      ? ""
      : rawDetailText;
  const metricText = browserUnsupported ? "" : renderCoreMsg(cg.metricText);
  const footer = renderCoreMsg(cg.footerText) || t("transport.footer");

  const hasDiagChecks = diagVm.checks.length > 0;
  const canDiagNow =
    on &&
    !tryingPending &&
    hasDiagChecks &&
    (cg.showDiagnosticsEntry || (wasDiagRef.current && running && !diagVm.connected));
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
    const report = diagVm.reportLines.map((line) => renderCoreMsg(line)).filter(Boolean).join("\n");
    navigator.clipboard.writeText(report).then(
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
    if (running || cooldown || diagVm.connected || diagVm.peerOutdated || browserUnsupported || !diagVm.canRetry) return;
    setLiveMsg(t("transport.diag_retrying"));
    void host.retryDirectNow(id).then((started) => {
      if (!started) {
        setCooldown(true);
        setTimeout(() => setCooldown(false), 2000);
      }
    });
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
            {diagVm.checks.map((c, i) => (
              <DiagCheckItem
                key={i}
                mark={c.mark}
                label={renderCoreMsg(c.labelText)}
                detail={renderCoreMsg(c.detailText)}
                status={renderCoreMsg(c.statusText)}
              />
            ))}
          </div>
          <div className="diag-actions">
            <button
              type="button"
              className="diag-retry"
              disabled={running || cooldown || diagVm.connected || diagVm.peerOutdated || browserUnsupported || !diagVm.canRetry}
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
