import * as i18n from "../i18n";
// The dark card (design-system §6 终端卡): a command waiting to be allowed,
// dark in both themes, the same on the home, in a session's dock and on the
// approval page. What would run comes first, then why, then the buttons. On
// the home a command not shown whole cannot be allowed: the card offers
// 不允许 and 看完整命令 until it is opened in full. Docked (in a session, or
// on its own page) the command is whole and scrolls with the content above
// the buttons, which stay in view.

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { approvalDisplay, approvalTruncatedMessage } from "../approval-display";
import { renderText } from "../core/text";
import { SessionCommandStatus, type SessionCommandState } from "../gen/remoteagent/core/v1/session_view_pb";
import { ChevronRight, LoaderCircle } from "../icons";
import { skeletonMessage } from "../skeleton";

/** Whole lines a card's command shows before 看完整命令. */
export const CARD_LINES = 5;

const latchedBottomIds = new Set<string>();

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

export interface ApprovalCardProps {
  approvalId?: string;
  kind: string;
  body: string;
  description?: string;
  truncated?: boolean;
  isSkeleton?: boolean;
  canAlwaysAllow?: boolean;
  alwaysLabel?: string;
  denyReasonPlaceholder?: string;
  timeText?: string;
  hot?: boolean;
  locked?: boolean;
  command?: SessionCommandState;
  reason?: string;
  onReasonChange?: (v: string) => void;
  defaultDenying?: boolean;
  /** The session's title, which opens it; none inside the session. */
  title?: string;
  /** 「项目 · 机器」 under the title. */
  where?: string;
  onOpen?: () => void;
  /** Docked: the whole command in a scrolling content area, with the
   * buttons under it. */
  docked?: boolean;
  /** A docked card's head in place of the card's own: the dock's line,
   * which says what else waits and folds it. */
  head?: ReactNode;
  /** Writing a reason, which keeps a docked card open whatever the room. */
  onDenying?: (open: boolean) => void;
  onResolve: (allow: boolean, reason: string, always: boolean) => void;
  onRetry?: () => void;
  onAbandon?: () => void;
}

