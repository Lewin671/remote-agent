// Tool calls in the chat: finished rows and their folded runs, a card for
// each tool that runs or whose end was never heard, and a tool's detail
// page (client-v2 耗时工具与执行反馈). Only what the daemon reported is
// shown: no stdout stream, progress or "stuck" guess (architecture §13).

import { useState } from "react";
import type { ApprovalRequested, ToolCall } from "../gen/remoteagent/v1/event_pb";
import { ToolKind, type ApprovalDecision } from "../gen/remoteagent/v1/event_pb";
import { backgroundNote, clock, decisionText, duration, toolClock, toolName as toolKindName, toolsSummary, type Derived, type RunningTool } from "../model";
import type { Remote } from "../remote";
import { Exp, NavBar, useNow } from "../ui";
import { imagesNote } from "../imageview";
import { ImageStrip } from "./Images";

/** Shortens the absolute paths in a tool title (see shortenPaths), or a
 * whole path such as a file edit's title, spaces and all. */
export type Short = { text: (title: string) => string; path: (path: string) => string };
export const asIs: Short = { text: (x) => x, path: (x) => x };

/** A tool row's title, the same while it runs and once done: a file
 * edit reads "编辑 <path>". */
export function toolTitle(t: Pick<ToolCall, "kind" | "title" | "itemType">, short: Short): string {
  // A file edit's title is one path, which may hold spaces.
  return t.kind === ToolKind.FILE_CHANGE ? "编辑 " + short.path(t.title) : short.text(t.title || t.itemType);
}

/** What a running tool does, in full: a shell command's command line, else
 * its title (the input may be JSON the title already sums up). */
export function commandOf(t: Pick<RunningTool, "input" | "title" | "itemType">): string {
  if (t.input) {
    try {
      const v = JSON.parse(t.input);
      if (v && typeof v === "object" && typeof v.command === "string") return v.command;
    } catch {
      return t.input;
    }
  }
  return t.title || t.itemType;
}



/** A tool an interrupt may have left running in the background. */
function backgroundName(t: Pick<RunningTool, "itemType" | "kind">): string {
  const name = toolKindName(t);
  return name === "命令" ? "后台命令" : `${name}（后台）`;
}

/** Whether a pending approval is for this tool: the daemon gives both the
 * same digest of the call's input (Claude Code). Codex's carry none. */
export function awaitingApproval(t: RunningTool, approvals: Iterable<ApprovalRequested>): boolean {
  if (!t.inputDigest) return false;
  for (const a of approvals) if (a.inputDigest === t.inputDigest) return true;
  return false;
}

/** A running tool, or one whose end was never heard, as one line in the
 * chat (client-v7): what it is, its state and time since its start. 详情
 * opens its command and the ways to see more in place. The time stops
 * while this client is cut off from the server. */
