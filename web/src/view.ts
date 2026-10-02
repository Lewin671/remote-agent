// The session view last picked. The choice is kept per browser, and every
// session opens in it.

export type View = "chat" | "term" | "split";

const KEY = "ra-view";

export function getView(): View {
  const v = localStorage.getItem(KEY);
  return v === "term" || v === "split" ? v : "chat";
}

export function setView(v: View) {
  if (v === "chat") localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, v);
}
