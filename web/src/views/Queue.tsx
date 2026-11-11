import { clockTime } from "../schedules";
import { FileChips } from "./Files";
import { isSkeleton, skeletonMessage } from "../skeleton";
// The message queue in full (client-v2 队列完整页): every message the
// computer accepted and has not written yet, in its order, whoever sent
// it, each cancellable; and what became of a cancel.

import { useState } from "react";
import { InputStage, InputStatus, SendMode, SessionStatus } from "../gen/remoteagent/v1/event_pb";
import { originLabel } from "../devices";
import { agentName, agentOf, machineName, queuedIds, queueReason, type Derived } from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { LocalThumbs } from "./Images";
import { promptIdOf } from "./Composer";
import { useFresh } from "../motion";

/** How a cancel ended, as the page reports it once the message leaves
 * the queue: canceled here, canceled elsewhere, or written first. */
export function cancelOutcome(status: InputStatus | undefined, mine: boolean): string {
  if (status === InputStatus.CANCELED) return mine ? "已取消" : "上一条已在另一客户端取消";
  if (status === InputStatus.SUBMITTED || status === InputStatus.UNCONFIRMED) return mine ? "上一条已送入终端，不能取消" : "";
  return "";
}

/** How far a message got while it is written to the terminal: past the
 * point where a cancel can still take it back. */
export function stageText(stage: InputStage | undefined): string {
  switch (stage) {
    case InputStage.PASTING_IMAGES:
      return "正在粘贴图片…";
    case InputStage.IMAGES_RECOGNIZED:
      return "图片已识别，正在发送…";
    case InputStage.SUBMITTING:
      return "已送入终端，等待确认";
  }
  return "";
}

/** Cancels a queued message, or sends one that is paused. */
async function queueAct(remote: Remote, sid: string, d: Derived, id: string, resume = false) {
  if (isSkeleton(id) || isSkeleton(d.accepted.get(id)?.text)) return;
  if (resume) { remote.resumes.set(id, { state: "sending", error: "" }); remote.touch(); }
  if (!resume) { remote.cancels.set(id, { state: "canceling", error: "" }); remote.touch(); }
  try {
    await remote.command(sid, resume ? { case: "resumeInput", value: { sessionId: sid, requestId: id } } : { case: "cancelInput", value: { sessionId: sid, requestId: id } });
    if (resume) { remote.resumes.delete(id); remote.touch(); }
  } catch (e) {
    if (resume) remote.resumes.set(id, { state: "failed", error: errorText(e) });
    if (!resume) remote.cancels.set(id, { state: "failed", error: errorText(e) });
    remote.touch();
  }
}

/** What a queued message says under its bubble: a few words for what it
 * waits for (design-system §6 输入框), and what can be done with it. `rank`
 * is its place among the `count` queued; `needed` says whether the user has
 * anything to do or know about it (a turn runs, it is paused, held or being
 * canceled), or whether it is only on its way to the terminal. */
