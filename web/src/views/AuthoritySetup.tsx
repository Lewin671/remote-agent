import * as l10n from "../i18n";
import { ChevronRight } from "../icons";
import { Suspense, useState } from "react";
import { Startup } from "../Startup";
import { page } from "./pages";
import { BROWSER_AUTHORITY_NOTICE } from "../core/security-client";
import {
  CreateAccountStep,
  OnboardingMode,
  OnboardingRecoveryActionKind,
  SecurityBannerTone,
  SecurityRecoveryPhase,
  useOnboardingView,
  type CoreHost,
} from "../core/host";
import { renderText } from "../core/text";
import { Banner, NavBar } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { SetupReset } from "../SetupReset";

// Pairing and curve code are needed only after choosing to join.
const Setup = page(() => import("./Pairing").then((m) => ({ default: m.Setup })));

export function AccountEntry({ host }: { host: CoreHost }) {
  const vm = useOnboardingView(host);
  // Secret inputs stay in shell local state and are passed to Core only on submit.
  const [token, setToken] = useState("");
  const [oldCode, setOldCode] = useState("");

  const repairBanner = vm.showLegacyRepairBanner && (
    <Banner
      kind="warn"
      action={l10n.t("common.got_it")}
      onAction={() => host.appRootDismissUpgradeNotice()}
    >
      <strong>{l10n.t("security.upgrade_repair_title")}</strong>{" "}
      {l10n.t("security.upgrade_repair_notice")}
    </Banner>
  );

  if (vm.mode === OnboardingMode.JOIN) {
    return (
      <>
        <NavBar
          onBack={vm.canBack ? () => host.onboardingSetMode(OnboardingMode.ENTRY) : undefined}
          title={l10n.t("security.join_an_existing_account")}
        />
        <Suspense fallback={<Startup />}>
          <Setup host={host} />
        </Suspense>
      </>
    );
  }

  if (vm.mode === OnboardingMode.ENTRY || vm.mode === OnboardingMode.UNSPECIFIED) {
    return (
      <div className="setup authority-setup account-entry">
        <div className="entry-hero">
          <div className="appmark" aria-hidden="true">
            <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.3" strokeLinecap="round" strokeLinejoin="round">
              <path d="m6.2 7.2 5.4 4.8-5.4 4.8" />
              <path d="M13.2 16.8h4.8" />
            </svg>
          </div>
          <h3 className="hl">remote-agent</h3>
          <p className="hl-sub">{l10n.t("security.control_ai_coding_clis_on_your_computer_from_any")}</p>
        </div>
        {repairBanner}
        <div className="entry-actions">
          <div className="grp">
            <button
              className="row entry-row"
              aria-label={l10n.t("security.create_account")}
              onClick={() => host.onboardingSetMode(OnboardingMode.CREATE)}
            >
              <span className="row-name">
                {l10n.t("security.create_account")}
                <small>{l10n.t("security.this_device_becomes_the_managing_client")}</small>
              </span>
              <span className="v"><ChevronRight /></span>
            </button>
            <button
              className="row entry-row"
              aria-label={l10n.t("security.join_an_existing_account")}
              onClick={() => host.onboardingSetMode(OnboardingMode.JOIN)}
            >
              <span className="row-name">
                {l10n.t("security.join_an_existing_account")}
                <small>{l10n.t("security.join_with_a_code_from_the_managing_client")}</small>
              </span>
              <span className="v"><ChevronRight /></span>
            </button>
            <button
              className="row entry-row"
              aria-label={l10n.t("security.recover_management_with_a_recovery_code")}
              onClick={() => host.onboardingSetMode(OnboardingMode.RECOVERY)}
            >
              <span className="row-name">
                {l10n.t("security.recover_management_with_a_recovery_code")}
                <small>{l10n.t("security.use_when_the_managing_client_is_lost")}</small>
              </span>
              <span className="v"><ChevronRight /></span>
            </button>
          </div>
          <p className="gf entry-note">{l10n.t("security.entry_e2ee_note")}</p>
        </div>
      </div>
    );
  }

  const isCreate = vm.mode === OnboardingMode.CREATE;
  const createVm = vm.create;
  const recVm = vm.recovery;

  return (
    <div className="setup authority-setup">
      <NavBar
        title={
          isCreate
            ? l10n.t("security.create_account")
            : l10n.t("security.recover_management_with_a_recovery_code")
        }
        onBack={vm.canBack ? () => host.onboardingSetMode(OnboardingMode.ENTRY) : undefined}
      />
      {repairBanner}
      <Banner kind="">{BROWSER_AUTHORITY_NOTICE()}</Banner>

      {isCreate && createVm && (
        <>
          {!createVm.showSetupReset && (
            <label className="field">
              <small>{l10n.t("security.server")}</small>
              <input
                aria-label={l10n.t("security.server")}
                placeholder="https://ra.example.com"
                value={createVm.serverInput}
                onChange={(e) => host.onboardingCreateEdit({ serverInput: e.target.value.trim() })}
              />
            </label>
          )}
          {createVm.step === CreateAccountStep.MISSING_CODE_ACK && (
            <Banner kind="warn">
              {l10n.t("security.the_original_creation_draft_is_preserved_continue_with_the")}
            </Banner>
          )}
          {createVm.hasPersistenceResult && (
            <PersistenceResult persisted={createVm.storagePersisted} />
          )}
          {createVm.recoveryCode && (
            <CodeBackup
              code={createVm.recoveryCode}
              saved={createVm.codeSavedChecked}
              onSaved={(checked) => host.onboardingCreateEdit({ codeSavedChecked: checked })}
            />
          )}
          {(createVm.showSetupReset || createVm.recoveryCode) && (
            <label className="field">
              <small>{l10n.t("security.setup_token_optional_with_open_registration")}</small>
              <input
                aria-label="Setup token"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            </label>
          )}
          <button
            className="btn pri"
            disabled={!createVm.canSubmit}
            onClick={() =>
              host.onboardingCreateSubmit({
                serverInput: createVm.serverInput,
                setupToken: token,
                confirmCodeSaved: createVm.step === CreateAccountStep.BACKUP_CODE && createVm.codeSavedChecked,
              })
            }
          >
            {createVm.busy
              ? l10n.t("security.processing")
              : createVm.step === CreateAccountStep.FORM && !createVm.hasPendingCreate
                ? l10n.t("security.generate_recovery_code")
                : l10n.t("security.continue_creation")}
          </button>
          {createVm.banner?.visible && (
            <Banner kind={createVm.banner.tone === SecurityBannerTone.WARN ? "warn" : "err"}>
              {renderText(createVm.banner.text)}
            </Banner>
          )}
          {createVm.showSetupReset && <SetupReset host={host} />}
        </>
      )}
      {!isCreate && recVm && (
        <>
          {recVm.phase === SecurityRecoveryPhase.FORM && (
            <label className="field">
              <small>{l10n.t("security.server")}</small>
              <input
                aria-label={l10n.t("security.server")}
                placeholder="https://ra.example.com"
                value={recVm.serverInput}
                onChange={(e) => host.onboardingRecoveryEdit({ serverInput: e.target.value.trim() })}
              />
            </label>
          )}
          {recVm.hasPersistenceResult && (
            <PersistenceResult persisted={recVm.storagePersisted} />
          )}
          <p className="txt">{l10n.t("security.if_this_browser_is_already_a_member_recovery_will")}</p>

          {recVm.phase === SecurityRecoveryPhase.FORM && (
            <>
              <p className="txt">
                {renderText(recVm.statusHintText) || l10n.t("security.after_submission_wait_48_hours_the_current_managing_client")}
              </p>
              <textarea
                className="field"
                aria-label={l10n.t("security.original_recovery_code")}
                placeholder={l10n.t("security.original_recovery_code")}
                value={oldCode}
                onChange={(e) => setOldCode(e.target.value)}
              />
              <button
                className="btn pri"
                disabled={!recVm.canPrepare || !oldCode.trim()}
                onClick={() => {
                  const codeToSend = oldCode;
                  setOldCode("");
                  host.onboardingRecoveryAction(OnboardingRecoveryActionKind.PREPARE, {
                    serverInput: recVm.serverInput,
                    oldCode: codeToSend,
                  });
                }}
              >
                {recVm.busy ? l10n.t("security.processing") : l10n.t("security.prepare_recovery")}
              </button>
            </>
          )}

          {recVm.phase !== SecurityRecoveryPhase.FORM && (
            <>
              {recVm.newRecoveryCode ? (
                <CodeBackup
                  code={recVm.newRecoveryCode}
                  saved={recVm.codeSavedChecked}
                  onSaved={(checked) => host.onboardingRecoveryEdit({ codeSavedChecked: checked })}
                />
              ) : (
                <p className="txt">
                  {l10n.t("security.the_original_recovery_draft_is_preserved_the_new_recovery")}
                </p>
              )}

              {recVm.canConfirmCodeSaved && (
                <button
                  className="btn"
                  disabled={recVm.busy}
                  onClick={() =>
                    host.onboardingRecoveryAction(OnboardingRecoveryActionKind.CONFIRM_CODE_SAVED)
                  }
                >
                  {l10n.t("security.i_saved_the_new_recovery_code_offline")}
                </button>
              )}

              {recVm.phase !== SecurityRecoveryPhase.UNSIGNED_DRAFT && renderText(recVm.statusHintText) && (
                <Banner kind={recVm.phase === SecurityRecoveryPhase.REJECTED ? "err" : "warn"}>
                  {renderText(recVm.statusHintText)}
                </Banner>
              )}

              {recVm.phase === SecurityRecoveryPhase.UNSIGNED_DRAFT && (
                <>
                  <textarea
                    className="field"
                    aria-label={l10n.t("security.original_recovery_code")}
                    placeholder={l10n.t("security.original_recovery_code")}
                    value={oldCode}
                    onChange={(e) => setOldCode(e.target.value)}
                  />
                  <button
                    className="btn pri"
                    disabled={!recVm.canContinueSigning || !oldCode.trim()}
                    onClick={() => {
                      const codeToSend = oldCode;
                      setOldCode("");
                      host.onboardingRecoveryAction(OnboardingRecoveryActionKind.CONTINUE_SIGNING, {
                        oldCode: codeToSend,
                      });
                    }}
                  >
                    {l10n.t("security.continue_original_draft")}
                  </button>
                  {recVm.canDiscard && (
                    <button
                      className="btn"
                      disabled={recVm.busy}
                      onClick={() =>
                        host.onboardingRecoveryAction(OnboardingRecoveryActionKind.DISCARD_UNSIGNED)
                      }
                    >
                      {l10n.t("security.abandon_unsigned_draft")}
                    </button>
                  )}
                </>
              )}

              {recVm.phase === SecurityRecoveryPhase.SUBMITTED_WAITING && (
                <button
                  className="btn pri"
                  disabled={!recVm.canSubmitOrRefresh || (!!recVm.newRecoveryCode && !recVm.codeSavedChecked)}
                  onClick={() => {
                    if (recVm.canConfirmCodeSaved) {
                      host.onboardingRecoveryAction(OnboardingRecoveryActionKind.CONFIRM_CODE_SAVED);
                    }
                    host.onboardingRecoveryAction(OnboardingRecoveryActionKind.REFRESH_OR_SUBMIT);
                  }}
                >
                  {recVm.busy ? l10n.t("security.processing") : l10n.t("security.submit_or_check_original_recovery")}
                </button>
              )}

              {recVm.phase === SecurityRecoveryPhase.READY_TO_COMPLETE && (
                <button
                  className="btn pri"
                  disabled={!recVm.canComplete || (!!recVm.newRecoveryCode && !recVm.codeSavedChecked)}
                  onClick={() => {
                    if (recVm.canConfirmCodeSaved) {
                      host.onboardingRecoveryAction(OnboardingRecoveryActionKind.CONFIRM_CODE_SAVED);
                    }
                    host.onboardingRecoveryAction(OnboardingRecoveryActionKind.COMPLETE);
                  }}
                >
                  {recVm.busy ? l10n.t("security.processing") : l10n.t("security.complete_recovery")}
                </button>
              )}

              {recVm.phase === SecurityRecoveryPhase.REJECTED && recVm.canDiscard && (
                <button
                  className="btn"
                  disabled={recVm.busy}
                  onClick={() =>
                    host.onboardingRecoveryAction(OnboardingRecoveryActionKind.DISCARD_REJECTED)
                  }
                >
                  {l10n.t("security.apply_again")}
                </button>
              )}
            </>
          )}

          {recVm.banner?.visible && (
            <Banner kind={recVm.banner.tone === SecurityBannerTone.WARN ? "warn" : "err"}>
              {renderText(recVm.banner.text)}
            </Banner>
          )}
          {recVm.showSetupReset && <SetupReset host={host} />}
        </>
      )}
    </div>
  );
}
