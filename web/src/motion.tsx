// Motion (design-system §4 动效): what comes, goes, grows or changes says
// so with one short transition. Durations and the curve are the CSS
// variables in styles.css (--m-*, --ease-*); everything here reads them, so
// the stylesheet is the one place that says how long and how, and the
// reduced-motion block there turns displacement and size changes off.
//
// Entering is mostly CSS (a class with an animation, played once when the
// element is inserted). What CSS cannot do is done here with the Web
// Animations API:
//   - an element that leaves is already gone from React's tree, so a ghost
//     (an inert copy left in its place) plays the exit: useExit, useGrow;
//   - a height that follows content: useGrow, useHeightFlip, tweenHeight;
//   - content that changes in place crossfades with its old self: Swap,
//     useMarkSwap;
//   - the knob of a segmented control slides: useKnob;
//   - only what is new may play its entrance: useFresh.
// Nothing runs where the API is missing (the test DOM), and every
// animation can be interrupted: the next one starts from what is on screen.

import { useLayoutEffect, useRef, type ReactNode, type RefObject } from "react";

export type Tier = "fast" | "base" | "slow";

const FALLBACK = { fast: 120, base: 200, slow: 280 };

function cssValue(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function cssMs(name: string, fallback: number): number {
  const v = parseFloat(cssValue(name));
  return Number.isFinite(v) ? v : fallback;
}

/** Whether animations can run: not under the tests, whose DOM implements
 * the API without ever playing it, so that a ghost would never go. */
export function canAnimate(): boolean {
  return import.meta.env.MODE !== "test" && typeof Element !== "undefined" && typeof Element.prototype.animate === "function";
}

/** How long a tier takes to come (the reduced-motion block shortens them). */
export const enterMs = (tier: Tier) => cssMs(`--m-${tier}`, FALLBACK[tier]);
/** How long it takes to go: 0.8 of the tier, ease-in. */
export const exitMs = (tier: Tier) => cssMs(`--m-${tier}-out`, FALLBACK[tier] * 0.8);
/** How long a height takes to change; 0 when the system asks for less motion. */
const sizeMs = () => cssMs("--m-size", FALLBACK.base);
/** The distance a thing rises as it comes; 0 when the system asks for less motion. */
const riseOf = (big: boolean) => cssMs(big ? "--m-dy-lg" : "--m-dy", big ? 12 : 8);
const easeOut = () => cssValue("--ease-out") || "cubic-bezier(.2, .8, .2, 1)";
const easeIn = () => cssValue("--ease-in") || "ease-in";

const SIZE = "mo-size";

/** The animation now changing this element's height, if any. */
function sizing(el: Element): Animation | undefined {
  return el.getAnimations().find((a) => a.id === SIZE);
}

function rowGap(parent: Element | null): number {
  if (!parent) return 0;
  const cs = getComputedStyle(parent);
  if (!/flex|grid/.test(cs.display)) return 0;
  const v = parseFloat(cs.rowGap);
  return Number.isFinite(v) ? v : 0;
}

/** The look of a box with nothing in it, and of one at its natural size. */
function boxFrames(el: HTMLElement, height: number) {
  const cs = getComputedStyle(el);
  const gap = rowGap(el.parentElement);
  const shut = { height: "0px", minHeight: "0px", paddingTop: "0px", paddingBottom: "0px", marginTop: "0px", marginBottom: `${-gap}px` };
  const open = {
    height: `${height}px`,
    minHeight: cs.minHeight === "auto" ? "0px" : cs.minHeight,
    paddingTop: cs.paddingTop,
    paddingBottom: cs.paddingBottom,
    marginTop: cs.marginTop,
    marginBottom: cs.marginBottom,
  };
  return { shut, open };
}

/** Runs frames on a box that must clip while it moves; what its overflow was
 * comes back when the last of its size animations is over. */
function play(el: HTMLElement, frames: Keyframe[], ms: number, easing: string, id = SIZE): Animation {
  if (el.dataset.moKeep === undefined) el.dataset.moKeep = el.style.overflow;
  el.style.overflow = "hidden";
  const a = el.animate(frames, { duration: ms, easing, id });
  const done = () => {
    if (el.getAnimations().some((x) => x.id === SIZE && x.playState === "running")) return;
    el.style.overflow = el.dataset.moKeep ?? "";
    delete el.dataset.moKeep;
  };
  a.addEventListener("finish", done);
  a.addEventListener("cancel", done);
  return a;
}

/** Eases an element's height from one measured value to another. */
export function easeBetween(el: HTMLElement, from: number, to: number) {
  if (canAnimate() && sizeMs() > 0 && Math.abs(from - to) >= 1) play(el, [{ height: `${from}px` }, { height: `${to}px` }], sizeMs(), easeOut());
}

/** The height an element has on screen now, in the middle of an ease or not. */
export function heightNow(el: HTMLElement): number {
  return el.getBoundingClientRect().height;
}

/** Whether an ease of the element's height is running. */
export function easing(el: Element): boolean {
  return !!sizing(el);
}

/** Ends the running ease of an element's height. */
export function stopEasing(el: Element) {
  sizing(el)?.cancel();
}

/** An inert copy of an element, for it to leave by or to fade out as. */
function copyOf(el: HTMLElement): HTMLElement {
  const g = el.cloneNode(true) as HTMLElement;
  g.removeAttribute("id");
  for (const x of g.querySelectorAll("[id]")) x.removeAttribute("id");
  g.setAttribute("aria-hidden", "true");
  g.inert = true;
  g.classList.add("mo-ghost");
  return g;
}

/** Removes the ghost once everything animating on it has finished. */
function removeWhenDone(g: HTMLElement) {
  const run = () => {
    const all = g.getAnimations({ subtree: true });
    const gone = () => g.remove();
    if (!all.length) return gone();
    void Promise.allSettled(all.map((a) => a.finished)).then(gone);
  };
  run();
  setTimeout(() => g.remove(), 1200);
}

type ExitOptions = {
  tier?: Tier;
  /** Collapse the height as it goes (the elements around close up). */
  collapse?: boolean;
  /** A class the stylesheet animates the ghost and its parts by. */
  leave?: string;
  /** Asked as it unmounts: true when something takes its place at once, and
   * it should go without a sound. */
  skip?: () => boolean;
};

/** Plays an element's exit on a ghost left in its place. Held until the
 * end of the task: a mount that React undoes at once (StrictMode) cancels it. */
function leaving(el: HTMLElement, o: ExitOptions, alive: () => boolean) {
  const g = copyOf(el);
  const rect = el.getBoundingClientRect();
  const frames = o.collapse && sizeMs() > 0 ? boxFrames(el, rect.height) : undefined;
  g.style.display = "none";
  el.after(g);
  queueMicrotask(() => {
    if (alive()) return g.remove();
    g.style.display = "";
    const ms = exitMs(o.tier ?? "base");
    if (o.leave) g.classList.add(o.leave);
    else if (frames) {
      play(g, [{ ...frames.open, opacity: 1 }, { ...frames.shut, opacity: 0 }], ms, easeIn(), SIZE);
    } else g.animate([{ opacity: 1 }, { opacity: 0 }], { duration: ms, easing: easeIn() });
    removeWhenDone(g);
  });
}

/** An element that fades away (or, with `leave`, is animated away by the
 * stylesheet) when it unmounts, in the place it was. The ghost cannot be
 * clicked. */
export function useExit(ref: RefObject<HTMLElement | null>, o: ExitOptions = {}) {
  const opts = useRef(o);
  opts.current = o;
  const alive = useRef(false);
  useLayoutEffect(() => {
    const el = ref.current;
    alive.current = true;
    return () => {
      alive.current = false;
      if (el && el.isConnected && canAnimate() && !opts.current.skip?.()) leaving(el, opts.current, () => alive.current);
    };
  }, [ref]);
}

/** A block that opens to its height as it mounts and closes up as it
 * unmounts: the dock, the tray of pictures. What is around it moves with
 * it, frame by frame. With `rise`, it also comes up from below. */
export function useGrow(ref: RefObject<HTMLElement | null>, { rise = false }: { rise?: boolean } = {}) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !canAnimate()) return;
    const ms = sizeMs();
    // What it measures is its own size, not a motion still on it.
    sizing(el)?.cancel();
    const { shut, open } = boxFrames(el, el.getBoundingClientRect().height);
    const dy = rise ? riseOf(true) : 0;
    const from = { ...shut, opacity: 0, translate: `0 ${dy}px` };
    const to = { ...open, opacity: 1, translate: "0 0" };
    if (ms === 0) el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: enterMs("fast"), easing: easeOut() });
    else play(el, [from, to], ms, easeOut());
  }, [ref, rise]);
  useExit(ref, { collapse: true });
}

