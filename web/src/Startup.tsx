import * as l10n from "./i18n";
/** Matches the inline HTML shell while the saved account is read. */
export function Startup() {
  return (
    <main id="startup" className="startup" aria-label={l10n.t("startup.opening_remote_agent")}>
      <h1>remote-agent</h1>
      <p id="startup-status" role="status">{l10n.t("startup.opening")}</p>
      <a id="startup-retry" href="" hidden>{l10n.t("common.reload")}</a>
    </main>
  );
}
