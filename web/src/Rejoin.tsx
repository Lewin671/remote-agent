import * as l10n from "./i18n";
import { useState } from "react";
import { Alert, Banner } from "./ui";
import { authorityExitAdvice } from "./security-operation";
import { forgetAccount, loadRegistration } from "./storage";
import { errorText } from "./remote";

/** Rejoin keeps only the server; account keys and cached ciphertext are erased. */
export function RejoinButton({ server, children, onDone }: {
  server: string; children: string; onDone?: () => void;
}) {
  const [advice, setAdvice] = useState<string>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const leave = async (confirmed = false) => {
    if (busy) return;
    setBusy(true);
    try {
      const status = await authorityExitAdvice();
      const pending = !!await loadRegistration();
      const warning = status.authority || status.unresolved || pending
        ? (pending ? l10n.t("security.this_device_has_an_unfinished_join_request_deleting_it") : status.message)
          + l10n.t("security.this_deletes_the_account_keys_and_cache_on_this") : undefined;
      if (warning && (!confirmed || warning !== advice)) { setAdvice(warning); return; }
      localStorage.setItem("ra-server", server);
      await forgetAccount();
      location.hash = "/pair?";
      if (onDone) onDone(); else location.reload();
    } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  };
  return <>
    <button className="btn pri" disabled={busy} onClick={() => void leave()}>{children}</button>
    {error && <Banner kind="err">{error}</Banner>}
    {advice && <Alert title={l10n.t("security.delete_local_keys_and_rejoin")} text={advice} ok={l10n.t("security.rejoin")} danger
      onCancel={() => setAdvice(undefined)} onOk={() => void leave(true)} />}
  </>;
}

export function RetainedIdentityDeniedScreen({ server, onDone }: { server: string; onDone: () => void }) {
  return <div className="center"><div className="gone">
    <div className="et">{l10n.t("security.the_server_does_not_recognize_this_device")}</div>
    <p className="muted small">{l10n.t("security.it_may_have_been_removed_or_the_service_may")}</p>
    <RejoinButton server={server} onDone={onDone}>{l10n.t("security.rejoin_with_a_pairing_code")}</RejoinButton>
    <p className="muted small">{l10n.t("security.the_account_keys_and_cache_in_this_browser_will")}</p>
  </div></div>;
}