/** A box whose content changes size on its own (a dock opening or folding,
 * a card answered and shrinking to its line, the next item taking its place):
 * its height eases to the new one while the old content, copied just
 * before, fades out above the new. Resizing the window or the keyboard
 * (`--content-max` moving) is not this and does not ease. */
export function useHeightFlip(ref: RefObject<HTMLElement | null>, { fade = true }: { fade?: boolean } = {}) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !canAnimate() || typeof ResizeObserver === "undefined") return;
    // Unknown while something else (its entrance) is moving it.
    let height: number | undefined = sizing(el) ? undefined : el.getBoundingClientRect().height;
    void sizing(el)?.finished.then(() => (height ??= el.getBoundingClientRect().height), () => undefined);
    let room = el.style.getPropertyValue("--content-max");
    let view = `${innerWidth}x${innerHeight}`;
    let copy: HTMLElement | undefined;
    let at = -Infinity;
    const snap = (every = 0) => {
      if (!fade) return;
      const t = performance.now();
      if (t - at < every || sizing(el)) return;
      const card = el.firstElementChild as HTMLElement | null;
      copy = card ? copyOf(card) : undefined;
      at = t;
    };
    snap();
    // A person's touch is what usually changes it: copy what they touched.
    const touched = () => snap();
    for (const type of ["pointerdown", "keydown", "click"]) el.addEventListener(type, touched, true);
    const ro = new ResizeObserver(() => {
      if (sizing(el)) return;
      const to = el.getBoundingClientRect().height;
      const now = el.style.getPropertyValue("--content-max");
      const here = `${innerWidth}x${innerHeight}`;
      const moved = now !== room || here !== view;
      const from = height;
      height = to;
      room = now;
      view = here;
      if (from === undefined || moved || Math.abs(from - to) < 1) return snap();
      // Without size motion the new content still fades in.
      if (sizeMs() === 0) {
        if (fade) el.firstElementChild?.animate([{ opacity: 0 }, { opacity: 1 }], { duration: enterMs("base"), easing: easeOut() });
        return snap();
      }
      play(el, [{ height: `${from}px` }, { height: `${to}px` }], sizeMs(), easeOut());
      if (fade) el.firstElementChild?.animate([{ opacity: 0 }, { opacity: 1 }], { duration: enterMs("base"), easing: easeOut() });
      if (copy) {
        const old = copy;
        old.classList.add("mo-swap-out");
        el.append(old);
        old.animate([{ opacity: 1 }, { opacity: 0 }], { duration: exitMs("base"), easing: easeIn() });
        removeWhenDone(old);
        copy = undefined;
      }
    });
    ro.observe(el);
    // The copy for a change nobody touched is the last one seen, renewed
    // twice a second at most (the card may be long).
    const renew = setInterval(() => snap(500), 500);
    return () => {
      ro.disconnect();
      clearInterval(renew);
      for (const type of ["pointerdown", "keydown", "click"]) el.removeEventListener(type, touched, true);
    };
  }, [ref]);
}

