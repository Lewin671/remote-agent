import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { NEAR_END } from "./scroll-follow";
import { Virtuoso, type StateSnapshot, type VirtuosoHandle } from "react-virtuoso";
import type { Remote } from "../remote";

type Position = { state: StateSnapshot; count: number; width: number; first: number; anchor?: { key: string; index: number; offset: number } };
type Entry<T> = { at: "header" } | { at: "footer" } | { at: "row"; item: T };
const positions = new WeakMap<Remote, Map<string, Position>>();
type Props<T> = {
  items: T[];
  render: (item: T) => ReactNode;
  scroller: RefObject<HTMLDivElement | null>;
  remote: Remote;
  sid: string;
  follow: () => boolean;
  header: ReactNode;
  footer: ReactNode;
  /** All the history is here. */
  loaded: boolean;
  /** What is here is what the list ends with (the latest page, or the whole
   * copy kept in this browser): it can be drawn, and older records that come
   * in above are placed without moving what is on screen. */
  ready: boolean;
  focusKey?: string;
  /** A class for a row that has only now arrived (its entrance). */
  rowClass?: (item: T) => string | undefined;
  /** The history is known to be long before all of it is here (the session's
   * event count), so the virtual list is used from the first draw instead of
   * replacing the plain one when the count passes 100. */
  long?: boolean;
};

// A first index well above zero, to have room to count down from as older
// records join above.
const FIRST = 1_000_000;
// Frames a drawn and measured list at the end must keep its height before it is shown.
const STILL_FRAMES = 1;

/** Keep complete event state, but mount only rows near the viewport. */
export function HistoryRows<T extends { key: string }>(props: Props<T>) {
  const { items, render, scroller, remote, sid, header, footer, loaded, rowClass, long } = props;
  // Once the virtual list is in use it stays: swapping it for the plain one
  // and back would redraw the whole chat.
  const windowed = useRef(false);
  if (items.length > 100 || long) windowed.current = true;
  const [parent, setParent] = useState<HTMLElement>();
  const [initial] = useState(() => ({ top: remote.scroll.get(sid), long: items.length > 100, loading: !loaded }));
  const shortRestored = useRef(false);
  const focused = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    if (!loaded || !props.focusKey || items.length > 100 || focused.current === props.focusKey) return;
    const index = items.findIndex(it => it.key === props.focusKey);
    const row = scroller.current?.querySelector<HTMLElement>(`[data-history-index="${index + 1}"]`);
    if (row) { row.scrollIntoView?.({ block: "start" }); focused.current = props.focusKey; }
  }, [loaded, props.focusKey, items, scroller, parent]);
  useLayoutEffect(() => {
    if (!props.focusKey && !shortRestored.current && loaded && items.length <= 100 && initial.loading && initial.top && !initial.top.stick && parent) {
      parent.scrollTop = initial.top.top;
      shortRestored.current = true;
    }
  }, [loaded, items.length, initial, parent, props.focusKey]);
  useEffect(() => { setParent(scroller.current ?? undefined); }, [scroller]);
  // A reclaimed history arrives in pages. Do not restore a position into an
  // incomplete prefix or overwrite its measurements with that prefix.
  if (initial.loading && initial.top && !initial.top.stick && !loaded) return <div className="sep">正在恢复阅读位置…</div>;
  if (!windowed.current) return <>{header}{items.map((it, index) => <div key={it.key} className={"history-row" + (rowClass?.(it) ? " " + rowClass(it) : "")} data-history-index={index + 1}>{render(it)}</div>)}{footer}</>;
  if (!parent) return null;
  // Initial positioning must use the page we intend to show, not an empty
  // list or an incomplete cached prefix. Virtuoso initializes only once.
  if (!props.ready) return <HistoryPlaceholder parent={parent} />;
  return <WindowedRows {...props} parent={parent} initialTop={initial.long || initial.loading ? initial.top : undefined} />;
}

function HistoryPlaceholder({ parent }: { parent: HTMLElement }) {
  useLayoutEffect(() => {
    parent.classList.add("mo-hold");
    return () => parent.classList.remove("mo-hold");
  }, [parent]);
  return null;
}

