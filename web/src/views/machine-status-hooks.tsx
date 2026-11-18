// The parts of the computer-status screens that are not a screen of their
// own: motion/visibility preferences, rolling digits, and FLIP list/box helpers.

import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { canAnimate } from "../motion";

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
