// Authentication and pairing bodies must never be replayed to a redirect target.
export const relayFetch: typeof globalThis.fetch = (input, init) =>
  globalThis.fetch(input, { ...init, redirect: "error" });
