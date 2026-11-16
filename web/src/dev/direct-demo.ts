// Made-up direct-path states of the computers for ?demo (never compiled into a build).
//   direct=direct          direct for files and reading
//   direct=file | read     direct for only files, or only reading
//   direct=relay           connected, both still on the relay, speeds not compared yet
//   direct=relay-faster    connected, both on the relay, which measured faster
//   direct=trying          trying to connect
//   direct=none            not direct, no reason known
//   direct=peer_unsupported | no_response | no_traversal | failed   not direct, with that reason
//   direct=off             the switch is off
//   direct=unsupported     this browser has no WebRTC
//   direct=mix             the MacBook direct, VPS-dev unreachable by the networks, the rest on the relay
//   speeds=both | direct | relay | none   what was measured, for every state that has a path
//   (without speeds the state's own sample is shown)
// The switch works: turning it on shows "trying" for a moment, then the state above.

import type { DirectEngine, DirectStatus } from "../transport/direct-engine";

type Scenario = Omit<DirectStatus, "fileMiBps" | "readMs"> & { off?: boolean };
const relay = { file: "relay", read: "relay" } as const;

function scenario(name: string): Scenario {
  switch (name) {
    case "direct": return { state: "connected", file: "direct", read: "direct" };
    case "file": return { state: "connected", file: "direct", read: "relay" };
    case "read": return { state: "connected", file: "relay", read: "direct" };
    case "relay": case "relay-faster": return { state: "connected", ...relay };
    case "trying": return { state: "connecting", ...relay };
    case "peer_unsupported": case "no_response": case "no_traversal": case "failed": return { state: "none", reason: name, ...relay };
    case "off": return { state: "none", reason: "disabled", ...relay, off: true };
    case "unsupported": return { state: "unsupported", ...relay };
    default: return { state: "none", ...relay };
  }
}

function measured(name: string, speeds: string | null): Pick<DirectStatus, "fileMiBps" | "readMs"> {
  if (speeds === "none") return { fileMiBps: {}, readMs: {} };
  if (speeds === "both") return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 12, relay: 180 } };
  if (speeds === "direct") return { fileMiBps: { direct: 6.3 }, readMs: { direct: 12 } };
  if (speeds === "relay") return { fileMiBps: { relay: 4.1 }, readMs: { relay: 180 } };
  switch (name) {
    case "direct": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 12, relay: 180 } };
    case "file": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { relay: 180 } };
    case "read": return { fileMiBps: {}, readMs: { direct: 12, relay: 180 } };
    case "relay-faster": return { fileMiBps: { direct: 3.2, relay: 4.1 }, readMs: { direct: 220, relay: 180 } };
    default: return { fileMiBps: {}, readMs: {} };
  }
}

export function seedDirect(engine: DirectEngine, opts: URLSearchParams) {
  const asked = opts.get("direct");
  if (!asked) return;
  const speeds = opts.get("speeds");
  const nameFor = (id: string) => (asked === "mix" ? (id === "mac" ? "direct" : id === "vps" ? "no_traversal" : "relay") : asked);
  const switched = new Map<string, boolean>();
  const since = new Map<string, number>();
  const notify = () => (engine as unknown as { notify(): void }).notify();
  engine.enabled = (id) => switched.get(id) ?? !scenario(nameFor(id)).off;
  engine.setEnabled = (id, on) => {
    switched.set(id, on);
    since.set(id, Date.now());
    notify();
    // Nothing reported for a moment, then trying, then the state the page was opened with.
    setTimeout(notify, 600);
    setTimeout(notify, 2600);
  };
  engine.status = (id): DirectStatus => {
    const name = nameFor(id), base = scenario(name);
    const sample = measured(name, speeds);
    if (base.state === "unsupported") return { ...base, ...sample };
    if (!engine.enabled(id)) return { state: "none", reason: "disabled", ...relay, fileMiBps: {}, readMs: {} };
    const age = Date.now() - (since.get(id) ?? -Infinity);
    if (switched.get(id) && age < 600) return { state: "none", ...relay, fileMiBps: {}, readMs: {} };
    if (switched.get(id) && age < 2600) return { state: "connecting", ...relay, fileMiBps: {}, readMs: {} };
    // Turned on from off: after trying, it is direct.
    return base.off ? { ...scenario("direct"), ...measured("direct", speeds) } : { ...base, ...sample };
  };
}
