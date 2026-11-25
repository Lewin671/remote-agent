import * as l10n from "../i18n";
import { ChevronRight } from "../icons";
import { useEffect, useState } from "react";
import {
  AccountSecurityActionKind,
  AccountSecuritySubpage,
  ExitConfirmStep,
  ExitIntentKind,
  getCoreHost,
  HandoffActionKind,
  HandoffRole,
  PendingSecurityOpKind,
  SecurityBannerTone,
  useAccountSecurityView,
  useCoreHost,
  useExitConfirmView,
  useHandoffView,
  type CoreHost,
} from "../core/host";
import { renderText as fmt } from "../core/text";
import { BROWSER_AUTHORITY_NOTICE } from "../core/security-client";
import type { Remote } from "../remote";
import { Alert, Banner, NavBar } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";

export {
  ManagementEntry,
  PairCodePage,
  DeviceActions,
} from "./DeviceManagement";

export function LeaveAlert({
  remote,
  host: suppliedHost,
  onCancel,
}: {
  remote?: Remote;
  host?: CoreHost;
  onCancel: () => void;
}) {
  const host = suppliedHost ?? (remote ? getCoreHost(remote) : undefined);
  if (!host) return null;
  return <LeaveAlertInner host={host} server={remote?.account.server ?? ""} onCancel={onCancel} />;
}

function LeaveAlertInner({
  host,
  server,
  onCancel,
}: {
  host: CoreHost;
  server: string;
  onCancel: () => void;
}) {
  const vm = useExitConfirmView(host);
  useEffect(() => {
    host.exitRequest(ExitIntentKind.LEAVE_ACCOUNT, server);
    return () => {
      const cur = host.viewExitConfirm();
      if (cur.intent === ExitIntentKind.LEAVE_ACCOUNT && cur.step !== ExitConfirmStep.EXECUTING) {
        host.exitConfirm(false);
      }
    };
  }, [host, server]);

  return (
    <Alert
      title={l10n.t("security.exit_this_browser")}
      text={
        fmt(vm.errorText) ||
        fmt(vm.warningBody) ||
        l10n.t("security.verifying_management_identity_and_unfinished_security_operations")
      }
      ok={vm.busy ? l10n.t("security.exiting") : l10n.t("security.exit")}
      danger
      onCancel={() => {
        host.exitConfirm(false);
        onCancel();
      }}
      onOk={() => {
        if (!vm.busy && vm.step === ExitConfirmStep.AWAITING_CONFIRM) {
          host.exitConfirm(true);
        }
      }}
    />
  );
}

