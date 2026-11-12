// Tool calls in the chat (design-system §6 工具调用): one line of 13 ink2
// per call, with finished runs folded; the command running now is its own
// gray block at the end of the chat, with how long it has run.
import { useLayoutEffect, useRef, type ReactNode } from "react";
import { CopyButton } from "../markdown";
import { useRowExpanded } from "./RowState";
import type { ApprovalRequested, ToolCall } from "../gen/remoteagent/v1/event_pb";
import { ToolKind, type ApprovalDecision } from "../gen/remoteagent/v1/event_pb";
import { clock, decisionText, toolClock, toolsSummary, type Derived, type RunningTool } from "../model";
import type { Remote } from "../remote";
import { Exp, StateMark, useNow } from "../ui";
import { ImageStrip } from "./Images";
import { Check, LoaderCircle, X } from "../icons";

export type Short = { text: (title: string) => string; path: (path: string) => string };
export const asIs: Short = { text: (x) => x, path: (x) => x };
export function toolTitle(t: Pick<ToolCall, "kind" | "title">, short: Short): string {
  return t.kind === ToolKind.FILE_CHANGE ? "编辑 " + short.path(t.title || "文件") : short.text(t.title || (t.kind === ToolKind.COMMAND ? "命令" : "工具"));
}
/** The fullest original text available; display-only path shortening never enters here. */
export function commandOf(t: Pick<RunningTool, "input" | "title" | "kind">): string {
  if (t.kind === ToolKind.FILE_CHANGE || t.input === t.title) return t.title;
  if (t.input) {
    try {
      const v = JSON.parse(t.input);
      // Only an object is a structured tool payload; a shell command can
      // itself be valid JSON (true, a quoted executable, etc.).
      if (!v || typeof v !== "object" || Array.isArray(v)) return t.input;
      for (const key of ["command", "CommandLine", "cmd"]) {
        if (v && typeof v[key] === "string") return v[key];
      }
    } catch { return t.input; }
    return t.title;
  }
  return t.title;
}

/** The same measured height transition for a group and each command within it. */
function ToolHeight({ children, className = "" }: { children: ReactNode; className?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const height = useRef<number | undefined>(undefined);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const next = el.getBoundingClientRect().height;
    if (height.current !== undefined && height.current !== next && el.animate && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      el.animate([{ height: `${height.current}px`, overflow: "hidden" }, { height: `${next}px`, overflow: "hidden" }], { duration: 200, easing: "cubic-bezier(.2,.8,.2,1)" });
    }
    height.current = next;
  });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    // A child command can grow without rerendering its enclosing group.
    const observer = new ResizeObserver(() => {
      if (!el.getAnimations?.().length) height.current = el.getBoundingClientRect().height;
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return <div ref={ref} className={className}>{children}</div>;
}

function CommandRow({ name, title, text, className = "single", mark, meta }: { name: string; title: string; text: string; className?: string; mark?: ReactNode; meta?: ReactNode }) {
  const [open, setOpen] = useRowExpanded(`command/${name}`);
  // A pointer-down may clear the selection before click gets to read it.
  const hadSelection = useRef(false);
  const toggle = () => { if (!window.getSelection()?.toString()) setOpen(!open); };
  return <ToolHeight className="tool-entry"><div className={`tool ${className} command-row${open ? " expanded" : ""}`} role="button" tabIndex={0} aria-expanded={open}
    onPointerDown={() => { hadSelection.current = !!window.getSelection()?.toString(); }}
    onClick={(e) => {
      const selecting = hadSelection.current;
      hadSelection.current = false;
      if (!selecting && !(e.target as Element).closest("button")) toggle();
    }}
    onKeyDown={(e) => { if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); toggle(); } }}>
    {mark}{open ? <div className="tool-command"><code>{text}</code><CopyButton text={text} /></div> : <code>{title}</code>}{meta}
  </div></ToolHeight>;
}
function runningPreview(t: RunningTool): string {
  if (t.input) {
    try { const v = JSON.parse(t.input); if (v && typeof v.command === "string") return v.command; } catch { return t.input; }
  }
  return t.title || (t.kind === ToolKind.COMMAND ? "命令" : "工具");
}
export function awaitingApproval(t: RunningTool, approvals: Iterable<ApprovalRequested>): boolean {
  for (const a of approvals) {
    if (t.inputDigest ? a.inputDigest === t.inputDigest : !!a.command && (a.command === t.input || a.command === t.title)) return true;
  }
  return false;
}
export function ActiveTool({ tool, d, unsettled, remote }: { tool: RunningTool; d: Derived; unsettled?: boolean; aborted?: boolean; remote: Remote }) {
  const online = remote.conn === "online";
  const now = useNow(1000, online && !unsettled && tool.since > 0);
  const c = toolClock(tool.since, now, online ? undefined : remote.lostAt ?? now);
  // Says what the time is: how long it has been running.
  const ran = c ? `已运行 ${clock(c.ms)}` : "";
  const status = !online ? `${ran ? ran + " · " : ""}等待同步` : unsettled ? "后台运行" : awaitingApproval(tool, d.approvals.values()) ? "等待审批" : ran;
  // Gone to the background, it is a tool line like the others.
  if (unsettled) return <CommandRow name={tool.callId} title={runningPreview(tool)} text={commandOf(tool)} mark={<span className="sp faint" role="img" aria-label="后台运行"><LoaderCircle /></span>} meta={<span className="muted">{status}</span>} />;
  // Running: the ink ring; with the clock stopped while offline, the still loader.
  return <CommandRow name={tool.callId} className="now" title={runningPreview(tool)} text={commandOf(tool)} mark={<span className={online ? "sp" : "sp faint"} role="img" aria-label={online ? "运行中" : "等待同步"}>{online ? <StateMark kind="run" /> : <LoaderCircle />}</span>} meta={<span className="muted">{status}</span>} />;
}
export function ToolRow({ t, short, decision, by, resultText }: { t: ToolCall; short: Short; decision?: ApprovalDecision; by?: string; resultText?: string }) {
  const ok = t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0);
  return <><CommandRow name={t.callId} title={toolTitle(t, short)} text={commandOf(t)} mark={<span className={ok ? "ok" : "x"}>{ok ? <Check /> : <X />}</span>} meta={<>{!ok && t.exitCode !== undefined && <span className="muted">退出码 {t.exitCode}</span>}{decision !== undefined && <span className="muted">{resultText || decisionText(decision)}{by && ` · ${by}`}</span>}</>} /><ImageStrip refs={t.images} className="under-tool" /></>;
}
export function ToolGroup({ calls, short }: { calls: ToolCall[]; short: Short }) {
  const [open, setOpen] = useRowExpanded("tools");
  const images = calls.flatMap((c) => c.images);
  return <><ToolHeight><button className="tool single tgrp" onClick={() => setOpen(!open)} aria-expanded={open}><span className="ok"><Check /></span><span>{toolsSummary(calls)}</span><span className="muted"><Exp up={open} /></span></button>{open && <div className="grpbody">{calls.map((t, i) => <CommandRow key={t.callId || i} name={t.callId || String(i)} title={toolTitle(t, short)} text={commandOf(t)} />)}</div>}</ToolHeight><ImageStrip refs={images} className="under-tool" /></>;
}
export function BackgroundTool({ call, short }: { call: ToolCall; short: Short; remote: Remote; onTerminal?: () => void }) {
  return <CommandRow name={call.callId} title={toolTitle(call, short)} text={commandOf(call)} mark={<span className="sp faint" role="img" aria-label="后台运行"><LoaderCircle /></span>} meta={<span className="muted">后台运行</span>} />;
}