/** Content that changes in place: the old one stays under the new for the
 * length of a fade, so a word or a glyph becomes the next without a cut.
 * `hold` keeps the old opaque while the new fades in over it (two buttons
 * of one color, which must not dip in between). */
export function Swap({ k, children, className = "", hold = false }: { k: string; children: ReactNode; className?: string; hold?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const shown = useRef<string>(undefined);
  const leaving = useRef<HTMLElement>(undefined);
  // The old content is gone from the page once this render is committed, so
  // the copy it leaves by is taken now, and only when the key is to change.
  if (shown.current !== undefined && shown.current !== k && canAnimate()) {
    const old = box.current?.querySelector<HTMLElement>(":scope > .mo-swap-cur");
    leaving.current = old ? copyOf(old) : undefined;
  }
  useLayoutEffect(() => {
    const el = box.current;
    const cur = el?.querySelector<HTMLElement>(":scope > .mo-swap-cur");
    const old = leaving.current;
    leaving.current = undefined;
    const changed = shown.current !== undefined && shown.current !== k;
    shown.current = k;
    if (!el || !cur || !canAnimate() || !changed || !old) return;
    old.classList.add("mo-swap-out");
    el.append(old);
    const ms = enterMs("fast");
    if (!hold) old.animate([{ opacity: 1 }, { opacity: 0 }], { duration: exitMs("fast"), easing: easeIn(), fill: "forwards" });
    cur.animate([{ opacity: 0 }, { opacity: 1 }], { duration: ms, easing: easeOut() });
    void Promise.allSettled([...cur.getAnimations()].map((a) => a.finished)).then(() => old.remove());
    setTimeout(() => old.remove(), 1000);
  });
  return (
    <div ref={box} className={"mo-swap " + className}>
      <div key={k} className="mo-swap-cur">
        {children}
      </div>
    </div>
  );
}

/** A state graphic that changes shape (spinner, dot, ring) fades into the
 * next one: the old shape stays where it was, fading out under it. */
export function useMarkSwap(ref: RefObject<HTMLElement | null>, key: string) {
  const last = useRef<string>(undefined);
  const leaving = useRef<{ node: HTMLElement; width: number; height: number }>(undefined);
  // The old shape is gone from the page once this render is committed, so
  // its copy and its size are taken now, and only when the shape is to change.
  if (last.current !== undefined && last.current !== key && canAnimate()) {
    const el = ref.current;
    leaving.current = el?.offsetParent ? { node: copyOf(el), width: el.offsetWidth, height: el.offsetHeight } : undefined;
  }
  useLayoutEffect(() => {
    const el = ref.current;
    const prev = leaving.current;
    leaving.current = undefined;
    const changed = last.current !== undefined && last.current !== key;
    last.current = key;
    if (!el || !canAnimate() || !changed || !prev || !el.offsetParent) return;
    // The old shape is centered where the new one now is, whatever moved
    // around them in the same render.
    const old = prev.node;
    Object.assign(old.style, {
      position: "absolute",
      left: `${el.offsetLeft + (el.offsetWidth - prev.width) / 2}px`,
      top: `${el.offsetTop + (el.offsetHeight - prev.height) / 2}px`,
      width: `${prev.width}px`,
      height: `${prev.height}px`,
      margin: "0",
    });
    el.after(old);
    old.animate([{ opacity: 1 }, { opacity: 0 }], { duration: enterMs("fast"), easing: easeOut(), fill: "forwards" });
    el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: enterMs("fast"), easing: easeOut() });
    removeWhenDone(old);
  });
}

