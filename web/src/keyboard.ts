import { useEffect, useRef } from "react";

/** On a phone, finish dismissing the IME before a picker takes focus.
 * Listen to viewport geometry, rather than racing a fixed animation delay. */
export function afterKeyboardHidden(action: () => void, restingHeight = innerHeight): () => void {
  const viewport = window.visualViewport;
  let frame = 0;
  let done = false;
  const stop = () => {
    done = true;
    cancelAnimationFrame(frame);
    viewport?.removeEventListener("resize", check);
    viewport?.removeEventListener("scroll", check);
    window.removeEventListener("resize", check);
  };
  const check = () => {
    if (done) return;
    cancelAnimationFrame(frame);
    if (viewport && viewport.scale === 1 && viewport.height < restingHeight - 1) return;
    frame = requestAnimationFrame(() => { stop(); action(); });
  };
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  viewport?.addEventListener("resize", check);
  viewport?.addEventListener("scroll", check);
  window.addEventListener("resize", check);
  check();
  return stop;
}

export function useKeyboardDismiss() {
  const pending = useRef<() => void>(() => {});
  const resting = useRef<number | undefined>(undefined);
  useEffect(() => {
    // Chromium resizes the layout viewport too. Remember the space before
    // focusing the composer; innerHeight at the later click includes the IME.
    const focused = (event: FocusEvent) => {
      if (event.target instanceof HTMLElement && event.target.matches("textarea, input, [contenteditable=true]"))
        resting.current ??= window.visualViewport?.height ?? innerHeight;
    };
    document.addEventListener("focusin", focused);
    return () => { pending.current(); document.removeEventListener("focusin", focused); };
  }, []);
  return (action: () => void) => {
    pending.current();
    if (matchMedia("(min-width: 900px)").matches) action();
    else pending.current = afterKeyboardHidden(() => { resting.current = undefined; action(); }, resting.current);
  };
}
