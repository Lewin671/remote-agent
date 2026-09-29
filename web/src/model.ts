// View models derived from the decrypted state: projects, statuses, and
// what a session's events say is pending.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import type { ApprovalRequested, Event, PlanProposed, QuestionAsked, TokenUsage } from "./gen/remoteagent/v1/event_pb";
import { ApprovalDecision, InputStatus, SessionStatus } from "./gen/remoteagent/v1/event_pb";
import type { MachineView, Remote, SessionView } from "./remote";

// --- projects ---

export interface Project {
  key: string;
  name: string;
  sessions: SessionView[]; // most recent first
  updatedMs: number;
  paths: string[];
  machines: string[];
}

/** The project a session belongs to: its working directory's absolute
 * path, the same on every machine. */
export function projectKey(s: SessionView): string {
  return s.info?.cwd || "";
}

/** A project's name: the last folder of its path. */
export function projectName(key: string): string {
  return key.split("/").filter(Boolean).pop() || "未知项目";
}

export function projects(remote: Remote): Project[] {
  const byKey = new Map<string, Project>();
  for (const s of remote.sessions.values()) {
    const key = projectKey(s);
    let p = byKey.get(key);
    if (!p) {
      p = { key, name: projectName(key), sessions: [], updatedMs: 0, paths: [], machines: [] };
      byKey.set(key, p);
    }
    p.sessions.push(s);
    p.updatedMs = Math.max(p.updatedMs, Number(s.updatedMs));
    const cwd = s.info?.cwd;
    if (cwd && !p.paths.includes(cwd)) p.paths.push(cwd);
    const m = machineName(remote.machines.get(s.machineId));
    if (!p.machines.includes(m)) p.machines.push(m);
  }
  for (const p of byKey.values()) p.sessions.sort((a, b) => Number(b.updatedMs - a.updatedMs));
  return [...byKey.values()].sort((a, b) => b.updatedMs - a.updatedMs);
}

export function machineName(m?: MachineView): string {
  return m?.meta?.hostname?.replace(/\.local$/, "") || (m ? m.id.slice(0, 8) : "未知电脑");
}

/** Avatar color class, stable per project. */
export function avatarClass(key: string): string {
  let h = 0;
  for (const c of key) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return "c" + ((h % 4) + 1);
}

// --- sessions ---

export type Tone = "run" | "wait" | "idle" | "off";

export function exited(s: SessionView): boolean {
  return s.info?.status === SessionStatus.EXITED;
}

/** The session's dot color and a short status. */
export function statusOf(s: SessionView): { tone: Tone; text: string } {
  const i = s.info;
  if (!i) return { tone: "off", text: "读取中" };
  if (i.status === SessionStatus.EXITED) return { tone: "off", text: "已结束" };
  if (i.pendingApprovals > 0) return { tone: "wait", text: "等待审批" };
  if (i.pendingQuestions > 0) return { tone: "wait", text: "等你回答" };
  if (i.status === SessionStatus.BLOCKED) return { tone: "wait", text: "终端里有弹窗" };
  if (i.status === SessionStatus.RUNNING) return { tone: "run", text: "运行中" };
  if (i.status === SessionStatus.STARTING) return { tone: "run", text: "启动中" };
  return { tone: "idle", text: "空闲" };
}

export function needsAttention(s: SessionView): boolean {
  return !exited(s) && statusOf(s).tone === "wait";
}

export function titleOf(s: SessionView): string {
  return plain(s.info?.title || "") || "新会话";
}