/** The knob of a segmented control: one element that slides to whichever
 * button is picked. The control holds `<i className="mo-knob" />` first and
 * buttons marked `.on`, `aria-pressed` or `aria-selected`; `value` says
 * when the pick changed. Placed without sliding when first drawn and when
 * the control is resized. */
export function useKnob(ref: RefObject<HTMLElement | null>, value: unknown) {
  const placed = useRef(false);
  const place = useRef<(slide: boolean) => void>(undefined);
  useLayoutEffect(() => {
    const root = ref.current;
    const knob = root?.querySelector<HTMLElement>(":scope > .mo-knob");
    if (!root || !knob) return;
    let settle = 0;
    place.current = (slide) => {
      const on = root.querySelector<HTMLElement>(":scope > button.on, :scope > button[aria-pressed='true'], :scope > button[aria-selected='true']");
      if (!on) {
        knob.style.opacity = "0";
        return;
      }
      // Everything is read first and written after, so that the page lays
      // out once for it, not once between each pair.
      const width = on.offsetWidth, height = on.offsetHeight, x = on.offsetLeft, y = on.offsetTop;
      const still = !slide || sizeMs() === 0;
      cancelAnimationFrame(settle);
      if (still) knob.style.transition = "none";
      knob.style.opacity = "1";
      knob.style.width = `${width}px`;
      knob.style.height = `${height}px`;
      knob.style.transform = `translate(${x}px, ${y}px)`;
      // The transition comes back once the placement has been drawn without
      // it: the change is only taken into account at the next frame.
      if (still) settle = requestAnimationFrame(() => (settle = requestAnimationFrame(() => (knob.style.transition = ""))));
    };
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => place.current?.(false));
    ro.observe(root);
    return () => {
      cancelAnimationFrame(settle);
      ro.disconnect();
    };
  }, [ref]);
  useLayoutEffect(() => {
    place.current?.(placed.current);
    placed.current = true;
  }, [value]);
}

