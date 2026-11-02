import type { ReactNode } from "react";
import type { Tone } from "../model";
import { Dot } from "../ui";
import { unreadLabel } from "../schedules";

/** Shared anatomy for session and schedule rows. */
export function SessionLine({
  tone,
  title,
  right,
  preview,
  meta,
  unread = 0,
  schedule = false,
  selected,
  last,
  onClick,
}: {
  tone: Tone;
  title: string;
  right?: ReactNode;
  preview?: ReactNode;
  meta?: ReactNode;
  unread?: number;
  schedule?: boolean;
  selected?: boolean;
  last?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`sess ${tone}${selected ? " sel" : ""}${last ? " last" : ""}${schedule ? " schedule-line" : ""}${unread ? " unread" : ""}`}
      onClick={onClick}
    >
      <Dot tone={tone} />
      <div className="sm">
        <div className="row1">
          <b>{title}</b>
          <span>{right}</span>
        </div>
        {preview && <div className="preview-line"><div className="pv">{preview}</div>{unread > 0 && <span className="schedule-unread" aria-label={`${unread} 条未读通知`}>{unreadLabel(unread)}</span>}</div>}
        {meta && <div className="mc">{meta}</div>}
      </div>
    </button>
  );
}
