import { isSkeleton } from "../skeleton";
// Actions on what a session waits for. Each keeps the request slot of its
// last attempt, so "重试" repeats the same command and the daemon answers
// it once.

import { useEffect, useRef, useState } from "react";
import { SessionStatus, type ApprovalRequested } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { type Derived } from "../model";
import { stillUnknown } from "../attempt";
import {
  errorText,
  newSlot,
  type AnswerState,
  type CommandKind,
  type Remote,
  type RequestSlot,
} from "../remote";
import { approvalTruncated } from "../approval-display";
import { useRemote } from "../ui";

export function useDerived(remote: Remote, sid: string): Derived {
  useEffect(() => remote.retainHistory(sid), [remote, sid]);
  const historical = remote.historicalDerived(sid);
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    const update = () => {
      const session = remote.sessions.get(sid);
      const eligible = !document.hidden && remote.conn === "online" &&
        !!session && !session.endedMs && session.info?.status !== SessionStatus.EXITED &&
        remote.machines.get(session.machineId)?.online;
      if (!eligible) { clearInterval(timer); timer = undefined; }
      else if (timer === undefined) {
        timer = setInterval(() => void remote.refreshLiveSnapshot(sid), 5000);
        void remote.refreshLiveSnapshot(sid);
      }
    };
    const unsubscribe = remote.subscribe(update);
    document.addEventListener("visibilitychange", update);
    update();
    return () => {
      clearInterval(timer);
      unsubscribe();
      document.removeEventListener("visibilitychange", update);
    };
  }, [remote, sid]);
  return remote.currentDerived(sid, historical); // eslint-disable-line react-hooks/exhaustive-deps
}

/** Runs one kind of command, remembering the slot of the last attempt so
 * a retry of the same arguments reuses it. */
function useCommand(remote: Remote, sid: string) {
  const last = useRef<{ key: string; slot: RequestSlot }>(undefined);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  // The last failure left the outcome unknown: only a retry of the same
  // command may follow.
  const [unknown, setUnknown] = useState(false);
  const wasUnknown = useRef(false);
  const run = async (key: string, cmd: CommandKind): Promise<boolean> => {
    const retrying = last.current?.key === key;
    const slot = retrying ? last.current!.slot : newSlot();
    const before = retrying && wasUnknown.current;
    last.current = { key, slot };
    setBusy(key);
    setError("");
    setUnknown(false);
    try {
      await remote.command(sid, cmd, slot);
      wasUnknown.current = false;
      return true;
    } catch (e) {
      // Only an unknown outcome keeps the slot for the retry; a retry of
      // one that comes back expired stays unknown (see attempt.ts).
      const still = stillUnknown(e, before);
      wasUnknown.current = still;
      if (!still) last.current = { key, slot: newSlot() };
      setUnknown(still);
      setError(errorText(e));
      setBusy("");
      return false;
    }
  };
  const retry = () => last.current?.key;
  // Dismisses a definite failure, e.g. once the user changes the answer.
  const clearError = () => setError("");
  return { run, busy, error, unknown, retry, setBusy, clearError };
}

/** Allow or deny an approval. After success the buttons stay locked until
 * the resolution shows up in the events and the card goes away. */
export function useApproval(remote: Remote, sid: string, a: ApprovalRequested) {
  const c = useCommand(remote, sid);
  const resolve = (allow: boolean, message = "", always = false) =>
    isSkeleton(a) || (allow && approvalTruncated(a))
      ? Promise.resolve(false)
      : c.run(
          `${allow ? "allow" : "deny"}:${always ? "always" : "once"}:${message}`,
          {
            case: "resolveApproval",
            value: {
              sessionId: sid,
              approvalId: a.approvalId,
              allow,
              message,
              always,
            },
          },
        );
  const retry = () => {
    const key = c.retry();
    if (!key) return;
    const [kind, always, ...rest] = key.split(":");
    void resolve(kind === "allow", rest.join(":"), always === "always");
  };
  // unknown: the answer may have reached the computer; only it may be
  // retried, as the same request, never the opposite one.
  return { resolve, retry, busy: c.busy, error: c.error, unknown: c.unknown };
}

/** Answer question index of a question call: one 1-based option, or
 * several for a multi-select question. */
/** Answering a question call. Its state lives on the Remote by call ID,
 * so the docked card and the question page are one: moving between them
 * keeps the ticks, the question reached, and a pending answer's request
 * slot, which a retry must reuse. */
export function useAnswer(
  remote: Remote,
  sid: string,
  questionId: string,
  terminalOnly = false,
) {
  useRemote(remote);
  const st = remote.answerState(questionId);
  const set = (p: Partial<AnswerState>) => remote.patchAnswer(questionId, p);
  const answer = async (
    index: number,
    options: number[],
    multi = false,
  ): Promise<boolean> => {
    if (terminalOnly) return false;
    const key = `${index}:${options.join(",")}`;
    const cur = remote.answerState(questionId);
    const retrying = cur.last?.key === key;
    const slot = retrying ? cur.last!.slot : newSlot();
    const before = retrying && cur.unknown;
    set({ last: { key, slot }, busy: key, error: "", unknown: false });
    try {
      await remote.command(
        sid,
        {
          case: "answerQuestion",
          value: multi
            ? { sessionId: sid, questionId, questionIndex: index, options }
            : {
                sessionId: sid,
                questionId,
                option: options[0],
                questionIndex: index,
              },
        },
        slot,
      );
      return true;
    } catch (e) {
      // Only an unknown outcome keeps the slot for the retry; a retry of
      // one that comes back expired stays unknown (see attempt.ts).
      const still = stillUnknown(e, before);
      set({
        last: { key, slot: still ? slot : newSlot() },
        busy: "",
        unknown: still,
        error: errorText(e),
      });
      return false;
    }
  };
  // The question index and options of the pick in flight or last tried.
  const key = st.busy || st.last?.key || "";
  const [i, o] = key ? key.split(":") : ["-1", ""];
  const chosen = {
    index: Number(i),
    options: o ? o.split(",").map(Number) : [],
  };
  const setTicked = (f: (t: number[]) => number[]) =>
    set({ ticked: f(remote.answerState(questionId).ticked) });
  return {
    answer,
    busy: st.busy,
    error: st.error,
    unknown: st.unknown,
    clearError: () => set({ error: "" }),
    chosen,
    done: () => set({ busy: "" }),
    index: st.index,
    setIndex: (n: number) => set({ index: n }),
    ticked: st.ticked,
    setTicked,
  };
}

export function usePlan(remote: Remote, sid: string, terminalOnly = false) {
  const c = useCommand(remote, sid);
  const answer = (choice: PlanChoice) =>
    terminalOnly
      ? Promise.resolve(false)
      : c.run(String(choice), {
          case: "answerPlan",
          value: { sessionId: sid, choice },
        });
  const retry = () => {
    const key = c.retry();
    if (key) void answer(Number(key) as PlanChoice);
  };
  return { answer, retry, busy: c.busy, error: c.error };
}

/** The deadline of an approval in ms, 0 when it has none. */
export function deadlineOf(a: ApprovalRequested): number {
  return a.deadline
    ? Number(a.deadline.seconds) * 1000 + Math.floor(a.deadline.nanos / 1e6)
    : 0;
}
