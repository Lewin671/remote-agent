import * as i18n from "../i18n";
// Tool calls in the chat (design-system §6 工具调用): one line of 13 ink2
// per call, with finished runs folded; the command running now is its own
// gray block at the end of the chat, with how long it has run.
import { useRef, type ReactNode } from "react";
import { renderText } from "../core/text";
import { SessionToolStatus, type SessionToolCallItem, type SessionToolGroupItem } from "../gen/remoteagent/core/v1/session_view_pb";
import { ToolKind } from "../gen/remoteagent/v1/event_pb";
import { Check, LoaderCircle, X } from "../icons";
import { CopyButton } from "../markdown";
import { Exp, StateMark, useNow } from "../ui";
import { ImageStrip } from "./Images";
import { useRowExpanded } from "./RowState";

function formatClock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  return h > 0 ? `${h}:${String(m % 60).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}` : `${m}:${String(s % 60).padStart(2, "0")}`;
}

/** The fullest original text available; display-only path shortening never enters here. */
export function commandOf(t: { rawInput: string; title: string; toolKind: ToolKind }): string {
  if (t.toolKind === ToolKind.FILE_CHANGE || t.rawInput === t.title) return t.title;
  if (t.rawInput) {
    try {
      const v = JSON.parse(t.rawInput);
      // Only an object is a structured tool payload; a shell command can
      // itself be valid JSON (true, a quoted executable, etc.).
      if (!v || typeof v !== "object" || Array.isArray(v)) return t.rawInput;
      for (const key of ["command", "CommandLine", "cmd"]) {
        if (v && typeof v[key] === "string") return v[key];
      }
    } catch {
      return t.rawInput;
    }
    return t.title;
  }
  return t.title;
}

function runningPreview(t: SessionToolCallItem): string {
  if (t.rawInput) {
    try {
      const v = JSON.parse(t.rawInput);
      if (v && typeof v.command === "string") return v.command;
    } catch {
      return t.rawInput;
    }
  }
  return t.title || (t.toolKind === ToolKind.COMMAND ? i18n.t("tools.command") : i18n.t("tools.tool"));
}

// Change geometry once so the virtual list measures only the final size.
// Height animation would resize and reposition the list on every frame.
function CommandRow({ name, title, text, className = "single", mark, meta }: { name: string; title: string; text: string; className?: string; mark?: ReactNode; meta?: ReactNode }) {
  const [open, setOpen] = useRowExpanded(`command/${name}`);
  // A pointer-down may clear the selection before click gets to read it.
  const hadSelection = useRef(false);
  const toggle = () => {
    if (!window.getSelection()?.toString()) setOpen(!open);
  };
  return (
    <div className="tool-entry">
      <div
        className={`tool ${className} command-row${open ? " expanded" : ""}`}
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onPointerDown={() => {
          hadSelection.current = !!window.getSelection()?.toString();
        }}
        onClick={(e) => {
          const selecting = hadSelection.current;
          hadSelection.current = false;
          if (!selecting && !(e.target as Element).closest("button")) toggle();
        }}
        onKeyDown={(e) => {
          if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
            e.preventDefault();
            toggle();
          }
        }}
      >
        {mark}
        {open ? (
          <div className="tool-command">
            <code>{text}</code>
            <CopyButton text={text} />
          </div>
        ) : (
          <code>{title}</code>
        )}
        {meta}
      </div>
    </div>
  );
}

