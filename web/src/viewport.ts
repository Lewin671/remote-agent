/** Keep the app in the visible viewport when a mobile keyboard covers the
 * layout viewport. No easing: the browser already moves the keyboard.
 * offsetTop also accounts for Safari panning to reveal a focused field.
 * Pinch zoom is left to the browser instead of reflowing the zoomed page. */
export function watchViewport() {
  const viewport = window.visualViewport;
  const style = document.documentElement.style;
  const update = () => {
    if (viewport && viewport.scale !== 1) return;
    style.setProperty("--viewport-height", `${viewport?.height ?? window.innerHeight}px`);
    style.setProperty("--viewport-top", `${viewport?.offsetTop ?? 0}px`);
  };
  update();
  window.addEventListener("resize", update);
  viewport?.addEventListener("resize", update);
  viewport?.addEventListener("scroll", update);
  return () => {
    window.removeEventListener("resize", update);
    viewport?.removeEventListener("resize", update);
    viewport?.removeEventListener("scroll", update);
    style.removeProperty("--viewport-height");
    style.removeProperty("--viewport-top");
  };
}