export function ActiveTool({
  tool,
  d,
  unsettled,
  aborted,
  remote,
  onDetail,
  onTerminal,
  full,
}: {
  tool: RunningTool;
  d: Derived;
  unsettled?: boolean;
  aborted?: boolean;
  remote: Remote;
  onDetail?: () => void;
  onTerminal?: () => void;
  full?: boolean;
}) {
  const online = remote.conn === "online";
  const now = useNow(1000, online && !unsettled && tool.since > 0);
  const [open, setOpen] = useState(!!full);
  const lostAt = online ? undefined : (remote.lostAt ?? now);
  const c = unsettled ? undefined : toolClock(tool.since, now, lostAt);
  const approval = !unsettled && awaitingApproval(tool, d.approvals.values());
  let state: string, desc: string;
  if (unsettled) {
    state = "状态待确认";
    desc = aborted ? "本轮已中断，没有收到这次调用的结果；中断不保证结束后台进程，请到终端确认。" : "本轮已结束，没有收到这次调用的结果；进程是否结束要到终端确认。";
  } else if (!online) {
    state = "等待同步";
    desc = "未连接到服务器，暂时无法确认是否还在运行。";
  } else if (approval) {
    state = "等待审批";
    desc = "等你审批，批准前不会执行。";
  } else {
    state = "处理中";
    desc = full ? "尚未收到工具结果。实时输出请去终端查看。" : "尚未收到结果 · 可去终端查看";
  }
  const elapsed = unsettled ? "—" : !c ? "" : c.frozen ? `上次 ${clock(c.ms)}` : full ? clock(c.ms) : duration(c.ms);
  const off = unsettled || !online;
  const what = full ? "完整命令" : `${aborted ? backgroundName(tool) : toolKindName(tool)} · ${state}`;
  return (
    <div className={"active-tool" + (off ? " off" : "") + (approval ? " wait" : "")}>
      <button className="at-sum" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="at-what">
          <span className={off || approval ? "" : "spin"}>◌</span> {what} <span className="elapsed">{elapsed}</span>
        </span>
        <span className="muted">
          {open ? (
            <>
              收起 <Exp up />
            </>
          ) : (
            <>
              详情 <Exp />
            </>
          )}
        </span>
      </button>
      {open && (
        <div className="tool-details">
          <pre>{commandOf(tool)}</pre>
          <div className="td">{desc}</div>
          {!full && (
            <div className="ta">
              {onDetail && <button onClick={onDetail}>查看详情 ›</button>}
              {online ? onTerminal && <button onClick={onTerminal}>去终端 ›</button> : <span className="muted">终端不可用</span>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function ToolRow({ t, short, decision, by }: { t: ToolCall; short: Short; decision?: ApprovalDecision; by?: string }) {
  const decided = decision !== undefined ? decisionText(decision) + (by ? ` · ${by}` : "") : "";
  const [open, setOpen] = useState(false);
  const ok = t.status !== "failed" && (t.exitCode === undefined || t.exitCode === 0);
  const secs = Number(t.durationMs) / 1000;
  const out = t.output.replace(/\n+$/, "");
  const lines = out ? out.split("\n") : [];
  // The details keep the original: the tool's input, or else the title as
  // the CLI wrote it when paths were shortened for the row.
  const raw = t.input && t.input !== t.title ? t.input : toolTitle(t, asIs) !== toolTitle(t, short) ? t.title || t.itemType : "";
  return (
    <>
      <button className="tool" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className={ok ? "ok" : "x"}>{ok ? "✓" : "✕"}</span>
        <code>{toolTitle(t, short)}</code>
        {!ok && t.exitCode !== undefined ? (
          // A failed call keeps its exit code, with the approval beside it.
          <span className="exit">
            退出码 {t.exitCode}
            {decided && <span className="muted"> · {decided}</span>}
          </span>
        ) : decided ? (
          <span className="muted">{decided}</span>
        ) : secs >= 1 ? (
          <span className="muted">{Math.round(secs)}s</span>
        ) : null}
      </button>
      <ImageStrip refs={t.images} className="under-tool" />
      {open && raw && <div className="toolout">{raw}</div>}
      {(open || !ok) && lines.length > 0 && (
        <div className="toolout">
          {open ? out : lines[0]}
          {open && t.outputTruncated && <span className="more">{"\n"}…（已截断）</span>}
          {!open && lines.length > 1 && (
            <span className="more" onClick={() => setOpen(true)}>
              {"\n"}… 还有 {lines.length - 1} 行，展开
            </span>
          )}
        </div>
      )}
    </>
  );
}

export function ToolGroup({ calls, short }: { calls: ToolCall[]; short: Short }) {
  const [open, setOpen] = useState(false);
  // Pictures the calls returned stay in view with the group folded.
  const images = calls.flatMap((c) => c.images);
  return (
    <>
      <button className="tool tgrp" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className="ok">✓</span>
        <b>
          {toolsSummary(calls)}
          {imagesNote(images.length)}
        </b>
        <span className="muted">
          <Exp up={open} />
        </span>
      </button>
      {!open && <ImageStrip refs={images} className="under-tool" />}
      {open && (
        <div className="grpbody">
          {calls.map((t, i) => (
            <ToolRow key={i} t={t} short={short} />
          ))}
        </div>
      )}
    </>
  );
}

/** One running tool on its own page: everything received about it, the
 * way to the terminal, and interrupting the turn. No stop button for the
 * tool alone: the CLIs offer none (architecture §13). */
export function ToolPage({
  remote,
  d,
  seq,
  agent,
  sub,
  cwd,
  onBack,
  onTerminal,
  onInterrupt,
}: {
  remote: Remote;
  d: Derived;
  seq?: bigint;
  agent: string;
  sub: string;
  cwd: string;
  onBack: () => void;
  onTerminal: () => void;
  onInterrupt: () => void;
}) {
  const tool = d.tools.find((t) => t.seq === seq) ?? d.tools[0];
  const loose = !tool ? (d.unsettled.find((t) => t.seq === seq) ?? d.unsettled[0]) : undefined;
  const shown = tool ?? loose;
  const online = remote.conn === "online";
  const now = useNow(1000, online && !!tool && tool.since > 0);
  const c = tool ? toolClock(tool.since, now, online ? undefined : (remote.lostAt ?? now)) : undefined;
  return (
    <div className="page">
      <NavBar
        onBack={onBack}
        title={
          <>
            <span className="tname">工具详情</span>
            <span className="agent-id">{agent}</span>
          </>
        }
        sub={sub}
      />
      <div className="body pad">
        {shown ? (
          <>
            <div className="label flush">
              {toolKindName(shown)} · {tool ? (online ? "处理中" : "等待同步") : "状态待确认"}
              {c && <span>调用耗时 {c.frozen ? "上次 " : ""}{duration(c.ms)}</span>}
            </div>
            <div className="crumb">{cwd}</div>
            <ActiveTool tool={shown} d={d} unsettled={!tool} aborted={!!loose && d.abortedTools.has(loose.seq)} remote={remote} full />
            <div className="hint">耗时不代表进度；没有输出也可能仍在运行。</div>
          </>
        ) : (
          <div className="hint">这次调用已经结束，结果在聊天里。</div>
        )}
      </div>
      <div className="dock">
        <div className="btns">
          <button className="btn" onClick={onTerminal} disabled={!online}>
            去终端看实时画面
          </button>
        </div>
        {d.running && (
          <div className="foot">
            <span>需要停止这一轮？</span>
            <button className="link red" onClick={onInterrupt} disabled={!online}>
              中断本轮
            </button>
          </div>
        )}
        <div className="draft-note">中断不保证结束后台进程；后台命令在终端里处理。</div>
      </div>
    </div>
  );
}

/** A call that went on in the background: its event is its start, and
 * nothing reliable says when it ends, so it is never shown as done. */
export function BackgroundTool({ call, short, remote, onTerminal }: { call: ToolCall; short: Short; remote: Remote; onTerminal?: () => void }) {
  const online = remote.conn === "online";
  const [open, setOpen] = useState(false);
  const name = toolKindName(call);
  return (
    <div className="active-tool off bg">
      <button className="at-sum" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="at-what">
          ◌ {name === "命令" ? "后台命令" : `${name}（后台）`} · 状态待确认 <span className="elapsed">{call.backgroundId ? <span className="mono">{call.backgroundId}</span> : "—"}</span>
        </span>
        <span className="muted">
          {open ? (
            <>
              收起 <Exp up />
            </>
          ) : (
            <>
              详情 <Exp />
            </>
          )}
        </span>
      </button>
      {open && (
        <div className="tool-details">
          <pre>{call.kind === ToolKind.COMMAND || call.kind === ToolKind.UNSPECIFIED ? commandOf({ input: call.input, title: toolTitle(call, short), itemType: call.itemType }) : toolTitle(call, short)}</pre>
          <div className="td">{backgroundNote(call.background)}</div>
          <div className="ta">
            <span />
            {online && onTerminal ? <button onClick={onTerminal}>去终端 ›</button> : <span className="muted">终端不可用</span>}
          </div>
        </div>
      )}
    </div>
  );
}
