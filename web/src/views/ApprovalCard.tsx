import * as i18n from "../i18n";
// The dark card (design-system §6 终端卡): a command waiting to be allowed,
// dark in both themes, the same on the home, in a session's dock and on the
// approval page. What would run comes first, then why, then the buttons. On
// the home a command not shown whole cannot be allowed: the card offers
// 不允许 and 看完整命令 until it is opened in full. Docked (in a session, or
// on its own page) the command is whole and scrolls with the content above
// the buttons, which stay in view.

import { useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import type { ApprovalRequested } from "../gen/remoteagent/v1/event_pb";
import { approvalDisplay, approvalTruncated, approvalTruncatedMessage } from "../approval-display";
import { minutesLeft } from "../model";
import type { Remote } from "../remote";
import { isSkeleton, skeletonMessage } from "../skeleton";
import { useNow } from "../ui";
import { ChevronRight } from "../icons";
import { deadlineOf, useApproval } from "./actions";

export function approvalTitle(a: ApprovalRequested): string {
  return a.command ? i18n.t("list_state.android_request_command") : i18n.t("list_state.request_permission", { tool: approvalDisplay(a.toolName) });
}

export function approvalText(a: ApprovalRequested): string {
  return approvalDisplay(a.command || a.toolInputJson || a.toolName);
}

/** Whole lines a card's command shows before 看完整命令. */
export const CARD_LINES = 5;
/** Lines it keeps while a reason is being written under it. */
const REASON_LINES = 2;

/** How many lines el's text takes as laid out, or, with no layout to
 * measure, how many lines the text has. */
function linesOf(el: HTMLElement | null, text: string): number {
  const own = text.split("\n").length;
  if (!el) return own;
  const cs = getComputedStyle(el);
  const line = parseFloat(cs.lineHeight);
  const content = el.scrollHeight - (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0);
  // A line height in pixels, not a bare ratio, is what laid-out text has.
  const shown = line > 4 && content > 0 ? Math.round(content / line) : 0;
  return Math.max(shown, own);
}

export function ApprovalCard({
  remote,
  sid,
  a,
  title,
  where,
  onOpen,
  timeoutNote,
  docked,
  head: dockHead,
  onDenying,
}: {
  remote: Remote;
  sid: string;
  a: ApprovalRequested;
  /** The session's title, which opens it; none inside the session. */
  title?: string;
  /** 「项目 · 机器」 under the title. */
  where?: string;
  onOpen?: () => void;
  /** The home's card says what the end of the time does: 「，超时拒绝」. */
  timeoutNote?: boolean;
  /** Docked: the whole command in a scrolling content area, with the
   * buttons under it. */
  docked?: boolean;
  /** A docked card's head in place of the card's own: the dock's line,
   * which says what else waits and folds it. */
  head?: ReactNode;
  /** Writing a reason, which keeps a docked card open whatever the room. */
  onDenying?: (open: boolean) => void;
}) {
  const now = useNow(1000);
  const ap = useApproval(remote, sid, a);
  const [full, setFull] = useState(false);
  const [denying, setDenyingState] = useState(() => remote.demoFlag("deny"));
  const setDenying = (v: boolean) => {
    setDenyingState(v);
    onDenying?.(v);
  };
  const cmd = useRef<HTMLPreElement>(null);
  const text = approvalText(a);
  const [lines, setLines] = useState(() => linesOf(null, "❯ " + text));
  useLayoutEffect(() => {
    // Measured open, or with no clamp: a clamped box's scroll height is
    // still the whole text's, so either way it counts every line.
    const measure = () => setLines(linesOf(cmd.current, "❯ " + text));
    measure();
    if (typeof ResizeObserver === "undefined" || !cmd.current) return;
    const ro = new ResizeObserver(measure);
    ro.observe(cmd.current);
    return () => ro.disconnect();
  }, [text]);
  const reason = remote.approvalReasons.get(a.approvalId) ?? "";
  const setReason = (v: string) => {
    if (v) remote.approvalReasons.set(a.approvalId, v);
    else remote.approvalReasons.delete(a.approvalId);
    remote.touch();
  };
  const deadline = deadlineOf(a);
  const expired = deadline > 0 && now >= deadline;
  const truncated = approvalTruncated(a);
  const unlinked = remote.unlinked;
  // An answer whose outcome is unknown holds: only a retry of it may go.
  const locked = !!ap.busy || expired || (!!ap.error && ap.unknown) || unlinked;
  const kind = approvalTitle(a);
  const head = (right: ReactNode, hot = false) => (
    <div className="k">
      <i />
      {kind}
      {right && <span className={hot ? "hot" : ""}>{right}</span>}
    </div>
  );
  if (isSkeleton(a))
    return (
      <div className="tc">
        {head("")}
        <div className="y">{skeletonMessage()}</div>
      </div>
    );
  // Submitted: one line of where it stands; the next item comes up when
  // this one is gone from the session.
  if (ap.busy || ap.error) {
    const allow = (ap.busy || "").startsWith("allow");
    return (
      <div className="tc line" role="status">
        <div className="k">
          <i />
          {kind}
          {ap.error ? (
            <span className="red">{i18n.t("approval.delivery_failed", { reason: ap.error })}</span>
          ) : (
            <span>{allow ? i18n.t("approval.allowed") : i18n.t("approval.denied")}</span>
          )}
        </div>
        {ap.error && <button onClick={ap.retry}>{i18n.t("common.retry")}</button>}
      </div>
    );
  }
  const left = deadline - now;
  const time = unlinked ? i18n.t("list_state.unlinked") : deadline ? timeoutNote && left > 0 ? i18n.t("approval.timeout_note", { remaining: minutesLeft(deadline, now) }) : minutesLeft(deadline, now) : "";
  const more = docked ? 0 : lines - CARD_LINES;
  const cut = more > 0 && !full;
  const clamp = docked ? 0 : denying ? REASON_LINES : cut ? CARD_LINES : 0;
  const always = remote.machines.get(remote.sessions.get(sid)?.machineId ?? "")?.meta?.capabilities?.approvalAlways;
  const command = (
    <>
      <pre ref={cmd} className={"cmd" + (clamp ? " clamp" : "")} style={clamp ? ({ "--lines": clamp } as CSSProperties) : undefined}>
        <b>❯ </b>
        {text}
      </pre>
      {!denying && (cut ? (
        <div className="ml">{i18n.t("markdown.ios_hidden_lines", { count: more })}</div>
      ) : full && more > 0 ? (
        <div className="ml">
          {i18n.t("markdown.line_count", { count: lines })}
          <button aria-expanded onClick={() => setFull(false)}>{i18n.t("markdown.collapse")}</button>
        </div>
      ) : null)}
      {truncated && <div className="ml red">{approvalTruncatedMessage()}</div>}
      {!denying && a.description && <div className="y">{approvalDisplay(a.description)}</div>}
    </>
  );
  const actions = (
    <>
      {denying ? (
        <>
          <textarea
            className="inp"
            autoFocus
            rows={1}
            aria-label={i18n.t("approval.denial_reason")}
            placeholder={i18n.t("approval.reason_placeholder")}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <div className="btns decide">
            <button className="btn" onClick={() => setDenying(false)}>{i18n.t("common.cancel")}</button>
            <button className="btn pri" disabled={locked} onClick={() => void ap.resolve(false, reason.trim())}>{i18n.t("approval.deny")}</button>
          </div>
        </>
      ) : (
        <div className="btns decide">
          <button className="btn" disabled={locked} onClick={() => setDenying(true)}>{i18n.t("approval.deny")}</button>
          {cut ? (
            <button className="btn pri" aria-expanded={false} onClick={() => setFull(true)}>{i18n.t("approval.full_command")}</button>
          ) : (
            <button className="btn pri" disabled={locked || truncated} onClick={() => void ap.resolve(true)}>{i18n.t("approval.allow")}</button>
          )}
        </div>
      )}
      {always && !denying && !cut && (
        <div className="ml">
          <button disabled={locked || truncated} onClick={() => void ap.resolve(true, "", true)}>{i18n.t("approval.always_allow")}</button>
        </div>
      )}
    </>
  );
  if (docked)
    return (
      <div className="tc">
        {dockHead ?? head(time, !unlinked && deadline > 0 && left < 60_000)}
        <div className="pending-content">
          <div className="pending-scroll">{command}</div>
          <div className="pending-actions">{actions}</div>
        </div>
      </div>
    );
  return (
    <div className="tc">
      {head(time, !unlinked && deadline > 0 && left < 60_000)}
      {title !== undefined && (
        <h4>
          <button onClick={onOpen}>
            <span>{title}</span>
            <ChevronRight />
          </button>
        </h4>
      )}
      {where && <div className="m">{where}</div>}
      {command}
      {actions}
    </div>
  );
}
