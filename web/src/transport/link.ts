import { ConnectError } from "@connectrpc/connect";
import { DispatchRegistry, type DispatchCall } from "./dispatch";
import { RequestSlot, CommandError, uncertain } from "./request";
import * as l10n from "../i18n";
import type { Path } from "./path";
import { Router } from "./router";

/** Owns authorizations, delivery evidence and logical streams across paths.
 * Encryption and signed outcome verification stay in the shared endpoint. */
export class Link {
  readonly router: Router;
  private readonly listeners = new Set<() => void>();
  subscribe(listener: () => void) { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; }
  changed() { for (const listener of this.listeners) listener(); }
  private readonly dispatchStates = new DispatchRegistry();
  private readonly streams = new Set<{ end(reason: string, notify?: boolean): void }>();
  constructor(path: Path) { this.router = new Router(path); }
  status(machineId: string) { return this.router.status(machineId); }
  sweep() { this.dispatchStates.sweep(); }

  state(slot: RequestSlot) {
    if (!(slot instanceof RequestSlot)) throw new CommandError("unknown_outcome", l10n.t("errors.the_relay_did_not_provide_a_verifiable_execution_result"), false, false, "unknown");
    const state = this.dispatchStates.get(slot.requestId, slot.deadline, slot.evidence, slot.prior);
    slot.evidence = state;
    return state;
  }
  async call<T>(slot: RequestSlot, work: (call: DispatchCall) => Promise<T>): Promise<T> {
    const state = this.state(slot);
    const call = state.open();
    try { return await work(call); }
    catch (error) {
      if (call.uncertain && !uncertain(error) && !(error instanceof CommandError && error.fromDaemon && error.outcome !== "not_sent")) {
        if (error instanceof CommandError) throw new CommandError(error.code, ["clock", "security_unavailable"].includes(error.code) ? l10n.t("errors.the_relay_did_not_provide_a_verifiable_execution_result") : error.message, error.fromDaemon, error.readOnly, "unknown", error.conflictKind, error.resultAvailable);
        throw new CommandError("unknown_outcome", l10n.t("errors.the_relay_did_not_provide_a_verifiable_execution_result"), false, false, "unknown");
      }
      throw error;
    } finally { call.close(); this.sweep(); }
  }

  delivery(call: DispatchCall) {
    const evidence = call.delivery();
    const attempt = this.router.path.attempt(() => evidence.dispatch());
    return {
      attemptId: attempt.attemptId,
      prepare: () => attempt.prepare(),
      send: (...args: Parameters<typeof attempt.send>) => attempt.send(...args),
      refreshAuth: (prior: boolean) => attempt.refreshAuth(prior),
      refusal(error: ConnectError) {
        const proof = attempt.refusal(error);
        if (proof) evidence.notForwarded();
        return proof;
      },
    };
  }

  openPathStream(address: string) { return this.router.path.openStream(address); }

  openStream(onClose: (reason: string) => void, stop: (reason: string) => void) {
    let ended = false;
    const stream = {
      end: (reason: string, notify = true) => {
        if (ended) return;
        ended = true;
        this.streams.delete(stream);
        stop(reason);
        // Relay-only policy preserves the existing explicit end-of-stream
        // behavior. A physical adapter has no reference to this listener.
        if (notify) onClose(reason);
      },
    };
    this.streams.add(stream);
    return stream;
  }
  endRelayStreams(reason: string) { this.closeStreams(reason); }
  closeStreams(reason: string) {
    for (const stream of this.streams) stream.end(reason);
  }
}
