// Light, dark or following the system. The choice is kept per browser.

export type Theme = "auto" | "light" | "dark";

const KEY = "ra-theme";

export function getTheme(): Theme {
  const t = localStorage.getItem(KEY);
  return t === "light" || t === "dark" ? t : "auto";
}

/** Applies a theme; "auto" leaves it to prefers-color-scheme. */
export function applyTheme(t: Theme = getTheme()) {
  const root = document.documentElement;
  if (t === "auto") delete root.dataset.theme;
  else root.dataset.theme = t;
}

export function setTheme(t: Theme) {
  if (t === "auto") localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, t);
  applyTheme(t);
}

/**
 * Apple devices draw the mockup's weights with SF and PingFang. Elsewhere
 * Chinese comes from fonts that draw each weight heavier, so the page asks
 * for one step lighter (design-system §3; the weights live in styles.css).
 * iPadOS reports itself as a Mac.
 */
export function shiftedWeights(ua: string): boolean {
  return !/Macintosh|Mac OS X|iPhone|iPad|iPod/.test(ua);
}

export function applyFonts(ua: string = navigator.userAgent) {
  if (shiftedWeights(ua)) document.documentElement.dataset.weights = "shifted";
  else delete document.documentElement.dataset.weights;
}
