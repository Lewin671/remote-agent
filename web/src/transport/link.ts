import { cleanup } from "../cleanup";
import type { Path } from "./path";
import { Router } from "./router";

/** Owns physical path routing and logical streams across relay and direct paths.
 * Command lifecycle and retries now live in shared Core. */
export class Link {
  readonly router: Router;
  private readonly listeners = new Set<() => void>();
  subscribe(listener: () => void) { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; }
  changed() { cleanup(...this.listeners); }
  private readonly streams = new Set<{ end(reason: string, notify?: boolean): void }>();
  constructor(path: Path) { this.router = new Router(path); }
  status(machineId: string) { return this.router.status(machineId); }

  openPathStream(address: string) { return this.router.path.openStream(address); }

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