/** Which keys are new since the last render, for the entrance of what has
 * only now come: a list that is just opened, scrolled further back or drawn
 * again plays nothing, since all it holds was known or arrives behind the
 * keys already there. Only keys that follow every known key count (new
 * rows at the end, not older ones loaded above), and a burst of more than
 * `bulk` is a load, not an arrival. A key stays new for a moment, so the
 * class an entrance hangs on is not taken away mid-play. `armed` is false
 * until what is shown is complete. */
export function useFresh(
  keys: readonly string[],
  armed: boolean,
  {
    bulk = 8,
    anywhere = false,
    replaces,
  }: {
    /** More new keys than this at once are a load, not an arrival. */
    bulk?: number;
    /** New keys count wherever they stand, not only behind the known ones. */
    anywhere?: boolean;
    /** Whether a new key only takes the place of one that has gone (a
     * running tool that finished): its entrance would blink, so it has none. */
    replaces?: (key: string, gone: string[]) => boolean;
  } = {},
): (key: string) => boolean {
  const known = useRef<Set<string> | undefined>(undefined);
  const born = useRef(new Map<string, number>());
  const now = typeof performance === "undefined" ? 0 : performance.now();
  const prior = known.current;
  if (prior && armed) {
    let lastKnown = -1;
    keys.forEach((k, i) => {
      if (prior.has(k)) lastKnown = i;
    });
    const arrived = keys.filter((k, i) => !prior.has(k) && (anywhere || i > lastKnown));
    const added = keys.filter((k) => !prior.has(k));
    if (arrived.length > 0 && added.length <= bulk) {
      const have = new Set(keys);
      const gone = [...prior].filter((k) => !have.has(k));
      for (const k of arrived) if (!born.current.has(k) && !replaces?.(k, gone)) born.current.set(k, now);
    }
  }
  useLayoutEffect(() => {
    known.current = new Set(keys);
    for (const [k, t] of born.current) if (performance.now() - t > 2000) born.current.delete(k);
  });
  return (key) => {
    const t = born.current.get(key);
    return t !== undefined && now - t < 600;
  };
}

/** A line that has done its work (the status under a message that has gone
 * through) closes up: it fades and gives its height back, then is hidden.
 * With `on` false it is left as it is. */
export function useSettle(ref: RefObject<HTMLElement | null>, on: boolean) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !on || !canAnimate()) return;
    const { shut, open } = boxFrames(el, el.getBoundingClientRect().height);
    const hide = () => {
      el.style.display = "none";
    };
    if (sizeMs() === 0) {
      const a = el.animate([{ opacity: 1 }, { opacity: 0 }], { duration: exitMs("fast"), easing: easeIn(), fill: "forwards" });
      a.addEventListener("finish", hide);
      return;
    }
    const a = el.animate([{ ...open, opacity: 1 }, { ...shut, marginBottom: "0px", opacity: 0 }], {
      duration: sizeMs(),
      easing: easeIn(),
      fill: "forwards",
      id: SIZE,
    });
    el.style.overflow = "hidden";
    a.addEventListener("finish", hide);
  }, [ref, on]);
}
