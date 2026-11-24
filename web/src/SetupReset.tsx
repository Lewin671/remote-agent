import * as l10n from "./i18n";
import { useState } from "react";
import { Alert, Banner } from "./ui";
import { createStandaloneSecurityClient, translateSecurityKey } from "./core/security-client";

/** The retry path retains uncertain work; only this explicit confirmation discards it. */
export function SetupReset({ onDone }: { onDone: () => void }) {
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const [advice, setAdvice] = useState("");
  return (
    <>
      {error && <Banner kind="err">{error}</Banner>}
      <button
        className="link mid"
        onClick={() => {
          void (async () => {
            const standalone = createStandaloneSecurityClient();
            try {
              await standalone.client.bootstrap();
              const a = await standalone.client.checkExitAdvice();
              setAdvice(translateSecurityKey(a.message));
            } catch {
              setAdvice(
                l10n.t("security.the_local_account_unfinished_registration_and_security_operations_will"),
              );
            } finally {
              standalone.dispose();
            }
            setError("");
            setAsking(true);
          })();
        }}
      >
        {l10n.t("security.exit_and_start_over")}
      </button>
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
            const standalone = createStandaloneSecurityClient();
            try {
              try {
                await standalone.client.bootstrap();
              } catch {
                // Local security storage may be corrupt or missing private keys; allow clearing anyway.
              }
              await standalone.client.clearSecurity();
              setAsking(false);
              location.hash = "";
              onDone();
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e));
            } finally {
              standalone.dispose();
            }
          }}
        />
      )}
    </>
  );
}
