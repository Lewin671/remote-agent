import type { Path } from "./path";

export type DualLegResult<T> = { trusted: true; value: T } | { trusted: false; error: unknown };

/** Selects a transport path and merges dual-sent interactive legs by taking the
 * first trusted daemon outcome and ignoring late arrivals. */
export class Router {
  constructor(readonly path: Path) {}
  status(machineId: string) {
    const state = this.path.status(machineId);
    return { ...state, available: state.ready && state.reachable, canProbe: (serverReachable: boolean) => serverReachable || !state.relayRequired };
  }

  /** Runs primary (direct) and fallback (relay) legs concurrently with the same
   * sealed bytes. The first trusted result wins immediately and aborts the
   * other leg; late arrivals never overwrite the winner. If neither leg yields
   * a trusted result, the fallback failure is surfaced. */
  async dual<T>(
    primary: (signal: AbortSignal) => Promise<DualLegResult<T>>,
    fallback: (signal: AbortSignal) => Promise<DualLegResult<T>>,
    outer?: AbortSignal,
  ): Promise<T> {
    outer?.throwIfAborted();
    const ctrlPrimary = new AbortController();
    const ctrlFallback = new AbortController();
    const onOuter = () => {
      ctrlPrimary.abort(outer?.reason);
      ctrlFallback.abort(outer?.reason);
    };
    outer?.addEventListener("abort", onOuter, { once: true });

    try {
      return await new Promise<T>((resolve, reject) => {
        let settled = false;
        let pending = 2;
        let fallbackError: unknown;
        let primaryError: unknown;

        const finish = (isPrimary: boolean, res: DualLegResult<T>) => {
          if (settled) return;
          if (res.trusted) {
            settled = true;
            (isPrimary ? ctrlFallback : ctrlPrimary).abort();
            resolve(res.value);
            return;
          }
          if (isPrimary) primaryError = res.error;
          else fallbackError = res.error;
          pending--;
          if (pending === 0) {
            settled = true;
            reject(fallbackError ?? primaryError);
          }
        };

        void primary(ctrlPrimary.signal).then(
          res => finish(true, res),
          err => finish(true, { trusted: false, error: err }),
        );
        void fallback(ctrlFallback.signal).then(
          res => finish(false, res),
          err => finish(false, { trusted: false, error: err }),
        );
      });
    } finally {
      outer?.removeEventListener("abort", onOuter);
    }
  }
}
