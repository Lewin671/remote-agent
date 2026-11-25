import * as l10n from "../i18n";
import { useEffect } from "react";
import { SetupReset } from "../SetupReset";
import {
  JoinPairingStep,
  OnboardingMode,
  SecurityBannerAction,
  SecurityBannerTone,
  useOnboardingView,
  type CoreHost,
} from "../core/host";
import { renderText } from "../core/text";
import { Banner } from "../ui";
import { LoaderCircle } from "../icons";

export const joinLead = () => l10n.t("security.on_the_managing_client_open_settings_add_device_and");
export const joinExistingLead = () => l10n.t("security.the_original_device_identity_keys_and_history_are_preserved");

/** Joins this browser with explicit approval from an enrolled device. */
export function Setup({
  host,
  existingServer,
}: {
  host: CoreHost;
  existingServer?: string;
}) {
  const ob = useOnboardingView(host);
  const vm = ob.join;

  useEffect(() => {
    if (existingServer && !vm?.isExistingRepair) {
      host.onboardingSetMode(OnboardingMode.JOIN, {
        existingRepair: true,
        existingServer,
      });
    }
  }, [host, existingServer, vm?.isExistingRepair]);

  useEffect(() => {
    return () => {
      const cur = host.viewOnboarding().join;
      if (
        cur &&
        cur.step !== JoinPairingStep.FORM &&
        cur.canCancel &&
        !cur.pendingRegistration
      ) {
        host.onboardingJoinCancel();
      }
    };
  }, [host]);

  const bannerView = vm?.banner?.visible ? vm.banner : undefined;
  const bannerActionLabel =
    bannerView?.action === SecurityBannerAction.RETRY
      ? l10n.t("common.retry")
      : bannerView?.action === SecurityBannerAction.REFILL
        ? l10n.t("security.re_enter")
        : bannerView?.action === SecurityBannerAction.RESTART
          ? l10n.t("security.start_over")
          : undefined;
  const banner = bannerView ? (
    <Banner
      kind={bannerView.tone === SecurityBannerTone.WARN ? "warn" : "err"}
      action={bannerActionLabel}
      onAction={bannerActionLabel ? () => host.onboardingJoinBannerAction() : undefined}
    >
      {renderText(bannerView.text)}
    </Banner>
  ) : null;

  if (
    vm &&
    (vm.step === JoinPairingStep.COMPARE_SAS ||
      vm.step === JoinPairingStep.WAITING_APPROVAL) &&
    vm.sasEmoji.length > 0
  ) {
    const isCompare = vm.step === JoinPairingStep.COMPARE_SAS;
    return (
      <div className="setup">
        <h3 className="hl">{l10n.t("security.compare_patterns")}</h3>
        <div className="emoji">{vm.sasEmoji.join(" ")}</div>
        <p className="txt center-text">
          {l10n.t("security.compare_the_managing_client_s_4_patterns_if_they")}
        </p>
        {isCompare && (
          <button
            className="btn pri"
            disabled={!vm.canConfirmSas}
            onClick={() => host.onboardingJoinConfirmSas()}
          >
            {l10n.t("security.patterns_match_trust_and_continue")}
          </button>
        )}
        <p className="wait-line">
          <LoaderCircle />
          {isCompare
            ? l10n.t("security.confirm_the_patterns_on_this_device_first")
            : l10n.t("security.confirmed_here_waiting_for_managing_client_approval_and_binding")}
        </p>
        {banner}
        <div className="pair-wait-actions">
          <button
            className="btn"
            disabled={!vm.canCancel}
            onClick={() => host.onboardingJoinCancel()}
          >
            {l10n.t("security.patterns_differ_return_to_code_entry")}
          </button>
          <p className="gf center-text">
            {l10n.t("security.returning_stops_this_wait_reject_the_old_request_on")}
          </p>
        </div>
        {vm.showSetupReset && <SetupReset host={host} />}
      </div>
    );
  }

  return (
    <div className="setup">
      <h3 className="hl">
        {vm?.isExistingRepair
          ? l10n.t("security.one_time_security_setup")
          : l10n.t("security.join_account")}
      </h3>
      <p className="txt lead">
        {vm?.isExistingRepair ? joinExistingLead() : joinLead()}
      </p>
      <input
        className="field code-in"
        inputMode="numeric"
        autoComplete="one-time-code"
        placeholder="0000 0000"
        value={vm?.formattedCode ?? ""}
        onChange={(e) => host.onboardingJoinEdit({ codeInput: e.target.value })}
        aria-label={l10n.t("security.pairing_code")}
      />
      <label className="field">
        <small>{l10n.t("security.server")}</small>
        <input
          placeholder="https://ra.example.com"
          value={vm?.serverInput ?? ""}
          readOnly={vm ? !vm.serverEditable : false}
          onChange={(e) => host.onboardingJoinEdit({ serverInput: e.target.value.trim() })}
          aria-label={l10n.t("security.server")}
        />
      </label>
      {vm?.step === JoinPairingStep.WORKING && (
        <p className="wait-line">
          <LoaderCircle />
          {l10n.t("security.processing")}
        </p>
      )}
      {banner}
      <div className="setup-go">
        <button
          className="btn pri"
          disabled={!vm?.canSubmit}
          onClick={() => host.onboardingJoinSubmit()}
        >
          {vm?.busy ? l10n.t("security.processing") : l10n.t("security.join")}
        </button>
      </div>
      {vm?.showSetupReset && <SetupReset host={host} />}
    </div>
  );
}
