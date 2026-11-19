import { useEffect, useRef, useState } from "react";
import * as l10n from "../i18n";
import { Check, Copy, LoaderCircle } from "../icons";
import { type Remote } from "../remote";
import { Sheet, StateMark, useRemote } from "../ui";
import { plainDiagView } from "./direct-diag-view";

export function DirectDiagSheet({ remote, id, onClose }: { remote: Remote; id: string; onClose: () => void }) {
  useRemote(remote);
  const [, tick] = useState(0);
  const [copied, setCopied] = useState("" as "" | "ok" | "fail");
  const [cooldown, setCooldown] = useState(false);
  const [liveMsg, setLiveMsg] = useState("");
  const prevRunning = useRef(false);
  const retryInitiated = useRef(false);

  const engine = remote.direct;
  const status = engine.status(id);
  const data = engine.diagnostics(id);
  const connectedDirect =
    status.state === "connected" && (status.read === "direct" || status.file === "direct");
  const running =
    status.state === "connecting" || (data.rounds.length > 0 && data.rounds[0].end === undefined);
  const view = plainDiagView({
    ...data,
    now: Date.now(),
    browser: typeof navigator === "undefined" ? undefined : navigator.userAgent,
    reason: status.reason,
    connectedNow: connectedDirect,
    trying: running,
  });

  // Auto-close 600ms after a retry succeeds cleanly
  useEffect(() => {
    if (!view.connected || !retryInitiated.current) return;
    setLiveMsg(view.title);
    const timer = setTimeout(onClose, 600);
    return () => clearTimeout(timer);
  }, [view.connected, view.title, onClose]);

  // Sample running attempts and enforce 2s button cooldown after an unsuccessful retry
  useEffect(() => {
    engine.refreshDiagnostics(id);
    if (prevRunning.current && !running && !view.connected) {
      setCooldown(true);
      setLiveMsg(view.title);
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
  }, [engine, id, running, view.connected, view.title]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), copied === "fail" ? 2500 : 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = () => {
    if (typeof navigator === "undefined" || !navigator.clipboard) {
      setCopied("fail");
      setLiveMsg(l10n.t("transport.diag_copy_permission"));
      return;
    }
    navigator.clipboard.writeText(view.report).then(
      () => {
        setCopied("ok");
        setLiveMsg(l10n.t("common.copied"));
      },
      () => {
        setCopied("fail");
        setLiveMsg(l10n.t("transport.diag_copy_permission"));
      },
    );
  };

  const retry = () => {
    if (running || cooldown || view.connected) return;
    retryInitiated.current = true;
    setLiveMsg(l10n.t("transport.diag_retrying"));
    if (!engine.retryNow(id)) {
      setCooldown(true);
      setTimeout(() => setCooldown(false), 2000);
    }
    tick((n) => n + 1);
  };

  return (
    <Sheet onClose={onClose} title={l10n.t("transport.why_not_connected")} action={{ label: l10n.t("common.done"), onClick: onClose }}>
      <div className="diag-hero" role="status">
        <h5>{view.title}</h5>
        <p>{view.body}</p>
      </div>
      <div className="diag-checks">
        {view.checks.map((c, i) => (
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
          disabled={running || cooldown || view.connected || view.peerOutdated || status.state === "unsupported"}
          onClick={retry}
        >
          {running && <LoaderCircle />}
          <span>{running ? l10n.t("transport.diag_retrying") : l10n.t("transport.diag_retry")}</span>
        </button>
        <button
          type="button"
          className={"diag-copy" + (copied === "ok" ? " ok" : copied === "fail" ? " err" : "")}
          onClick={copy}
        >
          {copied === "ok" ? <Check /> : null}
          {!copied ? <Copy /> : null}
          <span>
            {copied === "ok"
              ? l10n.t("common.copied")
              : copied === "fail"
                ? l10n.t("transport.diag_copy_permission")
                : l10n.t("transport.diag_copy_support")}
          </span>
        </button>
      </div>
      <p className="gf">{l10n.t("transport.diag_privacy_footer")}</p>
      <span className="sr-only" aria-live="polite">{liveMsg}</span>
    </Sheet>
  );
}
