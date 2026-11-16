// 诊断信息 (design-system §6 「连接」组): why a computer's direct path is not up,
// in a sentence and in the counts behind it, for a user without webrtc-internals.
// Opened from the last row of the 「连接」 group; reads what the engine kept of the
// last rounds (transport/direct-diag.ts), which holds no address.
import { useEffect, useState } from "react";
import * as l10n from "../i18n";
import { type Remote } from "../remote";
import { Sheet, useRemote } from "../ui";
import { diagView } from "./direct-diag-view";

export function DirectDiagSheet({ remote, id, onClose }: { remote: Remote; id: string; onClose: () => void }) {
  useRemote(remote);
  const [, tick] = useState(0);
  const [copied, setCopied] = useState("" as "" | "ok" | "fail");
  const engine = remote.direct;
  const status = engine.status(id);
  const data = engine.diagnostics(id);
  const view = diagView({ ...data, now: Date.now(), browser: typeof navigator === "undefined" ? undefined : navigator.userAgent });
  const running = data.rounds.length > 0 && data.rounds[0].end === undefined;

  // The checks of a round still going move on their own: read them again while the sheet is open.
  useEffect(() => {
    engine.refreshDiagnostics(id);
    if (!running) return;
    const timer = setInterval(() => { engine.refreshDiagnostics(id); tick((n) => n + 1); }, 1000);
    return () => clearInterval(timer);
  }, [engine, id, running]);
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const copy = () => {
    if (typeof navigator === "undefined" || !navigator.clipboard) { setCopied("fail"); return; }
    navigator.clipboard.writeText(view.report).then(() => setCopied("ok"), () => setCopied("fail"));
  };
  const retry = () => { if (engine.retryNow(id)) tick((n) => n + 1); };
  const groups = [...view.groups, view.result];

  return (
    <Sheet onClose={onClose} title={l10n.t("transport.diag")} action={{ label: l10n.t("common.done"), onClick: onClose }}>
      <p className="diag-verdict" role="status">{view.sentence}</p>
      {groups.map((g) => (
        <div key={g.title}>
          <div className="fl">{g.title}</div>
          <div className="list diag-list">
            {g.rows.map((r, i) => (
              <div key={i} className="mi">
                <span className="mt">{r.label}</span>
                <span className="mv">{r.value}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
      <div className="list diag-acts">
        <button className="mi link-row" onClick={copy}>
          {copied === "ok" ? l10n.t("common.copied") : copied === "fail" ? l10n.t("transport.diag_copy_failed") : l10n.t("transport.diag_copy")}
        </button>
        <button className="mi link-row" disabled={status.state !== "none"} onClick={retry}>{l10n.t("transport.diag_retry")}</button>
      </div>
      <p className="gf">{l10n.t("transport.diag_footer")}</p>
    </Sheet>
  );
}
