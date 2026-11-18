import * as l10n from "./i18n";
/** Matches the inline HTML shell while the saved account is read. */
export function Startup() {
  // Preserve elapsed boot time so replacing inline #startup does not reset the 300ms delay.
  const elapsed = typeof performance === "undefined" ? 0 : Math.max(0, performance.now());
  const delay = `${Math.max(0, Math.round(300 - elapsed))}ms`;
  return (
    <main id="startup" className="startup" aria-label={l10n.t("startup.opening_remote_agent")}>
      <div className="startup-mark" aria-hidden="true">
        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.3" strokeLinecap="round" strokeLinejoin="round">
          <path d="m6.2 7.2 5.4 4.8-5.4 4.8" />
          <path d="M13.2 16.8h4.8" />
        </svg>
      </div>
      <p id="startup-status" role="status" style={{ animationDelay: delay }}>{l10n.t("startup.opening")}</p>
      <a id="startup-retry" href="" hidden>{l10n.t("common.reload")}</a>
    </main>
  );
}
