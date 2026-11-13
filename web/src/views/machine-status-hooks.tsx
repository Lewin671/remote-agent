// The parts of the computer-status screens that are not a screen of their
// own: the polling hook, the digits that roll, and the transitions that move
// a card or a row to where it goes now (FLIP).

import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { MachineStatusController, type MachinePollState } from "../machine-status";
import type { MachineStatus } from "../gen/remoteagent/v1/local_pb";
import { canAnimate } from "../motion";
import type { Remote } from "../remote";
import { useRemote } from "../ui";

/** Whether the system asks for less motion: every transition here is then a plain change. */
export function useReducedMotion(): boolean {
  const query = () => (typeof matchMedia === "function" ? matchMedia("(prefers-reduced-motion: reduce)") : undefined);
  const [reduced, setReduced] = useState(() => !!query()?.matches);
  useEffect(() => {
    const q = query();
    if (!q) return;
    const on = () => setReduced(q.matches);
    on();
    q.addEventListener?.("change", on);
    return () => q.removeEventListener?.("change", on);
  }, []);
  return reduced;
}

/** Whether the page is in front: a hidden page asks for nothing and draws nothing. */
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === "undefined" || !document.hidden);
  useEffect(() => {
    const on = () => setVisible(!document.hidden);
    document.addEventListener("visibilitychange", on);
    on();
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

export interface StatusView {
  state: MachinePollState;
  last?: MachineStatus;
  failures: number;
  curveSinceMs: bigint;
  /** When (this device's clock) the last answer came. */
  receivedAt: number;
  retry: () => void;
}

/** One computer's status, asked every `intervalMs` while this page is in front,
 * the computer is online and its daemon has the capability. */
export function useMachineStatus(remote: Remote, id: string, intervalMs: number): StatusView {
  useRemote(remote);
  const m = remote.machines.get(id);
  const online = !!m && remote.onlineOf(m);
  const supported = !!m?.meta?.capabilities?.machineStatus;
  const visible = usePageVisible();
  const control = useRef(undefined as MachineStatusController | undefined);
  const [view, setView] = useState<Omit<StatusView, "retry">>({ state: "idle", failures: 0, curveSinceMs: 0n, receivedAt: 0 });
  useEffect(() => {
    let seen: MachineStatus | undefined;
    let received = 0;
    const c: MachineStatusController = new MachineStatusController(
      (signal) => remote.getMachineStatus(id, signal),
      () => {
        if (c.last !== seen) {
          seen = c.last;
          received = Date.now();
        }
        setView({ state: c.state, last: c.last, failures: c.failures, curveSinceMs: c.curveSinceMs, receivedAt: received });
      },
      intervalMs,
    );
    control.current = c;
    return () => {
      c.dispose();
      control.current = undefined;
    };
  }, [remote, id, intervalMs]);
  useEffect(() => {
    control.current?.setActive(visible, online, supported);
  }, [visible, online, supported, remote, id, intervalMs]);
  return { ...view, retry: () => control.current?.retry() };
}

/** A number whose changed digits roll: up when it grows, down when it
 * shrinks, 200ms, and only the digits that changed. Plain when the system
 * asks for less motion. Not announced as it changes: the words around it
 * say what it is. */
export function Roll({ text }: { text: string }) {
  const reduced = useReducedMotion();
  const [shown, setShown] = useState({ text, prev: text, tick: 0 });
  if (text !== shown.text) setShown({ text, prev: shown.text, tick: shown.tick + 1 });
  const now = shown.text;
  const old = shown.prev;
  const grew = (parseFloat(now.replace(/[^\d.-]/g, "")) || 0) >= (parseFloat(old.replace(/[^\d.-]/g, "")) || 0);
  const chars = [...now];
  const before = [...old];
  return (
    <span className="ms-roll">
      <span className="ms-sr">{text}</span>
      <span aria-hidden>
        {chars.map((c, i) => {
          const was = before[before.length - (chars.length - i)];
          if (reduced || shown.tick === 0 || was === undefined || was === c) return <span key={`s${i}`}>{c}</span>;
          return (
            <span key={`r${shown.tick}-${i}`} className={"ms-digit " + (grew ? "up" : "down")}>
              <span>{grew ? <><i>{was}</i><i>{c}</i></> : <><i>{c}</i><i>{was}</i></>}</span>
            </span>
          );
        })}
      </span>
    </span>
  );
}

/** Moves each child that has a `data-flip` to where it now is from where it
 * was (300ms), after every render: a row that changed places slides instead
 * of jumping. */
export function useFlipList(ref: RefObject<HTMLElement | null>, ms = 300) {
  const reduced = useReducedMotion();
  const tops = useRef(new Map<string, number>());
  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return;
    const next = new Map<string, number>();
    for (const el of root.querySelectorAll<HTMLElement>("[data-flip]")) {
      const id = el.dataset.flip!;
      const top = el.offsetTop;
      next.set(id, top);
      const was = tops.current.get(id);
      if (was !== undefined && was !== top && !reduced && canAnimate())
        el.animate([{ transform: `translateY(${was - top}px)` }, { transform: "none" }], { duration: ms, easing: "cubic-bezier(.2, .8, .2, 1)" });
    }
    tops.current = next;
  });
}

/** Moves an element from the box it had to the box it has, scaling as it goes. */
export function flipBox(el: HTMLElement, from: DOMRect, ms = 350) {
  if (!canAnimate()) return;
  const to = el.getBoundingClientRect();
  if (!to.width || !to.height) return;
  el.animate(
    [
      { transformOrigin: "0 0", transform: `translate(${from.left - to.left}px, ${from.top - to.top}px) scale(${from.width / to.width}, ${from.height / to.height})` },
      { transformOrigin: "0 0", transform: "none" },
    ],
    { duration: ms, easing: "cubic-bezier(.2, .8, .2, 1)" },
  );
}
