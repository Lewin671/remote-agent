import { useSyncExternalStore } from "react";
import { localeData, messages, type Locale, type MessageArgs, type MessageKey } from "./generated";
export type { Locale, MessageKey } from "./generated";

export function resolveLocale(preference = "auto", system: readonly string[] = typeof navigator === "undefined" ? [] : navigator.languages): Locale {
  const explicit = localeData.locales.find(l => l.id === preference);
  if (explicit) return explicit.id;
  for (const tag of system) {
    const base = tag.replaceAll("_", "-").toLowerCase().split(/[.@-]/)[0];
    const match = localeData.locales.find(l => (l.languages as readonly string[]).includes(base));
    if (match) return match.id;
  }
  return localeData.default;
}

const storageKey = "ra.language";
const listeners = new Set<() => void>();
let debugLocale: Locale | undefined;
function preference(): string {
  try { return localStorage.getItem(storageKey) ?? "auto"; } catch { return "auto"; }
}
function current(): Locale {
  // Stage 0: preserve every existing Chinese screen, regardless of OS language.
  return debugLocale ?? (localeData.selectionEnabled ? resolveLocale(preference()) : localeData.default);
}
function changed() {
  if (typeof document !== "undefined") document.documentElement.lang = current();
  listeners.forEach(fn => fn());
}
export const localeStore = {
  snapshot: current,
  subscribe(fn: () => void) { listeners.add(fn); return () => { listeners.delete(fn); }; },
  setPreference(value: "auto" | Locale) {
    try { localStorage.setItem(storageKey, value); } catch { /* Private storage can be unavailable. */ }
    changed();
  },
  /** Test/development override, intentionally absent from the product settings. */
  overrideForTesting(value?: Locale) { debugLocale = value; changed(); },
};
if (typeof window !== "undefined") {
  window.addEventListener("languagechange", changed);
  window.addEventListener("storage", e => { if (e.key === storageKey || e.key === null) changed(); });
  changed();
}
export function useLocale(): Locale { return useSyncExternalStore(localeStore.subscribe, current, () => localeData.default); }

type Args<K extends MessageKey> = MessageArgs[K] extends Record<string, never> ? [args?: MessageArgs[K], locale?: Locale] : [args: MessageArgs[K], locale?: Locale];
export function t<K extends MessageKey>(key: K, ...[args, locale = current()]: Args<K>): string {
  let value: string | { plural?: string; select?: string; forms: Readonly<Record<string, string>> } = messages[locale]?.[key] ?? messages[localeData.default][key];
  if (!value) throw new Error("Unknown UI message key");
  const values = (args ?? {}) as Record<string, string | number>;
  if (typeof value !== "string") {
    const selected = value.plural ? values[value.plural] : values[value.select!];
    if (value.plural && (typeof selected !== "number" || !Number.isSafeInteger(selected) || selected < 0)) throw new Error("Invalid plural count");
    const form = value.plural ? new Intl.PluralRules(locale).select(selected as number) : String(selected);
    value = value.forms[form] ?? value.forms.other;
  }
  return value.replace(/\{([a-z][a-zA-Z0-9]*)\}/g, (_, name: string) => {
    if (!(name in values)) throw new Error("Missing message parameter");
    return String(values[name]);
  });
}
