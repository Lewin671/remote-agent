// The message queue in full (client-v2 队列完整页): every message the
// computer accepted and has not written yet, in its order, whoever sent
// it, each cancellable; and what became of a cancel.

import { useEffect, useRef, useState } from "react";
import { InputStage, InputStatus } from "../gen/remoteagent/v1/event_pb";
import { originLabel } from "../devices";
import { machineName, queuedIds, queueReason, type Derived } from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { NavBar } from "../ui";
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

export function QueuePage({
  remote,
  session,
  d,
  title,
  onBack,
}: {
  remote: Remote;
  session: SessionView;
  d: Derived;
  title: string;
  onBack: () => void;
}) {
  const sid = session.id;
  const ids = queuedIds(d, promptIdOf(remote, sid, d));
  const texts = new Map(remote.outbox(sid).map((o) => [o.sendId, o.text]));
  const ctx = { me: remote.account.deviceId, devices: remote.devices, machine: machineName(remote.machines.get(session.machineId)), mine: new Set(remote.outbox(sid).map((o) => o.requestId)) };
  const [toast, setToast] = useState("");
  const shown = useRef<string[]>(ids);
  // A message that left the queue while this page shows it: say what
  // happened to it, and whether the cancel asked for here was in time.
  useEffect(() => {
    const gone = shown.current.filter((id) => !ids.includes(id));
    for (const id of gone) {
      const c = remote.cancels.get(id);
      const text = cancelOutcome(d.inputs.get(id), !!c);
      if (text) setToast(text);
      remote.cancels.delete(id);
    }
    shown.current = ids;
  }, [ids.join(","), remote, d]); // eslint-disable-line react-hooks/exhaustive-deps
  const cancel = async (id: string) => {
    remote.cancels.set(id, { state: "canceling", error: "" });
    remote.touch();
    try {
      await remote.command(sid, { case: "cancelInput", value: { sessionId: sid, requestId: id } });
    } catch (e) {
      // Still queued: the cancel did not take. Already written: the page
      // says so when the message leaves the queue.
      if (d.inputs.get(id) === InputStatus.QUEUED) remote.cancels.set(id, { state: "failed", error: errorText(e) });
      remote.touch();
    }
  };
  const reason = queueReason(session, d);
  const online = remote.conn === "online";
  return (
    <div className="page">
      <NavBar
        onBack={onBack}
        title={<span className="tname">消息队列</span>}
        sub={title}
      />
      <div className="body pad">
        <div className="hint">这是电脑上的同一条队列，所有客户端看到同样的已接受消息，按顺序发送。取消只对尚未送入终端的消息生效。</div>
        {ids.length === 0 && <div className="hint">队列里没有消息了。</div>}
        {ids.map((id, i) => {
          const c = remote.cancels.get(id);
          const mine = texts.has(id);
          const acc = d.accepted.get(id);
          const text = texts.get(id) ?? acc?.text;
          const from = mine ? "本客户端" : originLabel(acc?.origin, ctx) || "另一客户端";
          const images = acc?.imageIds.length ?? 0;
          const stage = stageText(d.stages.get(id));
          const state =
            c?.state === "canceling"
              ? "正在取消…"
              : stage || (i === 0 ? (reason ? reason.replace(/^本轮结束后发送$/, "等待本轮结束").replace(/^就绪后发送$/, "等待输入框就绪") : "等待发送") : "等待发送");
          return (
            <div key={id} className={"queue-entry" + (c?.state === "canceling" ? " canceling" : "")}>
              <div className="qh">
                <b>
                  {i + 1} · {state}
                </b>
                <span className="qsrc">
                  {from}
                  {images > 0 && ` · ${images} 张图`}
                </span>
                <button className="cancel" disabled={c?.state === "canceling" || !online || !!stage} onClick={() => void cancel(id)}>
                  {c?.state === "canceling" ? "◌" : "取消"}
                </button>
              </div>
              <LocalThumbs blobs={remote.outbox(sid).find((o) => o.sendId === id)?.thumbs} />
              <p className={text !== undefined ? "" : "muted"}>{text ?? "另一客户端发送的消息，内容会在它发出后出现在聊天里"}</p>
              {c?.state === "failed" && (
                <small className="red">
                  取消未成功，消息仍在队列中：{c.error}
                  <button className="link" onClick={() => void cancel(id)} disabled={!online}>
                    重试
                  </button>
                </small>
              )}
            </div>
          );
        })}
      </div>
      {toast && (
        <button className="toast plain" onClick={() => setToast("")}>
          {toast}
        </button>
      )}
    </div>
  );
}
