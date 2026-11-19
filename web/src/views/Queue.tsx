import * as i18n from "../i18n";
// Pending tray above the composer (design-system §6 输入框): all unsent,
// waiting, paused, failed, and unconfirmed messages in fixed 7-group order.

import { useEffect, useState, type MouseEvent, type MutableRefObject, type PointerEvent } from "react";
import type { TrayItem } from "../attach";
import { renderText } from "../core/text";
import {
  SessionTrayGroupKind,
  SessionTrayRowActivity,
  SessionTrayTone,
  type SessionPendingTrayView,
  type SessionTrayItemView,
} from "../gen/remoteagent/core/v1/session_view_pb";
import type { SentFile } from "../gen/remoteagent/v1/event_pb";
import { ChevronDown, ChevronUp, LoaderCircle, Paperclip, X } from "../icons";
import { useFresh } from "../motion";
import { isSkeleton, skeletonMessage } from "../skeleton";

export interface LocalSendAttachments {
  thumbs?: Blob[];
  imageSizes?: { width: number; height: number }[];
  files?: Array<Pick<SentFile, "name" | "sizeBytes" | "fileId"> & { file?: File }>;
  items?: TrayItem[];
  original?: boolean;
}

export interface PendingTrayProps {
  tray?: SessionPendingTrayView;
  keyboardCollapsed?: boolean;
  onExpandKeyboard?: () => void;
  onTerminal?: () => void;
  onCancelItem: (requestId: string) => void;
  onIgnoreItem: (requestId: string) => void;
  onPromoteItem: (requestId: string) => void;
  onRestoreItem: (requestId: string) => void;
  onRetryItem: (requestId: string) => void;
  onResumeItem: (requestId: string) => void;
  onCancelAllPaused: () => void;
  onUndo: () => void;
  rowRefs?: MutableRefObject<Map<string, HTMLDivElement>>;
  defaultExpandedAll?: boolean;
}

const keepFocus = (e: PointerEvent | MouseEvent) => {
  e.preventDefault();
};

function toneClass(tone: SessionTrayTone): string {
  if (tone === SessionTrayTone.RED) return " red";
  if (tone === SessionTrayTone.ORANGE) return " orange";
  return "";
}

function rowBodyText(it: SessionTrayItemView): string {
  if (it.text) return it.text;
  if (it.foreignDimmed) return i18n.t("queue.other_message");
  if (it.imageCount > 0) return i18n.t("image.count", { count: it.imageCount });
  if (it.fileCount > 0) return "";
  return i18n.t("queue.other_message");
}

