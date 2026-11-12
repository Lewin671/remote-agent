import * as l10n from "../i18n";
export function PersistenceResult({ persisted }: { persisted: boolean }) {
  return (
    <p className="txt" role="status">
      {persisted ? l10n.t("security.asked_the_browser_to_retain_data") : l10n.t("security.the_browser_did_not_promise_to_retain_data")}
    </p>
  );
}
export function CodeBackup({
  code,
  saved,
  onSaved,
}: {
  code: string;
  saved: boolean;
  onSaved: (v: boolean) => void;
}) {
  return (
    <>
      <div className="label">{l10n.t("security.new_recovery_code_shown_only_once")}</div>
      <pre className="recovery-code" aria-label={l10n.t("security.new_recovery_code")}>
        {code}
      </pre>
      <p className="gf">{l10n.t("security.save_the_recovery_code_offline_anyone_with_it_can")}</p>
      <div className="grp">
        <label className="row backup-check">
          <input
            type="checkbox"
            checked={saved}
            onChange={(e) => onSaved(e.target.checked)}
          />
          {l10n.t("security.i_saved_the_new_recovery_code_offline")}</label>
      </div>
    </>
  );
}
