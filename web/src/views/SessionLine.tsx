import type { ReactNode } from "react";
import type { Tone } from "../model";
import { Dot } from "../ui";

/** Shared session row anatomy. */
export function SessionLine({
  tone,
  title,
  right,
  preview,
  meta,
  selected,
  last,
  onClick,
}: {
  tone: Tone;
  title: string;
  right?: ReactNode;
  preview?: ReactNode;
  meta?: ReactNode;
  selected?: boolean;
  last?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      className={`sess ${tone}${selected ? " sel" : ""}${last ? " last" : ""}`}
      onClick={onClick}
    >
      <Dot tone={tone} />
      <div className="sm">
        <div className="row1">
          <b>{title}</b>
          <span>{right}</span>
        </div>
        {preview && <div className="preview-line"><div className="pv">{preview}</div></div>}
        {meta && <div className="mc">{meta}</div>}
      </div>
    </button>
  );
}
