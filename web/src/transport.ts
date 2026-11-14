import { t } from "./i18n";

const epochs = new Map<string, string>();
const discovery = new Map<string, Promise<string>>();
export const RELAY_EPOCH_HEADER = "Remote-Agent-Relay-Epoch";
function origin(input: RequestInfo | URL) {
  return new URL(input instanceof Request ? input.url : String(input), globalThis.location?.href).origin;
}
// Browser-internal retries are opaque. The frozen body epoch and relay decision
// record, rather than a fetch-call count, make NotForwarded usable evidence.
export const relayFetch: typeof globalThis.fetch = async (input, init) => {
  const response = await globalThis.fetch(input, { ...init, redirect: "error" });
  const epoch = response.headers.get(RELAY_EPOCH_HEADER);
  if (epoch) epochs.set(origin(input), epoch);
  return response;
};
export function relayEpoch(server: string): Promise<string> {
  const key = origin(server), known = epochs.get(key);
  if (known) return Promise.resolve(known);
  const pending = discovery.get(key);
  if (pending) return pending;
  const work = relayFetch(key + "/healthz", {cache: "no-store"}).then(() => {
    const epoch = epochs.get(key);
    if (!epoch) throw new Error(t("errors.the_relay_did_not_provide_a_verifiable_execution_result"));
    return epoch;
  }).finally(() => discovery.delete(key));
  discovery.set(key, work);
  return work;
}
