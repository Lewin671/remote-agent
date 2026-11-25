import * as l10n from "./i18n";
import {
  ExitConfirmStep,
  ExitIntentKind,
  useAppRootView,
  useExitConfirmView,
  type CoreHost,
} from "./core/host";
import { renderText } from "./core/text";
import { Alert, Banner } from "./ui";

/** Rejoin keeps only the server; account keys and cached ciphertext are erased. */
export function RejoinButton({
  host,
  server,
  children,
}: {
  host: CoreHost;
  server: string;
  children: string;
}) {
  const vm = useExitConfirmView(host);
  const active = vm.intent === ExitIntentKind.REJOIN_PAIRING && vm.step !== ExitConfirmStep.CLOSED;
  const showDialog =
    active &&
    (vm.step === ExitConfirmStep.AWAITING_CONFIRM ||
      vm.step === ExitConfirmStep.EXECUTING ||
      vm.step === ExitConfirmStep.FAILED);
  const err = active ? renderText(vm.errorText) : "";
  const warningText = renderText(vm.warningBody);
  const dialogText =
    err ||
    (vm.showRejoinDeletesCacheNote && warningText
      ? `${warningText} ${l10n.t("security.this_deletes_the_account_keys_and_cache_on_this")}`
      : warningText || l10n.t("security.this_deletes_the_account_keys_and_cache_on_this"));
  return (
    <>
      <button
        className="btn pri"
        disabled={active && vm.busy}
        onClick={() => host.exitRequest(ExitIntentKind.REJOIN_PAIRING, server)}
      >
        {children}
      </button>
      {err && <Banner kind="err">{err}</Banner>}
      {showDialog && (
        <Alert
          title={l10n.t("security.delete_local_keys_and_rejoin")}
          text={dialogText}
          ok={vm.busy ? l10n.t("security.exiting") : l10n.t("security.rejoin")}
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

export function RetainedIdentityDeniedScreen({ host }: { host: CoreHost }) {
  const root = useAppRootView(host);
  const view = root.retainedDenied;
  const server = view?.server || root.server;
  return (
    <div className="center">
      <div className="gone">
        {view?.showLegacyRepairBanner && (
          <Banner
            kind="warn"
            action={l10n.t("common.got_it")}
            onAction={() => host.appRootDismissUpgradeNotice()}
          >
            <strong>{l10n.t("security.upgrade_repair_title")}</strong>{" "}
            {l10n.t("security.upgrade_repair_notice")}
          </Banner>
        )}
        <div className="et">
          {l10n.t("security.the_server_does_not_recognize_this_device")}
        </div>
        <p className="muted small">
          {l10n.t("security.it_may_have_been_removed_or_the_service_may")}
        </p>
        <RejoinButton host={host} server={server}>
          {l10n.t("security.rejoin_with_a_pairing_code")}
        </RejoinButton>
        <p className="muted small">
          {l10n.t("security.the_account_keys_and_cache_in_this_browser_will")}
        </p>
      </div>
    </div>
  );
}
