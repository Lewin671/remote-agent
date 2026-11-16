// The 「连接」 group on a computer's settings page (design-system §6): the one
// place that says everything about this computer's direct path. A switch, a
// status row (the path in use now, with its state graphic), the response time
// and file speed that were really measured, and one sentence under it.
import { useEffect, useRef, useState, type ReactNode } from "react";
import * as l10n from "../i18n";
import { Swap, useGrow } from "../motion";
import { errorText, type Remote } from "../remote";
import { Banner, StateMark, Switch, useRemote } from "../ui";
import { directTrying, directView, type DirectView } from "./direct-status";

/** How long a direct path just turned on may say "trying" before it reports
 * anything: a round of attempts is bounded at about this long. */
const TRYING_MS = 10_000;

/** A row of this group. Born with the page it does not open again; one that
 * comes later (the switch turned on, a speed measured) opens to its height,
 * and every one closes when it goes. */
function GroupRow({ className, enter, children }: { className: string; enter: boolean; children: ReactNode }) {
  const ref = useRef(null as HTMLDivElement | null);
  useGrow(ref, { enter });
  return <div ref={ref} className={className}>{children}</div>;
}

/** The second line under a state word: opens and closes with the line. */
function SubLine({ text, enter }: { text: string; enter: boolean }) {
  const ref = useRef(null as HTMLElement | null);
  useGrow(ref, { enter });
  return <small ref={ref}><Swap k={text} tier="base">{text}</Swap></small>;
}

export function ConnectionGroup({ remote, id, online }: { remote: Remote; id: string; online: boolean }) {
  useRemote(remote);
  const [error, setError] = useState("");
  const [turnedOn, setTurnedOn] = useState(undefined as number | undefined);
  const armed = useRef(false);
  useEffect(() => { armed.current = true; }, []);
  useEffect(() => {
    if (turnedOn === undefined) return;
    const timer = setTimeout(() => setTurnedOn(undefined), TRYING_MS);
    return () => clearTimeout(timer);
  }, [turnedOn]);

  const status = remote.direct.status(id);
  let view: DirectView = directView(status);
  // Just turned on, nothing reported yet: "trying" is what is happening.
  if (turnedOn !== undefined && status.state === "none" && !status.reason) view = directTrying();
  const supported = view.kind !== "unsupported";
  const on = supported && view.kind !== "off";
  const enter = armed.current;
  // An offline computer has no status to show; the switch stays, it is this device's.
  const showStatus = !supported || (on && online);
  const name = l10n.t("transport.direct");

  const change = (next: boolean) => {
    try {
      remote.direct.setEnabled(id, next);
      setError("");
      setTurnedOn(next ? Date.now() : undefined);
    } catch (e) { setError(errorText(e)); }
  };

  return (
    <>
      {error && <Banner kind="err">{error}</Banner>}
      <div className="label">{l10n.t("transport.group")}</div>
      <div className="grp conn-grp" aria-live="polite">
        <div className="row">
          {name}<Switch label={name} on={on} disabled={!supported} onChange={change} />
        </div>
        {showStatus && (
          <GroupRow className="row dx" enter={enter}>
            <span className="dx-mark"><StateMark kind={view.mark} /></span>
            <span className="dx-text">
              <Swap k={view.kind + view.word} tier="base">{view.word}</Swap>
              {view.sub && <SubLine text={view.sub} enter={enter} />}
            </span>
          </GroupRow>
        )}
        {on && view.speeds.map((s) => (
          <GroupRow key={s.key} className="row speed" enter={enter}>
            <span>{s.name}</span><span className="v">{s.value}</span>
          </GroupRow>
        ))}
      </div>
      <p className="gf">{l10n.t("transport.footer")}</p>
    </>
  );
}
