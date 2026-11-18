import * as i18n from "../i18n";
// The message queue and in-flight outgoing bubbles (design-system §6 输入框):
// every accepted input waiting to run, and local sends not yet confirmed in chat.

import { create } from "@bufbuild/protobuf";
import { useEffect, useRef, useState } from "react";
import type { TrayItem } from "../attach";
import { renderText } from "../core/text";
import {
  SessionCommandStatus,
  SessionOutgoingState,
  type SessionOutgoingView,
  type SessionPendingInputView,
} from "../gen/remoteagent/core/v1/session_view_pb";
import type { SentFile } from "../gen/remoteagent/v1/event_pb";
import {
  CommandSchema,
  type Command,
} from "../gen/remoteagent/v1/remote_pb";
import { LoaderCircle } from "../icons";
import { useFresh } from "../motion";
import { isSkeleton, skeletonMessage } from "../skeleton";
import { FileChips } from "./Files";
import { LocalThumbs } from "./Images";

export interface LocalSendAttachments {
  thumbs?: Blob[];
  imageSizes?: { width: number; height: number }[];
  files?: Array<Pick<SentFile, "name" | "sizeBytes" | "fileId"> & { file?: File }>;
  items?: TrayItem[];
  original?: boolean;
}

export function PendingBubbles({
  sessionId,
  pendingInputs,
  localAttachments,
  defaultExpandedAll = false,
  onSubmitCommand,
}: {
  sessionId: string;
  pendingInputs: readonly SessionPendingInputView[];
  localAttachments?: Map<string, LocalSendAttachments>;
  defaultExpandedAll?: boolean;
  onSubmitCommand: (slotKey: string, cmd: Command) => void;
}) {
  const [all, setAll] = useState(defaultExpandedAll);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const shown = all || pendingInputs.length <= 3 ? pendingInputs : pendingInputs.slice(0, 2);
  const ids = shown.map((p) => p.requestId);
  const fresh = useFresh(ids, true, { bulk: 3 });

  if (pendingInputs.length === 0) return null;

  return (
    <>
      {shown.map((pi) => {
        const id = pi.requestId;
        if (isSkeleton(id) || isSkeleton(pi.text)) {
          return (
            <div className={"pending-bubble me" + (fresh(id) ? " mo-rise" : "")} key={id}>
              {skeletonMessage()}
            </div>
          );
        }
        const att = localAttachments?.get(id);
        const message =
          pi.text ||
          (pi.imageCount > 0
            ? i18n.t("image.count", { count: pi.imageCount })
            : pi.fileCount > 0
              ? ""
              : i18n.t("queue.other_message"));
        const caption = (renderText(pi.caption) || renderText(pi.label))
          .replace(/\s*·(?:\s*·)+\s*/g, " · ")
          .replace(/^\s*·\s*|\s*·\s*$/g, "");
        const feedback = renderText(pi.feedback);
        const metaText = feedback && feedback !== caption ? `${caption} · ${feedback}` : caption;
        const cancelFailed = pi.cancel?.status === SessionCommandStatus.FAILED || pi.cancel?.status === SessionCommandStatus.UNKNOWN;
        const resumeFailed = pi.resume?.status === SessionCommandStatus.FAILED || pi.resume?.status === SessionCommandStatus.UNKNOWN;
        const showCancel = pi.canCancel && !pi.cancel?.busy;
        return (
          <div className={"pending-bubble me" + (fresh(id) ? " mo-rise" : "")} key={id}>
            <LocalThumbs blobs={att?.thumbs} sizes={att?.imageSizes} />
            <FileChips files={att?.files} />
            <button
              className={"pending-text" + (expanded.has(id) ? " expanded" : "")}
              onClick={() =>
                setExpanded((old) =>
                  new Set(old.has(id) ? [...old].filter((x) => x !== id) : [...old, id]),
                )
              }
            >
              {message}
            </button>
            <div className="pending-meta">
              <span>{metaText}</span>
              {pi.paused && (
                <button
                  disabled={!pi.canResume || !!pi.resume?.busy}
                  onClick={() =>
                    onSubmitCommand(
                      pi.resume?.slotKey ?? "",
                      create(CommandSchema, {
                        kind: {
                          case: "resumeInput",
                          value: { sessionId, requestId: id },
                        },
                      }),
                    )
                  }
                >
                  {resumeFailed ? i18n.t("common.retry") : i18n.t("composer.send")}
                </button>
              )}
              {showCancel && (
                <button
                  onClick={() =>
                    onSubmitCommand(
                      pi.cancel?.slotKey ?? "",
                      create(CommandSchema, {
                        kind: {
                          case: "cancelInput",
                          value: { sessionId, requestId: id },
                        },
                      }),
                    )
                  }
                >
                  {cancelFailed ? i18n.t("common.retry") : i18n.t("common.cancel")}
                </button>
              )}
            </div>
          </div>
        );
      })}
      {!all && pendingInputs.length > 3 && (
        <button className="more-row" onClick={() => setAll(true)}>
          {i18n.t("queue.more", { count: pendingInputs.length - 2 })}
        </button>
      )}
    </>
  );
}