export function SecurityPage({
  remote,
  onBack,
}: {
  remote: Remote;
  onBack: () => void;
}) {
  const host = useCoreHost(remote);
  const vm = useAccountSecurityView(host);
  const handoff = useHandoffView(host);
  const [showHandoff, setShowHandoff] = useState(false);
  const [abandonConfirm, setAbandonConfirm] = useState(false);
  // Secret input stays in shell local state and is passed on submit.
  const [profileCode, setProfileCode] = useState("");

  useEffect(() => {
    host.accountSecurityOpen(AccountSecuritySubpage.OVERVIEW);
    return () => host.accountSecurityClose();
  }, [host]);

  const openHandoff = () => {
    setShowHandoff(true);
    host.handoffAction(HandoffActionKind.OPEN);
  };

  const navTitle = showHandoff
    ? vm.isManager
      ? l10n.t("security.transfer_management")
      : l10n.t("security.take_over_management")
    : vm.subpage === AccountSecuritySubpage.ROTATE_RECOVERY
      ? l10n.t("security.recovery_code")
      : vm.subpage === AccountSecuritySubpage.PENDING_OP
        ? l10n.t("security.unfinished_security_operation")
        : l10n.t("security.account_security");

  const handleBack = () => {
    if (showHandoff) {
      setShowHandoff(false);
      return;
    }
    if (vm.subpage !== AccountSecuritySubpage.OVERVIEW) {
      host.accountSecurityAction(AccountSecurityActionKind.SET_SUBPAGE, {
        subpage: AccountSecuritySubpage.OVERVIEW,
      });
      return;
    }
    onBack();
  };

  const errBanner = showHandoff
    ? handoff.errorBanner?.visible ? handoff.errorBanner : undefined
    : vm.errorBanner?.visible ? vm.errorBanner : undefined;
  const bodyClass = "body security-body";

  return (
    <div className="page set" data-route="account-security">
      {abandonConfirm && (
        <Alert
          title={l10n.t("security.abandon_this_succession_preparation")}
          text={l10n.t("security.the_public_key_may_already_be_published_abandoning_only")}
          ok={l10n.t("new_session.android_abandon")}
          danger
          onCancel={() => setAbandonConfirm(false)}
          onOk={() => {
            setAbandonConfirm(false);
            host.accountSecurityAction(AccountSecurityActionKind.DISCARD_PENDING, { flag: true });
          }}
        />
      )}
      <NavBar title={navTitle} onBack={handleBack} />
      <div className={bodyClass}>
        <div className="set-banners">
          {vm.recoveryPending && (
            <Banner
              kind="warn"
              action={vm.canVetoRecovery ? l10n.t("security.reject") : undefined}
              onAction={
                vm.canVetoRecovery
                  ? () => {
                      setShowHandoff(false);
                      host.accountSecurityAction(AccountSecurityActionKind.SET_SUBPAGE, {
                        subpage: AccountSecuritySubpage.ROTATE_RECOVERY,
                      });
                    }
                  : undefined
              }
            >
              {fmt(vm.recoveryTakeoverBanner)}
            </Banner>
          )}
          {vm.recoveryStatusStale && (
            <Banner kind="warn">{l10n.t("security.security_state_may_be_out_of_date_connect_and")}</Banner>
          )}
          {vm.takingOver && (
            <Banner kind="warn">{l10n.t("security.completing_takeover_keep_this_page_in_the_foreground_and")}</Banner>
          )}
          {vm.needsRotationBanner && (
            <Banner kind="warn">{l10n.t("security.the_new_recovery_code_backup_is_unconfirmed_replace_the")}</Banner>
          )}
          {fmt(vm.pendingRemoveBanner) && (
            <Banner
              kind="warn"
              action={l10n.t("common.retry")}
              onAction={() => host.accountSecurityAction(AccountSecurityActionKind.RETRY_PENDING_REMOVE)}
            >
              {fmt(vm.pendingRemoveBanner)}
            </Banner>
          )}
          {fmt(vm.authorityErrorBanner) && (
            <Banner kind="err">{fmt(vm.authorityErrorBanner)}</Banner>
          )}
        </div>

        {!showHandoff && vm.subpage === AccountSecuritySubpage.OVERVIEW && (
          <>
            {vm.hasPendingOp && (
              <div className="grp">
                <button
                  className="row"
                  onClick={() =>
                    host.accountSecurityAction(AccountSecurityActionKind.SET_SUBPAGE, {
                      subpage: AccountSecuritySubpage.PENDING_OP,
                    })
                  }
                >
                  {l10n.t("security.unfinished_security_operation")}
                  <ChevronRight className="chev" />
                </button>
              </div>
            )}
            <div className="grp">
              <div className="row">
                {l10n.t("security.current_managing_client")}
                <span className="v">{fmt(vm.managerDisplay)}</span>
              </div>
              <div className="row">
                {l10n.t("security.remote_control_expiry")}
                <span className="v">{fmt(vm.leaseExpiryText)}</span>
              </div>
              {vm.isManager && (
                <button
                  className="row"
                  disabled={!vm.canRenewNow}
                  onClick={() => host.accountSecurityAction(AccountSecurityActionKind.RENEW_LEASE)}
                >
                  {l10n.t("security.renew_now")}
                </button>
              )}
            </div>
            <p className="gf">
              {!vm.isManager && fmt(vm.leaseRenewalAdvice)
                ? fmt(vm.leaseRenewalAdvice)
                : l10n.t("security.automatically_renews_when_you_return_to_the_managing_client")}
            </p>
            <div className="grp">
              {vm.canOpenRecovery && (
                <button
                  className="row"
                  onClick={() =>
                    host.accountSecurityAction(AccountSecurityActionKind.SET_SUBPAGE, {
                      subpage: AccountSecuritySubpage.ROTATE_RECOVERY,
                    })
                  }
                >
                  {l10n.t("security.recovery_code")}
                  <ChevronRight className="chev" />
                </button>
              )}
              {vm.canOpenHandoff && (
                <button className="row" onClick={openHandoff}>
                  {vm.isManager
                    ? l10n.t("security.transfer_management")
                    : l10n.t("security.take_over_management")}
                  <ChevronRight className="chev" />
                </button>
              )}
            </div>
          </>
        )}

        {!showHandoff && vm.subpage === AccountSecuritySubpage.PENDING_OP && (
          <div className="grp">
            <div className="row help">
              {l10n.t("security.a_security_operation_is_unfinished_continue_the_original_operation")}
            </div>
            <button
              className="row"
              disabled={!vm.canContinuePending}
              onClick={() => host.accountSecurityAction(AccountSecurityActionKind.CONTINUE_PENDING)}
            >
              {l10n.t("security.verify_and_continue")}
            </button>
            {vm.canDiscardPending && (
              <button
                className="row danger"
                disabled={vm.busy}
                onClick={() => {
                  if (vm.discardRequiresConfirm) {
                    setAbandonConfirm(true);
                  } else {
                    host.accountSecurityAction(AccountSecurityActionKind.DISCARD_PENDING, { flag: false });
                  }
                }}
              >
                {vm.pendingKind === PendingSecurityOpKind.HANDOFF_OFFER
                  ? l10n.t("security.abandon_this_succession_preparation_security")
                  : vm.pendingKind === PendingSecurityOpKind.RECOVERY_ROTATION
                    ? l10n.t("security.abandon_unpublished_recovery_code")
                    : l10n.t("security.abandon_unsigned_operation")}
              </button>
            )}
          </div>
        )}

        {!showHandoff && vm.subpage === AccountSecuritySubpage.ROTATE_RECOVERY && (
          <>
            {vm.preparedCode ? (
              <>
                <CodeBackup
                  code={vm.preparedCode}
                  saved={vm.codeSavedChecked}
                  onSaved={(checked) =>
                    host.accountSecurityAction(AccountSecurityActionKind.SET_ROTATION_SAVED, { flag: checked })
                  }
                />
                <button
                  className="btn pri"
                  disabled={!vm.canPublishRotation}
                  onClick={() => host.accountSecurityAction(AccountSecurityActionKind.PUBLISH_ROTATION)}
                >
                  {vm.busy ? l10n.t("security.publishing") : l10n.t("security.saved_publish_replacement")}
                </button>
                <p className="gf">
                  {l10n.t("security.nothing_is_published_before_you_save_it_an_unpublished")}
                </p>
              </>
            ) : (
              <>
                <p className="txt">
                  {l10n.t("security.replacing_the_recovery_code_immediately_invalidates_the_old_code")}
                </p>
                <button
                  className="btn pri"
                  disabled={!vm.canPrepareRotation}
                  onClick={() => host.accountSecurityAction(AccountSecurityActionKind.PREPARE_ROTATION)}
                >
                  {l10n.t("security.generate_new_recovery_code")}
                </button>
              </>
            )}
          </>
        )}

        {showHandoff && handoff.role === HandoffRole.OUTGOING_MANAGER && (
          <>
            <p className="txt">{l10n.t("security.once_the_successor_device_joins_and_confirms_saving_its")}</p>
            <div className="grp">
              <button
                className="row"
                disabled={!handoff.canRefresh}
                onClick={() => host.handoffAction(HandoffActionKind.REFRESH_CANDIDATES)}
              >
                {l10n.t("security.refresh_successor_devices")}
              </button>
              {handoff.candidates.length === 0 && (
                <div className="row help">{l10n.t("security.waiting_for_the_successor_device_to_generate_and_save")}</div>
              )}
              {handoff.candidates.map((c) => (
                <button
                  key={c.deviceId}
                  className="row"
                  disabled={!handoff.canTransfer}
                  onClick={() =>
                    host.handoffAction(HandoffActionKind.REQUEST_TRANSFER, { targetDeviceId: c.deviceId })
                  }
                >
                  {l10n.t("security.transfer_to")} {fmt(c.displayName)}
                </button>
              ))}
            </div>
          </>
        )}

        {showHandoff && handoff.role === HandoffRole.UNAVAILABLE && (
          <p className="txt">{l10n.t("security.computers_can_only_run_clis_and_cannot_take_over")}</p>
        )}

        {showHandoff && handoff.role === HandoffRole.INCOMING_CANDIDATE && (
          <>
            <Banner kind="">{BROWSER_AUTHORITY_NOTICE()}</Banner>
            {handoff.hasPersistenceResult && (
              <PersistenceResult persisted={handoff.storagePersisted} />
            )}
            {handoff.needsPublicProfileRestore && (
              <>
                <div className="label">{l10n.t("security.restore_account_public_key")}</div>
                <p className="txt">
                  {l10n.t("security.before_succession_pair_again_afterward_enter_the_new_recovery")}
                </p>
                <button
                  className="btn"
                  disabled={handoff.busy}
                  onClick={() => host.exitRequest(ExitIntentKind.REJOIN_PAIRING, "")}
                >
                  {l10n.t("security.pair_again")}
                </button>
                <textarea
                  className="field"
                  aria-label={l10n.t("security.recovery_code_for_restoring_public_information")}
                  value={profileCode}
                  onChange={(e) => setProfileCode(e.target.value)}
                />
                <button
                  className="btn pri"
                  disabled={handoff.busy || !profileCode.trim()}
                  onClick={() => {
                    const c = profileCode;
                    setProfileCode("");
                    host.handoffAction(HandoffActionKind.RESTORE_PUBLIC_PROFILE, { recoveryCode: c });
                  }}
                >
                  {l10n.t("security.restore_public_information")}
                </button>
              </>
            )}
            {!handoff.needsPublicProfileRestore && !!handoff.preparedCode && (
              <>
                <CodeBackup
                  code={handoff.preparedCode}
                  saved={handoff.codeSavedChecked}
                  onSaved={(checked) =>
                    host.handoffAction(HandoffActionKind.SET_CODE_SAVED, { flag: checked })
                  }
                />
                <button
                  className="btn pri"
                  disabled={!handoff.canPublishOffer}
                  onClick={() => host.handoffAction(HandoffActionKind.PUBLISH_OFFER)}
                >
                  {handoff.busy ? l10n.t("security.publishing") : l10n.t("security.saved_notify_managing_client")}
                </button>
                <p className="gf">
                  {l10n.t("security.nothing_is_published_before_you_save_it_an_unpublished")}
                </p>
              </>
            )}
            {!handoff.needsPublicProfileRestore && !handoff.preparedCode && handoff.hasPublishedOffer && (
              <Banner kind="">
                {l10n.t("security.recovery_code_saved_and_confirmed_waiting_for_the_current")}
              </Banner>
            )}
            {!handoff.needsPublicProfileRestore && !handoff.preparedCode && !handoff.hasPublishedOffer && (
              <button
                className="btn pri"
                disabled={!handoff.canPrepareCode}
                onClick={() => host.handoffAction(HandoffActionKind.PREPARE_CODE)}
              >
                {l10n.t("security.generate_new_recovery_code")}
              </button>
            )}
          </>
        )}

        <div className="set-banners">
          {!showHandoff && fmt(vm.successNotice) && (
            <Banner kind="">{fmt(vm.successNotice)}</Banner>
          )}
          {showHandoff && fmt(handoff.messageBanner) && (
            <Banner kind="">{fmt(handoff.messageBanner)}</Banner>
          )}
          {errBanner && (
            <Banner kind={errBanner.tone === SecurityBannerTone.WARN ? "warn" : "err"}>
              {fmt(errBanner.text)}
            </Banner>
          )}
        </div>
      </div>

      {handoff.confirmingTargetId && (
        <Alert
          title={l10n.t("security.transfer_management")}
          text={fmt(handoff.confirmDialogBody)}
          ok={l10n.t("security.confirm_transfer")}
          danger
          onCancel={() => host.handoffAction(HandoffActionKind.CANCEL_TRANSFER)}
          onOk={() =>
            host.handoffAction(HandoffActionKind.CONFIRM_TRANSFER, {
              targetDeviceId: handoff.confirmingTargetId,
            })
          }
        />
      )}
    </div>
  );
}