/** Text for one-line previews: Markdown code marks removed. */
export function plain(text: string): string {
  return text.replace(/`([^`]*)`/g, "$1").replace(/\*\*([^*]+)\*\*/g, "$1").replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
}

/** Short relative time, like the mockup: 刚刚, 3 分钟前, 9:12, 昨天. */
export function when(ms: number | bigint, now = Date.now()): string {
  const t = Number(ms);
  if (!t) return "";
  const d = now - t;
  if (d < 60_000) return "刚刚";
  if (d < 3_600_000) return `${Math.floor(d / 60_000)} 分钟前`;
  const then = new Date(t);
  const today = new Date(now);
  if (then.toDateString() === today.toDateString()) return hhmm(t);
  const yesterday = new Date(now - 86_400_000);
  if (then.toDateString() === yesterday.toDateString()) return "昨天";
  return `${then.getMonth() + 1}月${then.getDate()}日`;
}

export function hhmm(ms: number): string {
  const d = new Date(ms);
  return `${d.getHours()}:${String(d.getMinutes()).padStart(2, "0")}`;
}

// --- chips ---

export interface ModeChip {
  cls: string;
  text: string;
}

/** The approval mode, read from the latest turn context. */
export function modeOf(s: SessionView): ModeChip | undefined {
  const i = s.info;
  if (!i || (!i.approvalsReviewer && !i.approvalPolicy && !i.collaborationMode)) return undefined;
  if (i.collaborationMode === "plan") return { cls: "m-plan", text: "计划" };
  if (i.sandbox === "danger-full-access" && i.approvalPolicy === "never") return { cls: "m-danger", text: "完全放行" };
  if (i.approvalsReviewer === "auto_review") return { cls: "m-auto", text: "自动" };
  return { cls: "m-ask", text: "询问" };
}

export function contextPercent(s: SessionView): number | undefined {
  const i = s.info;
  if (!i || i.contextWindow <= 0n) return undefined;
  return Math.min(100, Number((i.contextTokens * 100n) / i.contextWindow));
}

// --- events ---

export interface Derived {
  approvals: Map<string, ApprovalRequested>; // pending
  resolved: Map<string, { decision: ApprovalDecision; message: string }>;
  questions: Map<string, QuestionAsked>; // pending
  answered: Map<string, string[]>;
  inputs: Map<string, InputStatus>; // latest state per request ID
  running: boolean;
  usage?: TokenUsage;
  plan?: PlanProposed; // the latest plan
}

/** Pending approvals and questions, input states and the running turn. */
export function derive(events: Event[]): Derived {
  const d: Derived = { approvals: new Map(), resolved: new Map(), questions: new Map(), answered: new Map(), inputs: new Map(), running: false };
  for (const e of events) {
    const k = e.kind;
    switch (k.case) {
      case "approvalRequested":
        d.approvals.set(k.value.approvalId, k.value);
        break;
      case "approvalResolved":
        d.approvals.delete(k.value.approvalId);
        d.resolved.set(k.value.approvalId, { decision: k.value.decision, message: k.value.message });
        break;
      case "questionAsked":
        if (!k.value.async) d.questions.set(k.value.questionId, k.value);
        break;
      case "questionAnswered":
        d.questions.delete(k.value.questionId);
        d.answered.set(
          k.value.questionId,
          k.value.answers.flatMap((a) => a.answers),
        );
        break;
      case "turnStarted":
        d.running = true;
        break;
      case "turnEnded":
        d.running = false;
        d.questions.clear();
        break;
      case "inputState":
        d.inputs.set(k.value.requestId, k.value.status);
        break;
      case "tokenUsage":
        d.usage = k.value;
        break;
      case "planProposed":
        d.plan = k.value;
        break;
      case "sessionState":
        if (k.value.status === SessionStatus.EXITED) {
          d.running = false;
          d.approvals.clear();
          d.questions.clear();
        }
        break;
    }
  }
  return d;
}

export function eventTime(e: Event): number {
  return e.time ? timestampMs(e.time) : 0;
}

export function decisionText(d: ApprovalDecision): string {
  switch (d) {
    case ApprovalDecision.ALLOW:
      return "已允许";
    case ApprovalDecision.DENY:
      return "已拒绝";
    case ApprovalDecision.TIMEOUT:
      return "超时拒绝";
    case ApprovalDecision.CANCELED:
      return "已取消";
  }
  return "";
}

/** Splits Codex's "(Recommended)" marker off an option label. */
export function optionLabel(label: string): { text: string; recommended: boolean } {
  const m = label.match(/^(.*?)\s*\((Recommended|推荐)\)\s*$/i);
  return m ? { text: m[1], recommended: true } : { text: label, recommended: false };
}

export function shortPath(p: string): string {
  return p.replace(/^\/Users\/[^/]+/, "~").replace(/^\/home\/[^/]+/, "~");
}
