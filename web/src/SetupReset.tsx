import * as l10n from "./i18n";
import { useState } from "react";
import { Alert, Banner } from "./ui";
import { authorityExitAdvice } from "./security-operation";
import { forgetAccount } from "./storage";

/** The retry path retains uncertain work; only this explicit confirmation discards it. */
export function SetupReset({ onDone }: { onDone: () => void }) {
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const [advice, setAdvice] = useState("");
  return (
    <>
      {error && <Banner kind="err">{error}</Banner>}
      <button className="link mid" onClick={() => { void authorityExitAdvice().then(a => { setAdvice(a.message); setAsking(true); }).catch(e => setError(String(e))); }}>{l10n.t("security.exit_and_start_over")}</button>
      {asking && (
        <Alert
          title={l10n.t("security.exit_and_delete_local_keys")}
          text={
            error || advice ||
            l10n.t("security.the_local_account_unfinished_registration_and_security_operations_will")
          }
          ok={l10n.t("security.confirm_exit")}
          danger
          onCancel={() => setAsking(false)}
          onOk={async () => {
            try {
              await forgetAccount();
              setAsking(false);
              location.hash = "";
              onDone();
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e));
            }
          }}
        />
      )}
    </>
  );
}
