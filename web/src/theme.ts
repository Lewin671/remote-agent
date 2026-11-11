// Light, dark or following the system. The choice is kept per browser.

export type Theme = "auto" | "light" | "dark";

const KEY = "ra-theme";

export function getTheme(): Theme {
  const t = localStorage.getItem(KEY);
  return t === "light" || t === "dark" ? t : "auto";
}

/** The page's paper in each theme (the paper token); the browser's own
 * bar takes it, so the page runs to the top with no colored band. */
const PAPER = { light: "#ffffff", dark: "#000000" };

/** Applies a theme; "auto" leaves it to prefers-color-scheme. */
export function applyTheme(t: Theme = getTheme()) {
  const root = document.documentElement;
  if (t === "auto") delete root.dataset.theme;
  else root.dataset.theme = t;
  // index.html has one theme-color per scheme; a theme picked here overrides both.
  for (const meta of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]'))
    meta.content = PAPER[t === "auto" ? (meta.media.includes("dark") ? "dark" : "light") : t];
}

export function setTheme(t: Theme) {
  if (t === "auto") localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, t);
  // Light to dark is no motion (design-system §4 动效): the colors that move
  // for other reasons (send, knob) do not slide between themes.
  const root = document.documentElement;
  root.dataset.themeSwitch = "";
  applyTheme(t);
  requestAnimationFrame(() => requestAnimationFrame(() => delete root.dataset.themeSwitch));
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
