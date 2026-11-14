/** Evidence belongs to an authorization; each call and each delivery has an owner. */
export class DispatchState {
  private possible = 0;
  private active = 0;
  constructor(private prior: boolean) {}
  includePrior(prior: boolean) { this.prior ||= prior; }
  get unresolved() { return this.prior || this.possible > 0 || this.active > 0; }
  open() {
    this.active++;
    let closed = false;
    const state = this;
    return {
      get hasPrior() { return state.prior || state.possible > 0; },
      get uncertain() { return state.prior || state.possible > 0 || state.active > (closed ? 0 : 1); },
      close() { if (!closed) { closed = true; state.active--; } },
      delivery() {
        let registered = false, settled = false;
        return {
          dispatch() {
            if (registered || settled || closed) throw new Error("dispatch_handle_closed");
            registered = true; state.possible++;
          },
          notForwarded() {
            if (!settled) { settled = true; if (registered) state.possible--; }
          },
        };
      },
    };
  }
}
export type DispatchCall = ReturnType<DispatchState["open"]>;
