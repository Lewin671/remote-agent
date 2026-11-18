import * as l10n from "./i18n";
/** Matches the inline HTML shell while the saved account is read. */
export function Startup() {
  return (
    <main id="startup" className="startup" aria-label={l10n.t("startup.opening_remote_agent")}>
      <div className="startup-mark" aria-hidden="true">
        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.3" strokeLinecap="round" strokeLinejoin="round">
          <path d="m6.2 7.2 5.4 4.8-5.4 4.8" />
          <path d="M13.2 16.8h4.8" />
        </svg>
      </div>
      <p id="startup-status" role="status">{l10n.t("startup.opening")}</p>
      <a id="startup-retry" href="" hidden>{l10n.t("common.reload")}</a>
    </main>
  );
}
