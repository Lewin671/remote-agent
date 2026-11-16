// Made-up direct-path states of the computers for ?demo (never compiled into a build).
//   direct=direct          Using direct, both purposes (no second line)
//   direct=file | read     Using direct, only for files / only for loading
//   direct=measuring       Direct connected, measuring speed (the first seconds; only the relay measured so far)
//   direct=relay           connected, Using relay, "Direct isn't clearly faster" (48 vs 160 would not qualify)
//   direct=relay-faster    connected, Using relay, "Relay is faster"
//   direct=trying          Setting up direct
//   direct=none            Using relay, no reason known
//   direct=peer_unsupported | no_response | no_traversal | failed   Using relay, "Direct unavailable: <reason>"
//   direct=off             the switch is off, no status row
//   direct=unsupported     this browser has no WebRTC
//   direct=mix             the MacBook direct, VPS-dev unreachable by the networks, the rest on the relay
//   speeds=both | direct | relay | none   what was measured, for every state that has a path
//   (without speeds the state's own sample is shown; a file nobody has transferred says
//   "Compared on the next transfer")
// The switch works: turning it on shows Setting up direct for a moment, then measuring for two
// seconds (when the state it was opened with is connected), then that state.

import type { DirectEngine, DirectStatus } from "../transport/direct-engine";

type Scenario = Omit<DirectStatus, "fileMiBps" | "readMs"> & { off?: boolean };
const relay = { file: "relay", read: "relay", warming: false, compared: false } as const;

function scenario(name: string): Scenario {
  switch (name) {
    case "direct": return { state: "connected", file: "direct", read: "direct", warming: false, compared: true };
    case "file": return { state: "connected", file: "direct", read: "relay", warming: false, compared: true };
    case "read": return { state: "connected", file: "relay", read: "direct", warming: false, compared: true };
    case "measuring": return { state: "connected", ...relay, warming: true };
    case "relay": case "relay-faster": return { state: "connected", ...relay, compared: true };
    case "trying": return { state: "connecting", ...relay };
    case "peer_unsupported": case "no_response": case "no_traversal": case "failed": return { state: "none", reason: name, ...relay };
    case "off": return { state: "none", reason: "disabled", ...relay, off: true };
    case "unsupported": return { state: "unsupported", ...relay };
    default: return { state: "none", ...relay };
  }
}

function measured(name: string, speeds: string | null): Pick<DirectStatus, "fileMiBps" | "readMs"> {
  if (speeds === "none") return { fileMiBps: {}, readMs: {} };
  if (speeds === "both") return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 48, relay: 175 } };
  if (speeds === "direct") return { fileMiBps: { direct: 6.3 }, readMs: { direct: 48 } };
  if (speeds === "relay") return { fileMiBps: { relay: 4.1 }, readMs: { relay: 217 } };
  switch (name) {
    case "direct": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { direct: 48, relay: 175 } };
    case "file": return { fileMiBps: { direct: 6.3, relay: 4.1 }, readMs: { relay: 175 } };
    case "read": return { fileMiBps: {}, readMs: { direct: 48, relay: 175 } };
    case "measuring": return { fileMiBps: {}, readMs: { relay: 217 } };
    case "relay": return { fileMiBps: {}, readMs: { direct: 160, relay: 175 } };
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
    setTimeout(notify, 4600);
  };
  engine.status = (id): DirectStatus => {
    const name = nameFor(id), base = scenario(name);
    const sample = measured(name, speeds);
    if (base.state === "unsupported") return { ...base, ...sample };
    if (!engine.enabled(id)) return { state: "none", reason: "disabled", ...relay, fileMiBps: {}, readMs: {} };
    const turnedOn = switched.get(id);
    const age = Date.now() - (since.get(id) ?? -Infinity);
    if (turnedOn && age < 600) return { state: "none", ...relay, fileMiBps: {}, readMs: {} };
    if (turnedOn && age < 2600) return { state: "connecting", ...relay, fileMiBps: {}, readMs: {} };
    // Turned on: setting up, measuring for a moment, then the state the page was opened with
    // (from off, that is direct).
    const final = base.off ? scenario("direct") : base, finalName = base.off ? "direct" : name;
    if (turnedOn && age < 4600 && final.state === "connected") return { ...scenario("measuring"), ...measured("measuring", speeds) };
    return { ...final, ...measured(finalName, speeds) };
  };
}
