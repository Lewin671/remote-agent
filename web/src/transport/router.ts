import type { Path } from "./path";

/** Only a relay adapter is installed in phase 0b. Callers ask this boundary
 * about the peer instead of deriving reachability from the account socket. */
export class Router {
  constructor(readonly path: Path) {}
  status(machineId: string) {
    const state = this.path.status(machineId);
    return { ...state, available: state.ready && state.reachable, canProbe: (serverReachable: boolean) => serverReachable || !state.relayRequired };
  }
}