export function ApprovalCard({
  approvalId = "",
  kind,
  body,
  description = "",
  truncated = false,
  isSkeleton = false,
  canAlwaysAllow = false,
  alwaysLabel = "",
  denyReasonPlaceholder = "",
  timeText = "",
  hot = false,
  locked: lockedProp = false,
  command,
  reason: controlledReason,
  onReasonChange,
  defaultDenying = false,
  title,
  where,
  onOpen,
  docked = false,
  head: dockHead,
  onDenying,
  onResolve,
  onRetry,
  onAbandon,
}: ApprovalCardProps) {
  const [full, setFull] = useState(false);
  const [denying, setDenyingState] = useState(defaultDenying);
  const [localReason, setLocalReason] = useState("");
  const [lastAllow, setLastAllow] = useState<boolean | undefined>(undefined);
  const lastSentReason = useRef("");
  const [reachedBottom, setReachedBottom] = useState(() => !docked || (!!approvalId && latchedBottomIds.has(approvalId)));
  const [shortViewport, setShortViewport] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const setDenying = (v: boolean) => {
    setDenyingState(v);
    onDenying?.(v);
  };
  const cmd = useRef<HTMLPreElement>(null);
  const text = approvalDisplay(body);
  const [lines, setLines] = useState(() => linesOf(null, "❯ " + text));

  const checkScrollBottom = useCallback(() => {
    if (!docked) {
      setReachedBottom(true);
      return;
    }
    if (approvalId && latchedBottomIds.has(approvalId)) {
      setReachedBottom(true);
      return;
    }
    const el = scrollRef.current;
    if (!el) return;
    const needsScroll = el.scrollHeight > el.clientHeight + 4;
    const atBottom = !needsScroll || el.scrollTop + el.clientHeight >= el.scrollHeight - 8;
    if (atBottom) {
      if (approvalId && el.clientHeight > 0) latchedBottomIds.add(approvalId);
      setReachedBottom(true);
    } else {
      setReachedBottom(false);
    }
  }, [approvalId, docked]);

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

  useLayoutEffect(() => {
    checkScrollBottom();
    const el = scrollRef.current;
    if (!docked || !el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(checkScrollBottom);
    ro.observe(el);
    return () => ro.disconnect();
  }, [docked, text, denying, checkScrollBottom]);

  useEffect(() => {
    if (!docked || !denying || typeof window === "undefined") {
      setShortViewport(false);
      return;
    }
    const check = () => {
      const h = window.visualViewport?.height ?? window.innerHeight;
      setShortViewport(h > 0 && h < 420);
    };
    check();
    window.visualViewport?.addEventListener("resize", check);
    window.addEventListener("resize", check);
    return () => {
      window.visualViewport?.removeEventListener("resize", check);
      window.removeEventListener("resize", check);
    };
  }, [docked, denying]);

  const reason = controlledReason ?? localReason;
  const setReason = (v: string) => {
    if (onReasonChange) onReasonChange(v);
    else setLocalReason(v);
  };

  const cmdBusy = !!command?.busy;
  const cmdError =
    command && (command.status === SessionCommandStatus.FAILED || command.status === SessionCommandStatus.UNKNOWN);
  const cmdErrorMsg = renderText(command?.errorText) || renderText(command?.statusText);
  const cmdStatusMsg = renderText(command?.statusText);
  // An answer whose outcome is unknown or frozen holds: only a retry or abandon may go.
  const locked =
    lockedProp ||
    cmdBusy ||
    !!command?.frozen ||
    !!command?.expired ||
    (!!cmdError && !command?.canRetry);

  const head = (right: ReactNode, isHot = false) => (
    <div className="k">
      <i />
      {kind}
      {right && <span className={isHot ? "hot" : ""}>{right}</span>}
    </div>
  );

  if (isSkeleton) {
    return (
      <div className="tc">
        {head("")}
        <div className="y">{skeletonMessage()}</div>
      </div>
    );
  }

  // While denying with reason, keep the full card open during delivery & failure so the draft and command remain visible.
  const holdDenyCard = denying && lastAllow === false && (cmdBusy || cmdError);

  // Submitted or failed: one line of where it stands (unless holding the open deny card).
  if ((cmdBusy || cmdError) && !holdDenyCard) {
    const busyLabel =
      cmdStatusMsg ||
      (lastAllow === false
        ? i18n.t("approval.denied")
        : lastAllow === true
          ? i18n.t("approval.allowed")
          : i18n.t("dock.waiting_confirmation"));
    return (
      <div className="tc line" role="status">
        <div className="k">
          <i />
          {kind}
          {cmdError ? (
            <span className="red">{i18n.t("approval.delivery_failed", { reason: cmdErrorMsg })}</span>
          ) : (
            <span>{busyLabel}</span>
          )}
        </div>
        {cmdError && command?.canRetry && onRetry && (
          <button onClick={onRetry}>{i18n.t("common.retry")}</button>
        )}
        {cmdError && command?.canAbandon && onAbandon && (
          <button onClick={onAbandon}>{i18n.t("common.cancel")}</button>
        )}
      </div>
    );
  }

  const submit = (allow: boolean, why = "", always = false) => {
    setLastAllow(allow);
    if (!allow) lastSentReason.current = why;
    onResolve(allow, why, always);
  };

  const submitDeny = () => {
    const trimmed = reason.trim();
    if (cmdError && lastAllow === false) {
      if (trimmed === lastSentReason.current && command?.canRetry && onRetry) {
        onRetry();
      } else {
        onAbandon?.();
        submit(false, trimmed, false);
      }
      return;
    }
    submit(false, trimmed, false);
  };

  const scrollToBottom = () => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8 || el.scrollHeight <= el.clientHeight + 4) {
      if (approvalId) latchedBottomIds.add(approvalId);
      setReachedBottom(true);
    }
  };

  const more = docked ? 0 : lines - CARD_LINES;
  const cut = more > 0 && !full;
  const clamp = docked ? 0 : cut ? CARD_LINES : 0;
  const desc = approvalDisplay(description);

  const commandBody = (
    <>
      <pre ref={cmd} className={"cmd" + (clamp ? " clamp" : "")} style={clamp ? ({ "--lines": clamp } as CSSProperties) : undefined}>
        <b>❯ </b>
        {text}
      </pre>
      {cut ? (
        <div className="ml">{i18n.t("markdown.ios_hidden_lines", { count: more })}</div>
      ) : full && more > 0 ? (
        <div className="ml">
          {i18n.t("markdown.line_count", { count: lines })}
          <button aria-expanded onClick={() => setFull(false)}>{i18n.t("markdown.collapse")}</button>
        </div>
      ) : null}
      {truncated && <div className="ml red">{approvalTruncatedMessage()}</div>}
      {!denying && desc && <div className="y">{desc}</div>}
    </>
  );

  const denyBusy = cmdBusy && lastAllow === false;
  const denyFailed = !!cmdError && lastAllow === false;
  const canSubmitDeny = !lockedProp && !cmdBusy && (!cmdError || !!command?.canRetry || !!command?.canAbandon);

  const actions = (
    <>
      {denying ? (
        <>
          <textarea
            className="inp"
            autoFocus
            rows={1}
            readOnly={denyBusy}
            aria-label={i18n.t("approval.denial_reason")}
            placeholder={denyReasonPlaceholder || i18n.t("approval.reason_placeholder")}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                if (!denyBusy) setDenying(false);
              } else if (e.key === "Enter" && !e.nativeEvent.isComposing && (!e.shiftKey || e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                if (canSubmitDeny) submitDeny();
              }
            }}
          />
          {denyFailed && <div className="deny-err" role="alert">{i18n.t("approval.delivery_failed", { reason: cmdErrorMsg })}</div>}
          <div className="btns decide">
            <button className="btn" disabled={denyBusy} onClick={() => setDenying(false)}>
              {i18n.t("common.cancel")}
            </button>
            <button className={"btn pri" + (denyBusy ? " spin-btn" : "")} disabled={!canSubmitDeny} onClick={submitDeny}>
              {denyBusy ? (
                <>
                  <LoaderCircle />
                  {i18n.t("approval.denying")}
                </>
              ) : denyFailed ? (
                i18n.t("approval.retry_deny")
              ) : (
                i18n.t("approval.deny")
              )}
            </button>
          </div>
        </>
      ) : (
        <div className="btns decide">
          <button className="btn" disabled={locked} onClick={() => setDenying(true)}>
            {i18n.t("approval.deny")}
          </button>
          {cut ? (
            <button className="btn pri" aria-expanded={false} onClick={() => setFull(true)}>
              {i18n.t("approval.full_command")}
            </button>
          ) : docked && !reachedBottom && !truncated ? (
            <button className="btn pri" disabled={locked} onClick={scrollToBottom}>
              {i18n.t("approval.scroll_to_view")}
            </button>
          ) : (
            <button className="btn pri" disabled={locked || truncated} onClick={() => submit(true, "", false)}>
              {i18n.t("approval.allow")}
            </button>
          )}
        </div>
      )}
      {canAlwaysAllow && !denying && !cut && reachedBottom && (
        <div className="ml">
          <button disabled={locked || truncated} onClick={() => submit(true, "", true)}>
            {alwaysLabel || i18n.t("approval.always_allow")}
          </button>
        </div>
      )}
      {denying && !docked && cut && (
        <div className="ml">
          <button onClick={() => setFull(true)}>{i18n.t("approval.full_command")}</button>
        </div>
      )}
    </>
  );

  if (docked) {
    return (
      <div className={"tc" + (denying ? " docked-deny" : "") + (shortViewport ? " short-vp" : "")}>
        {dockHead ?? head(timeText, hot)}
        <div className="pending-content">
          <div ref={scrollRef} className="pending-scroll" onScroll={checkScrollBottom}>{commandBody}</div>
          <div className="pending-actions">{actions}</div>
        </div>
      </div>
    );
  }

  return (
    <div className="tc">
      {head(timeText, hot)}
      {title !== undefined && (
        <h4>
          <button onClick={onOpen}>
            <span>{title}</span>
            <ChevronRight />
          </button>
        </h4>
      )}
      {where && <div className="m">{where}</div>}
      {commandBody}
      {actions}
    </div>
  );
}
