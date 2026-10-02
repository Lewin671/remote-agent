// The message queue in full (client-v2 队列完整页): every message the
// computer accepted and has not written yet, in its order, whoever sent
// it, each cancellable; and what became of a cancel.

import { useState } from "react";
import { InputStage, InputStatus, SendMode } from "../gen/remoteagent/v1/event_pb";
import { originLabel } from "../devices";
import { machineName, queuedIds, type Derived } from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { LocalThumbs } from "./Images";
import { promptIdOf } from "./Composer";

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

/** Pending input is shown at the end of the chat, on every client. */
export function PendingBubbles({ remote, session, d }: { remote: Remote; session: SessionView; d: Derived }) {
  const ids = queuedIds(d);
  const [all, setAll] = useState(() => remote.demoFlag("queue-all"));
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const shown = all || ids.length <= 3 ? ids : ids.slice(0, 2);
  const sid = session.id;
  const ctx = { me: remote.account.deviceId, devices: remote.devices, machine: machineName(remote.machines.get(session.machineId)) };
  const act = async (id: string, resume = false) => {
    if (!resume) { remote.cancels.set(id, { state: "canceling", error: "" }); remote.touch(); }
    try {
      await remote.command(sid, resume ? { case: "resumeInput", value: { sessionId: sid, requestId: id } } : { case: "cancelInput", value: { sessionId: sid, requestId: id } });
    } catch (e) {
      if (!resume) remote.cancels.set(id, { state: "failed", error: errorText(e) });
      remote.touch();
    }
  };
  return <>
    {shown.map((id, i) => {
      const a = d.accepted.get(id);
      const o = remote.outbox(sid).find((x) => x.sendId === id);
      const c = remote.cancels.get(id);
      const paused = d.paused.has(id);
      const held = d.inputs.get(id) === InputStatus.HELD_BY_CLI;
      const from = a?.origin && a.origin.deviceId !== remote.account.deviceId ? originLabel(a.origin, ctx) : "";
      const mode = a?.mode ?? o?.mode;
      const message = a?.text || o?.text || (id === promptIdOf(remote, sid, d) ? remote.prompts.get(sid) : undefined) || ((a?.imageIds.length ?? o?.imageIds?.length ?? 0) ? `${a?.imageIds.length ?? o?.imageIds?.length} 张图片` : "另一客户端的消息");
      const status = c?.state === "canceling" ? "正在取消…" : c?.state === "failed" ? "取消没成功，仍在排队" : paused ? "已暂停 · 中断前排着的" : mode === SendMode.QUEUE ? "排队 · 这一轮结束后发" : held ? "插入这一轮 · 等当前工具结束后并入" : "插入这一轮 · 等输入框就绪";
      return <div className="pending-bubble me" key={id}>
        <LocalThumbs blobs={o?.thumbs} />
        <button className={"pending-text" + (expanded.has(id) ? " expanded" : "")} onClick={() => setExpanded((old) => new Set(old.has(id) ? [...old].filter((x) => x !== id) : [...old, id]))}>{message}</button>
        <div className="pending-meta"><span>{status}{!paused && !held && !c?.state && ` · 第 ${i + 1} 条`}{from && ` · ${from}`}</span>
          {paused && <button onClick={() => void act(id, true)}>发送</button>}
          {c?.state === "failed" ? <button onClick={() => void act(id)}>重试</button> : c?.state !== "canceling" && <button onClick={() => void act(id)}>取消</button>}
        </div>
      </div>;
    })}
    {!all && ids.length > 3 && <button className="todo-more" onClick={() => setAll(true)}>还有 {ids.length - 2} 条排队 展开</button>}
  </>;
}
