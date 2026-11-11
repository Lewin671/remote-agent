import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { Virtuoso, type StateSnapshot, type VirtuosoHandle } from "react-virtuoso";
import type { Remote } from "../remote";

type Position = { state: StateSnapshot; count: number; width: number; anchor?: { key: string; index: number; offset: number } };
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
  loaded: boolean;
  focusKey?: string;
  /** A class for a row that has only now arrived (its entrance). */
  rowClass?: (item: T) => string | undefined;
  /** The history is known to be long before all of it is here (the session's
   * event count), so the virtual list is used from the first draw instead of
   * replacing the plain one when the count passes 100. */
  long?: boolean;
};

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
  return <WindowedRows {...props} parent={parent} initialTop={initial.long || initial.loading ? initial.top : undefined} />;
}

function WindowedRows<T extends { key: string }>({ items, render, parent, remote, sid, follow, header, footer, initialTop, focusKey, loaded, rowClass }: Props<T> & {
  parent: HTMLElement;
  initialTop?: { top: number; stick: boolean };
}) {
  const list = useRef<VirtuosoHandle>(null);
  const focused = useRef<string | undefined>(undefined);
  // The list is drawn with estimated row heights and corrects them as the
  // rows are measured, and the history comes in pages that change what it
  // holds: both move it. It is not shown until it holds rows, all the history
  // is here and its height has been the same for a few frames (at the bottom,
  // for a list that follows the end); one that has stood still for a second
  // is shown whatever it waits for. A placeholder stands in meanwhile. Plain
  // lists are placed in the frame they are drawn in and need none of this.
  const ready = useRef(loaded);
  ready.current = loaded;
  useLayoutEffect(() => {
    parent.classList.add("mo-hold");
    const follows = !focusKey && (initialTop ? initialTop.stick : parent.scrollTop + parent.clientHeight >= parent.scrollHeight - 60);
    const t0 = performance.now();
    let still = 0;
    let last = -1;
    let id = requestAnimationFrame(function tick() {
      // The list stops short of the chat's own bottom padding: take that last step.
      if (follows && parent.scrollHeight - parent.scrollTop - parent.clientHeight < 40) parent.scrollTop = parent.scrollHeight;
      const sh = parent.scrollHeight;
      const drawn = !!parent.querySelector("[data-item-index]") && sh > parent.clientHeight;
      const there = !follows || sh - parent.scrollTop - parent.clientHeight < 4;
      still = sh === last && there && drawn ? still + 1 : 0;
      last = sh;
      if ((still >= 3 && ready.current) || still >= 60 || performance.now() - t0 > 6000) return parent.classList.remove("mo-hold");
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
  const save = useCallback(() => list.current?.getState((state) => {
    if (!state.ranges.length) return;
    const map = positions.get(remote) ?? new Map<string, Position>();
    positions.set(remote, map);
    map.delete(sid);
    const edge = parent.getBoundingClientRect().top;
    const row = [...parent.querySelectorAll<HTMLElement>("[data-item-index]")].find((el) => el.getBoundingClientRect().bottom > edge);
    const index = Number(row?.dataset.itemIndex);
    const item = currentItems.current[index - 1];
    map.set(sid, { state, count: count.current, width: parent.clientWidth, anchor: row && item ? { key: item.key, index, offset: edge - row.getBoundingClientRect().top } : undefined });
    while (map.size > 8) map.delete(map.keys().next().value!);
  }), [remote, sid, parent]);
  useLayoutEffect(() => {
    parent.addEventListener("scroll", save, { passive: true });
    return () => parent.removeEventListener("scroll", save);
  }, [parent, save]);
  const focusIndex = loaded && focusKey ? items.findIndex(it => it.key === focusKey) : -1;
  const restore = !focusKey && !top.stick && !anchor && saved && saved.count === items.length + 2 && saved.width === parent.clientWidth ? saved.state : undefined;
  return <Virtuoso
    ref={list}
    className="virtual-history"
    customScrollParent={parent}
    totalCount={items.length + 2}
    computeItemKey={(index) => index === 0 ? "history-header" : index === items.length + 1 ? "history-footer" : "row-" + items[index - 1].key}
    itemContent={(index) => index === 0 ? <div className="history-boundary">{header}</div> : index === items.length + 1 ? <div className="history-boundary">{footer}</div> : <div className={rowClass?.(items[index - 1])}>{render(items[index - 1])}</div>}
    increaseViewportBy={400}
    {...(focusIndex >= 0 ? { initialTopMostItemIndex: { index: focusIndex + 1, align: "start" as const } } : restore ? { restoreStateFrom: restore } : anchor ? { initialTopMostItemIndex: anchor }
      : top.stick ? { initialTopMostItemIndex: { index: items.length + 1, align: "end" as const } }
      : { initialScrollTop: top.top })}
    rangeChanged={() => queueMicrotask(save)}
    totalListHeightChanged={() => queueMicrotask(save)}
    atBottomThreshold={60}
    followOutput={(atBottom) => atBottom || follow() ? "auto" : false}
  />;
}
