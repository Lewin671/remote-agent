/** Matches the inline HTML shell while the saved account is read. */
export function Startup() {
  return (
    <main id="startup" className="startup" aria-label="正在打开 remote-agent">
      <h1>remote-agent</h1>
      <p id="startup-status" role="status">正在打开…</p>
      <a id="startup-retry" href="" hidden>重新加载</a>
    </main>
  );
}