export function queueLine(remote: Remote, session: SessionView, d: Derived, id: string, rank: number, count: number) {
  const sid = session.id;
  const a = d.accepted.get(id);
  const o = remote.outbox(sid).find((x) => x.sendId === id);
  const c = remote.cancels.get(id);
  const paused = d.paused.has(id);
  const resume = paused ? remote.resumes.get(id) : undefined;
  const held = d.inputs.get(id) === InputStatus.HELD_BY_CLI;
  const ctx = { me: remote.account.deviceId, devices: remote.devices, machine: machineName(remote.machines.get(session.machineId)) };
  const from = a?.origin && a.origin.deviceId !== remote.account.deviceId ? originLabel(a.origin, ctx) : "";
  const mode = a?.mode ?? o?.mode;
  const starting = session.info?.status === SessionStatus.STARTING;
  const queuedTime = a?.origin?.scheduledTaskId ? remote.events(sid).find(e => e.kind.case === "inputState" && e.kind.value.requestId === id && e.time)?.time : undefined;
  const scheduleLabel = a?.origin?.scheduledTaskId ? `定时任务${queuedTime ? " · " + clockTime(Number(queuedTime.seconds) * 1000) : ""} · ` : "";
  const status = c?.state === "canceling" ? "正在取消…" : c?.state === "failed" ? "取消没成功，仍在排队" : resume?.state === "failed" ? "发送没成功，仍已暂停" : resume?.state === "sending" ? "正在发送…" : paused ? "已暂停" : held ? "插入这一轮" : mode === SendMode.QUEUE ? "这一轮结束后发" : starting ? `等 ${agentName(agentOf(session))} 就绪后发出` : "插入这一轮";
  const needed = queueReason(session, d) !== "" || paused || held || !!c?.state || !!resume?.state || mode === SendMode.QUEUE || starting;
  const text = `${scheduleLabel}${status}${count > 1 && !paused && !held && !c?.state ? ` · 第 ${rank} 条` : ""}${from ? ` · ${from}` : ""}`;
  const buttons = (
    <>
      {paused && <button disabled={resume?.state === "sending"} onClick={() => void queueAct(remote, sid, d, id, true)}>{resume?.state === "failed" ? "重试" : "发送"}</button>}
      {c?.state === "failed" ? <button onClick={() => void queueAct(remote, sid, d, id)}>重试</button> : c?.state !== "canceling" && <button onClick={() => void queueAct(remote, sid, d, id)}>取消</button>}
    </>
  );
  return { text, buttons, needed };
}

/** Pending input is shown at the end of the chat, on every client. A message
 * this page sent (`own`) is not drawn here: its bubble, which has been on
 * screen since it was sent, takes the queue's look itself (Session's Pending). */
export function PendingBubbles({ remote, session, d, sentHere, own }: { remote: Remote; session: SessionView; d: Derived; sentHere?: { has(id: string): boolean }; own?: Set<string> }) {
  const ids = queuedIds(d);
  const [all, setAll] = useState(() => remote.demoFlag("queue-all"));
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const shown = all || ids.length <= 3 ? ids : ids.slice(0, 2);
  // A message that joins the queue rises in; those already there do not, and
  // neither does one this page has sent: it rose in when it was written.
  const fresh = useFresh(shown, true, { bulk: 3 });
  const sid = session.id;
  return <>
    {shown.map((id, i) => {
      // One this page sent is its own bubble's (Session's Pending) until the
      // chat holds it; it does not come back as a queue bubble after that.
      if (own?.has(id) || sentHere?.has(id)) return null;
      const a = d.accepted.get(id);
      if (isSkeleton(id) || isSkeleton(a?.text)) return <div className={"pending-bubble me" + (fresh(id) && !sentHere?.has(id) ? " mo-rise" : "")} key={id}>{skeletonMessage}</div>;
      const o = remote.outbox(sid).find((x) => x.sendId === id);
      const message = a?.text || o?.text || (id === promptIdOf(remote, sid, d) ? remote.prompts.get(sid) : undefined) || ((a?.imageIds.length ?? o?.imageIds?.length ?? 0) ? `${a?.imageIds.length ?? o?.imageIds?.length} 张图片` : (a?.files?.length ?? o?.files?.length ?? 0) ? "" : "另一客户端的消息");
      const line = queueLine(remote, session, d, id, i + 1, ids.length);
      return <div className={"pending-bubble me" + (fresh(id) && !sentHere?.has(id) ? " mo-rise" : "")} key={id}>
        <LocalThumbs blobs={o?.thumbs} />
        <FileChips files={a?.files ?? o?.files} />
        <button className={"pending-text" + (expanded.has(id) ? " expanded" : "")} onClick={() => setExpanded((old) => new Set(old.has(id) ? [...old].filter((x) => x !== id) : [...old, id]))}>{message}</button>
        <div className="pending-meta"><span>{line.text}</span>{line.buttons}</div>
      </div>;
    })}
    {!all && ids.length > 3 && <button className="more-row" onClick={() => setAll(true)}>还有 {ids.length - 2} 条排队 展开</button>}
  </>;
}
