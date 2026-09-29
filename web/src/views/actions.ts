// Actions on what a session waits for. Each keeps the request slot of its
// last attempt, so "重试" repeats the same command and the daemon answers
// it once.

import { useMemo, useRef, useState } from "react";
import type { ApprovalRequested } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { derive, type Derived } from "../model";
import { errorText, newSlot, uncertain, type CommandKind, type Remote, type RequestSlot } from "../remote";

export function useDerived(remote: Remote, sid: string): Derived {
  const events = remote.events(sid);
  // The array grows in place; its length tells a change.
  return useMemo(() => derive(events), [events, events.length]); // eslint-disable-line react-hooks/exhaustive-deps
}

/** Runs one kind of command, remembering the slot of the last attempt so
 * a retry of the same arguments reuses it. */
function useCommand(remote: Remote, sid: string) {
  const last = useRef<{ key: string; slot: RequestSlot }>(undefined);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const run = async (key: string, cmd: CommandKind): Promise<boolean> => {
    const slot = last.current?.key === key ? last.current.slot : newSlot();
    last.current = { key, slot };
    setBusy(key);
    setError("");
    try {
      await remote.command(sid, cmd, slot);
      return true;
    } catch (e) {
      // Only an uncertain outcome keeps the slot for the retry.
      if (!uncertain(e)) last.current = { key, slot: newSlot() };
      setError(errorText(e));
      setBusy("");
      return false;
    }
  };
  const retry = () => last.current?.key;
  return { run, busy, error, retry, setBusy };
}

/** Allow or deny an approval. After success the buttons stay locked until
 * the resolution shows up in the events and the card goes away. */
export function useApproval(remote: Remote, sid: string, a: ApprovalRequested) {
  const c = useCommand(remote, sid);
  const resolve = (allow: boolean, message = "") =>
    c.run(`${allow ? "allow" : "deny"}:${message}`, { case: "resolveApproval", value: { sessionId: sid, approvalId: a.approvalId, allow, message } });
  const retry = () => {
    const key = c.retry();
    if (!key) return;
    const [kind, ...rest] = key.split(":");
    void resolve(kind === "allow", rest.join(":"));
  };
  return { resolve, retry, busy: c.busy, error: c.error };
}

/** Answer question index of a question call: one 1-based option, or
 * several for a multi-select question. */
export function useAnswer(remote: Remote, sid: string, questionId: string) {
  const c = useCommand(remote, sid);
  const answer = (index: number, options: number[], multi = false) =>
    c.run(`${index}:${options.join(",")}`, {
      case: "answerQuestion",
      value: multi ? { sessionId: sid, questionId, questionIndex: index, options } : { sessionId: sid, questionId, option: options[0], questionIndex: index },
    });
  // The question index and options of the pick in flight or last tried.
  const key = c.busy || c.retry() || "";
  const [i, o] = key ? key.split(":") : ["-1", ""];
  const chosen = { index: Number(i), options: o ? o.split(",").map(Number) : [] };
  return { answer, busy: c.busy, error: c.error, chosen, done: () => c.setBusy("") };
}

export function usePlan(remote: Remote, sid: string) {
  const c = useCommand(remote, sid);
  const answer = (choice: PlanChoice) => c.run(String(choice), { case: "answerPlan", value: { sessionId: sid, choice } });
  const retry = () => {
    const key = c.retry();
    if (key) void answer(Number(key) as PlanChoice);
  };
  return { answer, retry, busy: c.busy, error: c.error };
}

/** The deadline of an approval in ms, 0 when it has none. */
export function deadlineOf(a: ApprovalRequested): number {
  return a.deadline ? Number(a.deadline.seconds) * 1000 + Math.floor(a.deadline.nanos / 1e6) : 0;
}
