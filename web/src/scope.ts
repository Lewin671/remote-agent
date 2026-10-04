// The home's scope: one project, or all of them (client-v27). The choice
// is kept per browser, since different devices tend to watch different
// projects.

export const ALL = "*";

const KEY = "ra-scope";
const subs = new Set<() => void>();

export function savedScope(): string {
  return localStorage.getItem(KEY) ?? "";
}

export function saveScope(scope: string) {
  localStorage.setItem(KEY, scope);
  subs.forEach((fn) => fn());
}

export function subscribeScope(fn: () => void) {
  subs.add(fn);
  return () => void subs.delete(fn);
}

/** The scope shown, given the projects still on the list: the only
 * project when there is one, else the saved one while it is listed, else
 * all projects. */
export function resolveScope(saved: string, keys: string[]): string {
  if (keys.length === 1) return keys[0];
  return keys.includes(saved) ? saved : ALL;
}
