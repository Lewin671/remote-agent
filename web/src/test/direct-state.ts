import { vi } from "vitest";
import type { DirectEngine } from "../transport/direct-engine";

/** Puts a Remote's real DirectEngine into a chosen state, without any network,
 * so screens are tested against the real status accessor. */
export function setDirect(engine: DirectEngine, id: string, state: "connected" | "connecting" | "failed", reason?: "no_traversal" | "no_response" | "peer_unsupported") {
  vi.stubGlobal("RTCPeerConnection", class {});
  const peers = new Map<string, unknown>();
  const failures = new Map<string, string>();
  if (state !== "failed") peers.set(id, { id, head: "head", live: true, session: { active: true }, paths: new Map(state === "connected" ? [[1, { ready: true }], [2, { ready: true }]] : []), snapshotUntil: 0, downloadUntil: 0 });
  else if (reason) failures.set(id, reason);
  Object.assign(engine, { self: {}, codec: {}, record: { member: true }, freshUntil: Date.now() + 600_000, head: "head", peers, failures });
}
