import { useEffect, useRef, useState, type ReactNode } from "react";
import { t } from "../i18n";
import { Swap, useGrow } from "../motion";
import { useCoreHost, useDirectError, useMachineSettingsView } from "../core/host";
import { renderText } from "../core/text";
import { ConnectionGroupStage, type ConnectionGroupView } from "../gen/remoteagent/core/v1/settings_view_pb";
import { ChevronRight } from "../icons";
import { Banner, StateMark, Switch } from "../ui";
import type { Remote } from "../remote";
import { DirectDiagSheet } from "./DirectDiagSheet";
import { stageToMark } from "./direct-status";

const TRYING_MS = 10_000;

function GroupRow({ className, enter, children }: { className: string; enter: boolean; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useGrow(ref, { enter });
  return <div ref={ref} className={className}>{children}</div>;
}

function NavRow({ enter, onClick, children }: { enter: boolean; onClick: () => void; children: ReactNode }) {
  const ref = useRef<HTMLButtonElement>(null);
  useGrow(ref, { enter });
  return <button ref={ref} type="button" className="row" onClick={onClick}>{children}<ChevronRight className="chev" /></button>;
}

function SubLine({ text, enter }: { text: string; enter: boolean }) {
  const ref = useRef<HTMLElement>(null);
  useGrow(ref, { enter });
  return <small ref={ref}><Swap k={text} tier="base">{text}</Swap></small>;
}

export function ConnectionGroup({
  remote,
  id,
  connection: passed,
}: {
  remote: Remote;
  id: string;
  connection?: ConnectionGroupView;
}) {
  const host = useCoreHost(remote);
  const vm = useMachineSettingsView(host, id);
  const error = useDirectError(host, id);
  const [diagOpen, setDiagOpen] = useState(() => remote.demoFlag("diagsheet"));
  const [turnedOn, setTurnedOn] = useState<number>();
  const armed = useRef(false);

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
  if (!cg) return null;

  const rawStatus = remote.direct.status(id);
  const browserUnsupported = rawStatus.state === "unsupported";
  const peerUnsupported = cg.stage === ConnectionGroupStage.UNSUPPORTED && !browserUnsupported;
  const tryingPending = turnedOn !== undefined && rawStatus.state === "none" &&
    (!rawStatus.reason || rawStatus.reason === "starting" || rawStatus.reason === "security_pending");
  const on = !browserUnsupported && cg.toggleChecked;
  const showStatus = browserUnsupported || peerUnsupported ||
    (on && cg.stage !== ConnectionGroupStage.OFFLINE && cg.stage !== ConnectionGroupStage.DISABLED);
  const enter = armed.current;

  const stage = tryingPending ? ConnectionGroupStage.CONNECTING : cg.stage;
  const statusText = browserUnsupported
    ? t("transport.status_unsupported")
    : tryingPending
      ? t("transport.status_connecting")
      : renderText(cg.statusText);
  const detailText = browserUnsupported || tryingPending ? "" : renderText(cg.detailText);
  const fileSpeed = renderText(cg.fileSpeedText) || (on && rawStatus.state === "connected" ? t("transport.file_speed_pending") : "");
  const readLatency = renderText(cg.readLatencyText);
  const footer = renderText(cg.footerText) || t("transport.footer");
  const hasRounds = remote.direct.diagnostics(id).rounds.length > 0;
  const showDiag = on && (cg.showDiagnosticsEntry || hasRounds);

  return (
    <>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="label">{t("transport.group")}</div>
      <div className="grp conn-grp" aria-live="polite">
        <div className="row">
          {t("transport.direct")}
          <Switch
            on={on}
            disabled={browserUnsupported || !cg.toggleEnabled}
            onChange={(next) => {
              setTurnedOn(next ? Date.now() : undefined);
              host.updateDirectPreference(id, next);
            }}
            label={t("transport.direct")}
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
          </GroupRow>
        )}
        {on && fileSpeed && (
          <GroupRow className="row speed" enter={enter}>
            <span>{t("transport.speed_file")}</span>
            <span className="v">{fileSpeed}</span>
          </GroupRow>
        )}
        {on && readLatency && (
          <GroupRow className="row speed" enter={enter}>
            <span>{t("transport.speed_read")}</span>
            <span className="v">{readLatency}</span>
          </GroupRow>
        )}
        {showDiag && (
          <NavRow enter={enter} onClick={() => setDiagOpen(true)}>{t("transport.diag")}</NavRow>
        )}
      </div>
      <p className="gf">{footer}</p>
      {diagOpen && <DirectDiagSheet remote={remote} id={id} onClose={() => setDiagOpen(false)} />}
    </>
  );
}
