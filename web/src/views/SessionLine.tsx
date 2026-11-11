import type { ReactNode } from "react";
import type { Tone } from "../model";
import { StateMark } from "../ui";

/** A session in a list (design-system §6 会话行): its state graphic in a
 * column of its own, the title and time, then 「状态 · 内容」 in ink2 and a
 * third line with what the list does not share. */
export function SessionLine({
  tone,
  title,
  right,
  preview,
  meta,
  selected,
  last,
  enter,
  onClick,
}: {
  tone: Tone;
  title: string;
  right?: ReactNode;
  preview?: ReactNode;
  meta?: ReactNode;
  selected?: boolean;
  last?: boolean;
  /** The session has only now come to this list. */
  enter?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`sess ${tone}${selected ? " sel" : ""}${last ? " last" : ""}${enter ? " mo-fade" : ""}`}
      onClick={onClick}
    >
      <span className="gut"><StateMark kind={tone} /></span>
      <div className="sm">
        <div className="row1">
          <b>{title}</b>
          <span>{right}</span>
        </div>
        {preview && <div className="pv">{preview}</div>}
        {meta && <div className="mc">{meta}</div>}
      </div>
    </button>
  );
}