export function OutgoingBubbles({
  sessionId,
  outgoing,
  localAttachments,
  onRetry,
  onRestore,
  onDismiss,
}: {
  sessionId: string;
  outgoing: readonly SessionOutgoingView[];
  localAttachments?: Map<string, LocalSendAttachments>;
  onRetry: (slotKey: string, cmd: Command) => void;
  onRestore: (requestId: string) => void;
  onDismiss: (requestId: string) => void;
}) {
  const retryGuard = useRef(new Set<string>());
  useEffect(() => {
    for (const id of [...retryGuard.current]) {
      const og = outgoing.find((item) => item.requestId === id);
      if (!og || (og.state !== SessionOutgoingState.FAILED && og.state !== SessionOutgoingState.UNKNOWN)) {
        retryGuard.current.delete(id);
      }
    }
  }, [outgoing]);

  if (outgoing.length === 0) return null;

  return (
    <>
      {outgoing.map((og) => {
        const att = localAttachments?.get(og.requestId) ?? (og.slotKey ? localAttachments?.get(og.slotKey) : undefined);
        const failed =
          og.state === SessionOutgoingState.FAILED || og.state === SessionOutgoingState.UNKNOWN;
        const unknown = og.state === SessionOutgoingState.UNKNOWN || og.feedbackUncertain;
        const sending = og.state === SessionOutgoingState.SENDING || og.state === SessionOutgoingState.SENT;
        const cls = failed ? (unknown ? " warn" : " fail") : sending ? " pending" : "";
        const feedback = renderText(og.feedback) || renderText(og.caption);
        const caption = renderText(og.caption) || i18n.t("composer.sending");
        const canRestore = og.canRestore && (!og.imageIds.length ? true : !!att?.items?.length);
        const missingUploadedAttachment =
          !unknown && (og.imageIds.length > 0 || og.fileIds.length > 0) && /not on this machine|upload it again/i.test(feedback);
        const canRetry = og.canRetry && !missingUploadedAttachment;
        return (
          <div key={og.requestId} className={"me" + cls}>
            <LocalThumbs blobs={att?.thumbs} sizes={att?.imageSizes} />
            <FileChips files={att?.files} />
            {og.text && <div className="mo-text">{og.text}</div>}
            <small>
              {failed ? (
                <>
                  {feedback}{" "}
                  {canRestore && (
                    <button onClick={() => onRestore(og.requestId)}>{i18n.t("composer.restore")}</button>
                  )}
                  {canRetry && (
                    <button
                      onClick={() => {
                        if (retryGuard.current.has(og.requestId)) return;
                        retryGuard.current.add(og.requestId);
                        onRetry(
                          og.slotKey,
                          create(CommandSchema, {
                            kind: {
                              case: "send",
                              value: {
                                sessionId,
                                requestId: og.requestId,
                                text: og.text,
                                mode: og.mode,
                                imageIds: [...og.imageIds],
                                fileIds: [...og.fileIds],
                              },
                            },
                          }),
                        );
                      }}
                    >
                      {i18n.t("common.retry")}
                    </button>
                  )}
                  {og.canDismiss && (
                    <button onClick={() => onDismiss(og.requestId)}>{i18n.t("session.dismiss_notice")}</button>
                  )}
                </>
              ) : (
                <span className="mo-small">
                  {sending && <LoaderCircle />} {caption}
                </span>
              )}
            </small>
          </div>
        );
      })}
    </>
  );
}