function WindowedRows<T extends { key: string }>({ items, render, parent, remote, sid, follow, header, footer, initialTop, focusKey, loaded, ready: isReady, rowClass }: Props<T> & {
  parent: HTMLElement;
  initialTop?: { top: number; stick: boolean };
}) {
  const list = useRef<VirtuosoHandle>(null);
  const focused = useRef<string | undefined>(undefined);
  // The list is drawn with estimated row heights and corrects them as the
  // rows are measured: that moves it. It is not shown until it holds rows,
  // what it ends with is here (the latest page, or the copy kept in this
  // browser: older records that come in above are placed without moving what
  // is on screen), it stands at the end if it follows it, and its height has
  // been the same for a frame once every row on the page is measured. A
  // placeholder stands in meanwhile. Plain
  // lists are placed in the frame they are drawn in and need none of this.
  const ready = useRef(isReady);
  ready.current = isReady;
  useLayoutEffect(() => {
    parent.classList.add("mo-hold");
    const follows = !focusKey && (initialTop ? initialTop.stick : parent.scrollTop + parent.clientHeight >= parent.scrollHeight - NEAR_END);
    const t0 = performance.now();
    let still = 0;
    let last = -1;
    let id = requestAnimationFrame(function tick() {
      // The list stops short of the chat's own bottom padding: take that last step.
      if (follows && parent.scrollHeight - parent.scrollTop - parent.clientHeight < 40) parent.scrollTop = parent.scrollHeight;
      const sh = parent.scrollHeight;
      const rows = parent.querySelectorAll<HTMLElement>("[data-item-index]");
      // Rows are on the page, and either the list is taller than it or its last
      // row (the footer) is there: a short chat has nothing more to wait for.
      const drawn = rows.length > 0 && (sh > parent.clientHeight || !!parent.querySelector(`[data-item-index="${firstNow.current + count.current - 1}"]`));
      const there = !follows || sh - parent.scrollTop - parent.clientHeight < 4;
      // Every row on the page is the size the list holds for it: what is
      // left to correct is not on the page.
      const measured = drawn && [...rows].every((el) => Math.abs(Number(el.dataset.knownSize) - el.offsetHeight) < 1);
      still = sh === last && there && measured ? still + 1 : 0;
      last = sh;
      if ((still >= STILL_FRAMES && ready.current) || performance.now() - t0 > 6000) return parent.classList.remove("mo-hold");
      id = requestAnimationFrame(tick);
    });
    return () => {
      cancelAnimationFrame(id);
      parent.classList.remove("mo-hold");
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    if (!loaded || !focusKey || focused.current === focusKey) return;
    const index = items.findIndex(it => it.key === focusKey);
    if (index >= 0) { list.current?.scrollToIndex({ index: index + 1, align: "start" }); focused.current = focusKey; }
  }, [focusKey, items, loaded]);
  const currentItems = useRef(items);
  currentItems.current = items;
  // Older records join above as the history comes in. The list is told how
  // many, as a lower index for its first row, and keeps what is on screen
  // where it is. Its indices from here on count from that first one.
  const head = useRef({ key: items[0]?.key, first: FIRST });
  if (items[0]?.key !== head.current.key) {
    const joined = head.current.key === undefined ? 0 : items.findIndex((it) => it.key === head.current.key);
    head.current = { key: items[0]?.key, first: joined > 0 ? head.current.first - joined : head.current.first };
  }
  const first = head.current.first;
  const firstAtMount = useRef(first);
  const count = useRef(items.length + 2);
  count.current = items.length + 2;
  const [saved] = useState(() => positions.get(remote)?.get(sid));
  const [top] = useState(() => initialTop ?? { top: parent.scrollTop, stick: follow() });
  // Crossing the small-list threshold must preserve the row being read,
  // including variable heights; a pixel offset based on estimated sizes cannot.
  const [anchor] = useState(() => {
    if (top.stick) return undefined;
    if (initialTop) {
      if (!saved || (saved.count === items.length + 2 && saved.width === parent.clientWidth) || !saved.anchor) return undefined;
      const index = items.findIndex((it) => it.key === saved.anchor!.key);
      return index >= 0 ? { index: index + 1, align: "start" as const, offset: saved.anchor.offset } : undefined;
    }
    const edge = parent.getBoundingClientRect().top;
    const row = [...parent.querySelectorAll<HTMLElement>("[data-history-index]")].find((el) => el.getBoundingClientRect().bottom > edge);
    return row ? { index: Number(row.dataset.historyIndex), align: "start" as const, offset: edge - row.getBoundingClientRect().top } : undefined;
  });
  const firstNow = useRef(first);
  firstNow.current = first;
  const saveNow = useCallback(() => list.current?.getState((state) => {
    if (!state.ranges.length) return;
    const map = positions.get(remote) ?? new Map<string, Position>();
    positions.set(remote, map);
    map.delete(sid);
    const edge = parent.getBoundingClientRect().top;
    const row = [...parent.querySelectorAll<HTMLElement>("[data-item-index]")].find((el) => el.getBoundingClientRect().bottom > edge);
    const index = Number(row?.dataset.itemIndex) - firstNow.current;
    const item = currentItems.current[index - 1];
    map.set(sid, { state, count: count.current, width: parent.clientWidth, first: firstNow.current, anchor: row && item ? { key: item.key, index, offset: edge - row.getBoundingClientRect().top } : undefined });
    while (map.size > 8) map.delete(map.keys().next().value!);
  }), [remote, sid, parent]);
  // Where the list stands is only kept for coming back to a place that was
  // left in the middle: nothing is kept for a list that follows its end, and
  // for the rest it is read once a frame, when the layout the frame needs
  // anyway is there, not once for each change of the list.
  const pending = useRef(0);
  const following = useRef(follow);
  following.current = follow;
  const save = useCallback(() => {
    if (pending.current || following.current()) return;
    pending.current = requestAnimationFrame(() => {
      pending.current = 0;
      if (!following.current()) saveNow();
    });
  }, [saveNow]);
  useLayoutEffect(() => {
    parent.addEventListener("scroll", save, { passive: true });
    return () => {
      parent.removeEventListener("scroll", save);
      // Leaving: what was left to keep is kept now.
      if (pending.current) {
        cancelAnimationFrame(pending.current);
        pending.current = 0;
        saveNow();
      }
    };
  }, [parent, save, saveNow]);
  // The rows between the header and the footer, handed to the list with the
  // first index so that both change together.
  const entries = useMemo<Entry<T>[]>(() => [{ at: "header" }, ...items.map((item) => ({ at: "row" as const, item })), { at: "footer" }], [items]);
  const focusIndex = loaded && focusKey ? items.findIndex(it => it.key === focusKey) : -1;
  const restore = !focusKey && !top.stick && !anchor && saved && saved.count === items.length + 2 && saved.width === parent.clientWidth && saved.first === firstAtMount.current ? saved.state : undefined;
  return <Virtuoso
    ref={list}
    className="virtual-history"
    customScrollParent={parent}
    data={entries}
    firstItemIndex={first}
    computeItemKey={(_, entry) => entry.at === "row" ? "row-" + entry.item.key : "history-" + entry.at}
    itemContent={(_, entry) => entry.at === "header" ? <div className="history-boundary">{header}</div> : entry.at === "footer" ? <div className="history-boundary">{footer}</div> : <div className={rowClass?.(entry.item)}>{render(entry.item)}</div>}
    increaseViewportBy={400}
    {...(focusIndex >= 0 ? { initialTopMostItemIndex: { index: focusIndex + 1, align: "start" as const } } : restore ? { restoreStateFrom: restore } : anchor ? { initialTopMostItemIndex: anchor }
      : top.stick ? { initialTopMostItemIndex: { index: "LAST" as const, align: "end" as const } }
      : { initialScrollTop: top.top })}
    rangeChanged={save}
    totalListHeightChanged={save}
    followOutput={() => follow() ? "auto" : false}
  />;
}
