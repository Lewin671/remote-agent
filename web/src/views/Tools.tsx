// Tool calls in the chat: one line per call, with finished runs folded.
import { useRowExpanded } from "./RowState";
import type { ApprovalRequested, ToolCall } from "../gen/remoteagent/v1/event_pb";
import { ToolKind, type ApprovalDecision } from "../gen/remoteagent/v1/event_pb";
import { clock, decisionText, toolClock, toolsSummary, type Derived, type RunningTool } from "../model";
import type { Remote } from "../remote";
import { Exp, useNow } from "../ui";
import { ImageStrip } from "./Images";
import { Check, LoaderCircle, X } from "../icons";

export type Short = { text: (title: string) => string; path: (path: string) => string };
export const asIs: Short = { text: (x) => x, path: (x) => x };
export function toolTitle(t: Pick<ToolCall, "kind" | "title">, short: Short): string {
  return t.kind === ToolKind.FILE_CHANGE ? "编辑 " + short.path(t.title || "文件") : short.text(t.title || (t.kind === ToolKind.COMMAND ? "命令" : "工具"));
}
export function commandOf(t: Pick<RunningTool, "input" | "title" | "kind">): string {
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
  const status = !online ? `${c ? clock(c.ms) + " · " : ""}等待同步` : unsettled ? "后台运行" : awaitingApproval(tool, d.approvals.values()) ? "等待审批" : c ? clock(c.ms) : "";
  return <div className="tool single"><span className={online && !unsettled ? "sp" : "sp faint"} role="img" aria-label={!online ? "等待同步" : unsettled ? "后台运行" : "运行中"}><LoaderCircle /></span><code>{commandOf(tool)}</code><span className="muted">{status}</span></div>;
}
export function ToolRow({ t, short, decision, by, resultText }: { t: ToolCall; short: Short; decision?: ApprovalDecision; by?: string; resultText?: string }) {
  const ok = t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0);
  return <><div className="tool single"><span className={ok ? "ok" : "x"}>{ok ? <Check /> : <X />}</span><code>{toolTitle(t, short)}</code>{!ok && t.exitCode !== undefined && <span className="muted">退出码 {t.exitCode}</span>}{decision !== undefined && <span className="muted">{resultText || decisionText(decision)}{by && ` · ${by}`}</span>}</div><ImageStrip refs={t.images} className="under-tool" /></>;
}
export function ToolGroup({ calls, short }: { calls: ToolCall[]; short: Short }) {
  const [open, setOpen] = useRowExpanded("tools");
  const images = calls.flatMap((c) => c.images);
  return <><button className="tool single tgrp" onClick={() => setOpen(!open)} aria-expanded={open}><span className="ok"><Check /></span><span>{toolsSummary(calls)}</span><span className="muted"><Exp up={open} /></span></button>{open && <div className="grpbody">{calls.map((t, i) => <div className="tool single" key={i}><code>{toolTitle(t, short)}</code></div>)}</div>}<ImageStrip refs={images} className="under-tool" /></>;
}
export function BackgroundTool({ call, short }: { call: ToolCall; short: Short; remote: Remote; onTerminal?: () => void }) {
  return <div className="tool single"><span className="sp faint" role="img" aria-label="后台运行"><LoaderCircle /></span><code>{toolTitle(call, short)}</code><span className="muted">后台运行</span></div>;
}
