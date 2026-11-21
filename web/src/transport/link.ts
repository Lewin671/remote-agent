import { cleanup } from "../cleanup";
import type { Path } from "./path";

/** Owns physical path readiness and active transport streams.
 * Command routing, retries, and direct session ownership live in shared Core. */
export class Link {
  private readonly listeners = new Set<() => void>();
  subscribe(listener: () => void) { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; }
  changed() { cleanup(...this.listeners); }
  private readonly streams = new Set<{ end(reason: string, notify?: boolean): void }>();
  constructor(readonly path: Path) {}

  status(machineId: string) {
    const state = this.path.status(machineId);
    return {
      ...state,
      available: state.ready && state.reachable,
      canProbe: (serverReachable: boolean) => serverReachable || !state.relayRequired,
    };
  }

  openPathStream(address: string) { return this.path.openStream(address); }

  openStream(onClose: (reason: string) => void, stop: (reason: string) => void) {
    let ended = false;
    const stream = {
      end: (reason: string, notify = true) => {
        if (ended) return;
        ended = true;
        this.streams.delete(stream);
        cleanup(() => stop(reason), () => { if (notify) onClose(reason); });
      },
    };
    this.streams.add(stream);
    return stream;
  }
  endRelayStreams(reason: string) { this.closeStreams(reason); }
  closeStreams(reason: string) {
    cleanup(...[...this.streams].map(stream => () => stream.end(reason)));
  }
}
