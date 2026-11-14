/** Finish every teardown step before reporting the first failure. */
export function cleanup(...steps: (() => void)[]): void {
  let failed = false;
  let first: unknown;
  for (const step of steps) {
    try { step(); }
    catch (error) { if (!failed) { failed = true; first = error; } }
  }
  if (failed) throw first;
}