export function ToolCallView({
  item,
  itemSeq = 0n,
  online = true,
  lostAt,
  awaitingApproval = false,
}: {
  item: SessionToolCallItem;
  itemSeq?: bigint;
  online?: boolean;
  lostAt?: number;
  awaitingApproval?: boolean;
}) {
  const imageSeq = item.seq || itemSeq;
  const since = Number(item.sinceMs);
  const frozenAt = Number(item.frozenAtMs);
  const isRunning = item.status === SessionToolStatus.RUNNING;
  const now = useNow(1000, isRunning && online && !frozenAt && since > 0);
  const decision = renderText(item.decisionLabel);

  if (isRunning || (item.status === SessionToolStatus.UNCONFIRMED && since > 0)) {
    const unsettled = item.status === SessionToolStatus.UNCONFIRMED;
    const end = frozenAt > 0 ? frozenAt : online ? now : (lostAt ?? now);
    const ran = since > 0 ? i18n.t("tools.elapsed", { duration: formatClock(Math.max(0, end - since)) }) : "";
    const status = !online
      ? i18n.t("tools.elapsed_sync", { elapsed: ran ? ran + " · " : "" })
      : unsettled
        ? i18n.t("tools.background")
        : awaitingApproval
          ? i18n.t("list_state.approval")
          : ran;
    if (unsettled) {
      return (
        <CommandRow
          name={item.callId}
          title={runningPreview(item)}
          text={commandOf(item)}
          mark={<span className="sp faint" role="img" aria-label={i18n.t("tools.background")}><LoaderCircle /></span>}
          meta={<span className="muted">{status}</span>}
        />
      );
    }
    return (
      <CommandRow
        name={item.callId}
        className="now"
        title={runningPreview(item)}
        text={commandOf(item)}
        mark={<span className={online ? "sp" : "sp faint"} role="img" aria-label={online ? i18n.t("list_state.running") : i18n.t("tools.waiting_sync")}>{online ? <StateMark kind="run" /> : <LoaderCircle />}</span>}
        meta={<span className="muted">{status}</span>}
      />
    );
  }

  if (item.status === SessionToolStatus.BACKGROUND || item.isBackground) {
    const label = renderText(item.backgroundLabel) || i18n.t("tools.background");
    return (
      <>
        <CommandRow
          name={item.callId}
          title={item.title}
          text={commandOf(item)}
          mark={<span className="sp faint" role="img" aria-label={i18n.t("tools.background")}><LoaderCircle /></span>}
          meta={<span className="muted">{label}</span>}
        />
        <ImageStrip refs={item.imageViews} seq={imageSeq} className="under-tool" />
      </>
    );
  }

  const ok = item.status !== SessionToolStatus.ERROR && item.status !== SessionToolStatus.DENIED && (!item.hasExitCode || item.exitCode === 0);
  return (
    <>
      <CommandRow
        name={item.callId}
        title={item.title}
        text={commandOf(item)}
        mark={<span className={ok ? "ok" : "x"}>{ok ? <Check /> : <X />}</span>}
        meta={
          <>
            {!ok && item.hasExitCode && item.exitCode !== 0 && <span className="muted">{i18n.t("tools.exit_code", { code: String(item.exitCode) })}</span>}
            {decision && <span className="muted">{decision}</span>}
          </>
        }
      />
      <ImageStrip refs={item.imageViews} seq={imageSeq} className="under-tool" />
    </>
  );
}

export function ToolGroupView({ group, itemSeq = 0n }: { group: SessionToolGroupItem; itemSeq?: bigint }) {
  const [open, setOpen] = useRowExpanded("tools");
  const images = group.calls.flatMap((c) => c.imageViews);
  const anchors = group.calls.flatMap((c) =>
    c.imageViews.map((_, i) => ({ seq: c.seq || itemSeq, imageIndex: i })),
  );
  return (
    <>
      <div>
        <button className="tool single tgrp" onClick={() => setOpen(!open)} aria-expanded={open}>
          <span className="ok"><Check /></span>
          <span>{renderText(group.summary)}</span>
          <span className="muted"><Exp up={open} /></span>
        </button>
        {open && (
          <div className="grpbody">
            {group.calls.map((c, i) => (
              <CommandRow key={c.callId || i} name={c.callId || String(i)} title={c.title} text={commandOf(c)} />
            ))}
          </div>
        )}
      </div>
      <ImageStrip refs={images} anchors={anchors} className="under-tool" />
    </>
  );
}
