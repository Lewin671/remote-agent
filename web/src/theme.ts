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