export function PendingTray({
  tray,
  keyboardCollapsed = false,
  onExpandKeyboard,
  onTerminal,
  onCancelItem,
  onIgnoreItem,
  onPromoteItem,
  onRestoreItem,
  onRetryItem,
  onResumeItem,
  onCancelAllPaused,
  onUndo,
  rowRefs,
  defaultExpandedAll = false,
}: PendingTrayProps) {
  const [all, setAll] = useState(defaultExpandedAll);
  const [openID, setOpenID] = useState<string | null>(null);
  const [kbExpanded, setKbExpanded] = useState(false);

  const totalCount = tray?.totalCount ?? 0;
  useEffect(() => {
    if (!keyboardCollapsed || totalCount === 0) {
      setKbExpanded(false);
    }
  }, [keyboardCollapsed, totalCount]);

  useEffect(() => {
    if (openID && !tray?.orderedItems.some((it) => it.requestId === openID)) {
      setOpenID(null);
    }
  }, [openID, tray?.orderedItems]);

  const ordered = tray?.orderedItems ?? [];
  const fresh = useFresh(
    ordered.map((it) => it.requestId),
    true,
    { bulk: 3 },
  );

  if (!tray || (totalCount === 0 && !tray.undo?.visible)) {
    return null;
  }

  if (keyboardCollapsed && !kbExpanded && totalCount > 0) {
    return (
      <div className="ptray">
        <button
          type="button"
          className="ptray-sum"
          onPointerDown={keepFocus}
          onClick={() => {
            setKbExpanded(true);
            onExpandKeyboard?.();
          }}
        >
          <span>
            {i18n.t("queue.keyboard_summary", { count: totalCount })}
            {tray.failedCount > 0 && (
              <b>{" · " + i18n.t("queue.keyboard_failed", { count: tray.failedCount })}</b>
            )}
          </span>
          <ChevronUp />
        </button>
      </div>
    );
  }

  const limit = ordered.length > 3 && !all ? 2 : ordered.length;
  const visibleIDs = new Set(ordered.slice(0, limit).map((it) => it.requestId));

  return (
    <div
      className="ptray"
      role="region"
      aria-label={totalCount > 0 ? i18n.t("queue.keyboard_summary", { count: totalCount }) : undefined}
    >
      {tray.groups.map((group) => {
        const visibleItems = group.items.filter((it) => visibleIDs.has(it.requestId));
        if (visibleItems.length === 0) return null;
        const gTone = toneClass(group.tone);
        return (
          <div className="ptray-grp" key={SessionTrayGroupKind[group.kind] ?? group.kind}>
            <div className={"ptray-gl" + gTone}>
              <span>{renderText(group.headerText)}</span>
              {group.canCancelAll && (
                <button
                  type="button"
                  disabled={tray.offline}
                  onPointerDown={keepFocus}
                  onClick={onCancelAllPaused}
                >
                  {i18n.t("queue.cancel_all")}
                </button>
              )}
            </div>
            {visibleItems.map((it) => {
              const id = it.requestId;
              if (isSkeleton(id) || isSkeleton(it.text)) {
                return (
                  <div className="ptray-row" key={id}>
                    <span className="ptray-lead" />
                    <div className="ptray-body">{skeletonMessage()}</div>
                  </div>
                );
              }
              const isOpen = openID === id;
              const rTone = toneClass(it.tone);
              const src = renderText(it.sourceLabel);
              const reason = renderText(it.reasonText);
              const canExpand = true;
              const toggleOpen = () => setOpenID((prev) => (prev === id ? null : id));
              const hasExpandOpts =
                isOpen &&
                it.activity === SessionTrayRowActivity.IDLE &&
                (it.canPromote || it.canRestore);

              return (
                <div
                  key={id}
                  data-tray-req={id}
                  ref={(el) => {
                    if (!rowRefs) return;
                    if (el) rowRefs.current.set(id, el);
                    else rowRefs.current.delete(id);
                  }}
                  className={
                    "ptray-row" +
                    (isOpen ? " open" : "") +
                    (fresh(id) ? " new" : "") +
                    (reason ? " has-why" : "")
                  }
                >
                  <span className="ptray-lead" aria-hidden="true">
                    {it.showSpinner ? <LoaderCircle /> : it.queueNumber > 0 ? it.queueNumber : null}
                  </span>
                  <div className="ptray-body">
                    <div
                      className={"ptray-hit" + (canExpand ? "" : " static")}
                      role="button"
                      tabIndex={0}
                      aria-expanded={isOpen}
                      onPointerDown={keepFocus}
                      onClick={toggleOpen}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          toggleOpen();
                        }
                      }}
                    >
                      <div className={"ptray-t" + (it.foreignDimmed ? " dimtxt" : "")}>
                        {src && <span className="src">{src + " · "}</span>}
                        {rowBodyText(it)}
                        {it.attachmentCount > 0 && (
                          <span
                            className="clip"
                            aria-label={i18n.t("queue.attachments_a11y", { count: it.attachmentCount })}
                          >
                            <Paperclip />
                            {it.attachmentCount}
                          </span>
                        )}
                      </div>
                      {reason && <div className={"ptray-why" + rTone}>{reason}</div>}
                    </div>
                    {hasExpandOpts && (
                      <div className="ptray-opts">
                        {it.canPromote && (
                          <button
                            type="button"
                            disabled={it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              setOpenID(null);
                              onPromoteItem(id);
                            }}
                          >
                            {i18n.t("queue.send_now")}
                          </button>
                        )}
                        {it.canRestore && (
                          <button
                            type="button"
                            disabled={it.serverBacked && it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              setOpenID(null);
                              onRestoreItem(id);
                            }}
                          >
                            {i18n.t("composer.restore")}
                          </button>
                        )}
                      </div>
                    )}
                  </div>
                  <div className="ptray-acts">
                    {it.activity === SessionTrayRowActivity.CANCELING ? (
                      <em>{i18n.t("queue.canceling")}</em>
                    ) : it.activity === SessionTrayRowActivity.SENDING ? (
                      <em>{i18n.t("queue.resuming")}</em>
                    ) : it.canIgnore ? (
                      <>
                        {onTerminal && (
                          <button
                            type="button"
                            disabled={it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              onTerminal();
                            }}
                          >
                            {i18n.t("session.go_terminal")}
                          </button>
                        )}
                        <button
                          type="button"
                          onPointerDown={keepFocus}
                          onClick={(e) => {
                            e.stopPropagation();
                            onIgnoreItem(id);
                          }}
                        >
                          {i18n.t("queue.ignore")}
                        </button>
                      </>
                    ) : (
                      <>
                        {it.canResume && (
                          <button
                            type="button"
                            disabled={it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              onResumeItem(id);
                            }}
                          >
                            {i18n.t("composer.send")}
                          </button>
                        )}
                        {it.canRetry && (
                          <button
                            type="button"
                            disabled={it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              onRetryItem(id);
                            }}
                          >
                            {i18n.t("common.retry")}
                          </button>
                        )}
                        {it.canCancel && (
                          <button
                            type="button"
                            className="x"
                            aria-label={i18n.t("common.cancel")}
                            disabled={it.serverBacked && it.networkActionsDisabled}
                            onPointerDown={keepFocus}
                            onClick={(e) => {
                              e.stopPropagation();
                              onCancelItem(id);
                            }}
                          >
                            <X />
                          </button>
                        )}
                      </>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        );
      })}
      {limit < ordered.length && (
        <button
          type="button"
          className="ptray-more"
          onPointerDown={keepFocus}
          onClick={() => setAll(true)}
        >
          <span>{i18n.t("queue.more_items", { count: ordered.length - limit })}</span>
          <ChevronDown />
        </button>
      )}
      {all && ordered.length > 3 && (
        <button
          type="button"
          className="ptray-more"
          onPointerDown={keepFocus}
          onClick={() => setAll(false)}
        >
          <span>{i18n.t("queue.collapse")}</span>
          <ChevronUp />
        </button>
      )}
      {tray.undo?.visible && (
        <div className="ptray-undo">
          <span>{renderText(tray.undo.label)}</span>
          <button
            type="button"
            disabled={!tray.undo.canUndo}
            onPointerDown={keepFocus}
            onClick={onUndo}
          >
            {i18n.t("queue.undo")}
          </button>
        </div>
      )}
    </div>
  );
}
