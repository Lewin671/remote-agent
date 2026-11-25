import * as l10n from "./i18n";
import {
  ExitConfirmStep,
  ExitIntentKind,
  useExitConfirmView,
  type CoreHost,
} from "./core/host";
import { renderText as fmt } from "./core/text";
import { Alert, Banner } from "./ui";

/** The retry path retains uncertain work; only this explicit confirmation discards it. */
export function SetupReset({ host }: { host: CoreHost }) {
  const vm = useExitConfirmView(host);
  const active = vm.intent === ExitIntentKind.SETUP_RESET && vm.step !== ExitConfirmStep.CLOSED;
  const err = active ? fmt(vm.errorText) : "";
  return (
    <>
      {err && <Banner kind="err">{err}</Banner>}
      <button
        className="link mid"
        disabled={active && vm.busy}
        onClick={() => host.exitRequest(ExitIntentKind.SETUP_RESET)}
      >
        {l10n.t("security.exit_and_start_over")}
      </button>
      {active && (
        <Alert
          title={l10n.t("security.exit_and_delete_local_keys")}
          text={
            err ||
            fmt(vm.warningBody) ||
            l10n.t("security.the_local_account_unfinished_registration_and_security_operations_will")
          }
          ok={vm.busy ? l10n.t("security.exiting") : l10n.t("security.confirm_exit")}
          danger
          onCancel={() => host.exitConfirm(false)}
          onOk={() => {
            if (!vm.busy) host.exitConfirm(true);
          }}
        />
      )}
    </>
  );
}
