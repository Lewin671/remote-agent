import { useLayoutEffect, useRef, useState, type MouseEvent, type PointerEvent, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useEscape } from "../ui";

/** Cancel pointer focus, not clicks or touch scrolling. Never focus the
 * textarea: a picker opened with the keyboard down must leave it down. */
export function keepComposerFocus(e: PointerEvent | MouseEvent) {
  if (document.activeElement instanceof HTMLTextAreaElement && document.activeElement.closest(".ntask")) e.preventDefault();
}

export type ComposerAnchor = {
  anchor: RefObject<HTMLElement | null>;
  boundary: RefObject<HTMLElement | null>;
};

/** A nonmodal picker in the phone composer's hierarchy. Content wraps up to
 * the available viewport; the anchor never participates in its layout. */
export function ComposerPicker({ anchor, boundary, title, onClose, children }: ComposerAnchor & {
  title?: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const root = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState({ left: 0, bottom: 0, width: 0, height: 0 });
  useEscape(onClose);
  useLayoutEffect(() => {
    const measure = () => {
      const box = boundary.current;
      const at = anchor.current;
      if (!box || !at) return;
      const parent = box.getBoundingClientRect();
      const rect = at.getBoundingClientRect();
      const head = box.querySelector(".nt-hd")?.getBoundingClientRect().bottom ?? parent.top;
      const top = head + 8;
      const width = title ? rect.width : Math.min(240, parent.width - 24);
      setPlace({
        left: Math.max(12, Math.min(rect.left - parent.left, parent.width - width - 12)),
        bottom: parent.bottom - rect.top + 8,
        width,
        height: Math.max(0, Math.min(600, rect.top - top - 8)),
      });
    };
    measure();
    const observer = new ResizeObserver(measure);
    if (boundary.current) observer.observe(boundary.current);
    if (anchor.current) observer.observe(anchor.current);
    window.visualViewport?.addEventListener("resize", measure);
    window.visualViewport?.addEventListener("scroll", measure);
    return () => {
      observer.disconnect();
      window.visualViewport?.removeEventListener("resize", measure);
      window.visualViewport?.removeEventListener("scroll", measure);
    };
  }, [anchor, boundary, title]);
  useLayoutEffect(() => {
    const guard = (e: globalThis.MouseEvent) => {
      if (!(e.target instanceof Element) || e.target.closest("input, textarea, [contenteditable]")) return;
      if (document.activeElement instanceof HTMLTextAreaElement && boundary.current?.contains(document.activeElement)) e.preventDefault();
    };
    const outside = (e: Event) => {
      const target = e.target;
      if (!(target instanceof Element) || root.current?.contains(target)) return;
      // Let the chips toggle/switch in their click handlers, without an
      // outside dismissal reopening the same picker in that click.
      if (boundary.current?.contains(target) && target.closest("[data-composer-picker]")) return;
      onClose();
    };
    const typing = (e: Event) => {
      if (e.target instanceof HTMLTextAreaElement && boundary.current?.contains(e.target)) onClose();
    };
    // Keep the guard installed through the compatibility mouse events.
    // Closing on pointerdown would unmount it before Safari's mousedown,
    // which can blur the field even after pointerdown was cancelled.
    document.addEventListener("pointerdown", guard, true);
    document.addEventListener("mousedown", guard, true);
    document.addEventListener("click", outside, true);
    document.addEventListener("focusin", outside);
    document.addEventListener("beforeinput", typing);
    return () => {
      document.removeEventListener("pointerdown", guard, true);
      document.removeEventListener("mousedown", guard, true);
      document.removeEventListener("click", outside, true);
      document.removeEventListener("focusin", outside);
      document.removeEventListener("beforeinput", typing);
    };
  }, [boundary, onClose]);
  if (!boundary.current) return null;
  return createPortal(
    <div ref={root} className={"composer-picker" + (title ? " model-picker" : "")} role="dialog" aria-label={title ?? "CLI"}
      onPointerDown={keepComposerFocus} onMouseDown={keepComposerFocus}
      style={{ left: place.left, bottom: place.bottom, width: place.width, maxHeight: place.height, visibility: place.width ? undefined : "hidden" }}>
      {title ? children : <div className="composer-picker-scroll">{children}</div>}
    </div>, boundary.current,
  );
}
