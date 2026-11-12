import * as l10n from "../i18n";
import { ChevronRight, Lock } from "../icons";
import { futureTime } from "../model";
import { useEffect, useState } from "react";
import {
  BROWSER_AUTHORITY_NOTICE,
  authorityExitAdvice,
  type SecurityOperation,
} from "../security-operation";
import { clientName } from "../devices";
import { DeviceKind } from "../gen/remoteagent/v1/relay_pb";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar, useRemote, useNow, usePageVisible } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { forgetAccount } from "../storage";
import type { HandoffOffer } from "../authority-client";
import { equal } from "../device-security";

import { authorityName } from "./authority-notice";
export {
  ManagementEntry,
  PairCodePage,
  DeviceActions,
} from "./DeviceManagement";

export function SecurityGroup({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  return (
    <>
      <div className="label">{l10n.t("security.account_security")}</div>
      <div className="grp">
        <button className="row" onClick={() => go("settings/security")}>
          <span className="ico">
            <Lock />
          </span>
          {l10n.t("security.account_security")}<span className="v">{remote.isAuthority ? l10n.t("security.this_device_manages_the_account") : authorityName(remote)}</span>
          <ChevronRight className="chev" />
        </button>
      </div>
    </>
  );
}

export function LeaveAlert({ onCancel }: { onCancel: () => void }) {
  const [advice, setAdvice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void authorityExitAdvice()
      .then((a) => setAdvice(a.message))
      .catch((e) => setError(errorText(e)));
  }, []);
  return (
    <Alert
      title={l10n.t("security.exit_this_browser")}
      text={error || advice || l10n.t("security.verifying_management_identity_and_unfinished_security_operations")}
      ok={busy ? l10n.t("security.exiting") : l10n.t("security.exit")}
      danger
      onCancel={onCancel}
      onOk={() => {
        if (!advice || busy) return;
        setBusy(true);
        void forgetAccount()
          .then(() => {
            location.hash = "";
            location.reload();
          })
          .catch((e) => {
            setError(errorText(e));
            setBusy(false);
          });
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
  useRemote(remote);
  const visible = usePageVisible();
  const [page, setPage] = useState<"overview" | "recovery" | "handoff" | "pending">("overview");
  const now = useNow(30_000);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [code, setCode] = useState(""),
    [saved, setSaved] = useState(false);
  const [persisted, setPersisted] = useState<boolean>();
  const [lease, setLease] = useState<bigint | null>();
  const [pending, setPending] = useState<SecurityOperation>();
  const [rotation, setRotation] = useState(false);
  const [offers, setOffers] = useState<HandoffOffer[]>([]);
  const [target, setTarget] = useState("");
  const [message, setMessage] = useState("");
  const [profileCode, setProfileCode] = useState("");
  const [profileReady, setProfileReady] = useState(true);
  const [takingOver, setTakingOver] = useState(false);
  const [abandonOffer, setAbandonOffer] = useState(false);
  const refresh = async () => {
    const r = await remote.security.verifiedRecord();
    setLease(
      r.lease && equal(r.lease.head, r.stateHash)
        ? r.lease.notAfter
        : null,
    );
    setTakingOver(
      remote.isAuthority &&
        remote.devices.get(remote.account.deviceId)?.meta?.handoffRecoveryEd
          .length === 32 &&
        (!r.lease || !equal(r.lease.head, r.stateHash)),
    );
    setPending(await remote.authority.pending());
    setRotation(await remote.authority.needsRecoveryCodeRotation());
    setProfileReady(await remote.authority.hasAccountPublicProfile());
    if (remote.isAuthority) setOffers(await remote.handoffCandidates());
  };
  useEffect(() => {
    if (!visible) return;
    let current = true;
    // App resumes the transport in its parent effect. Read after those
    // effects, and ignore a read invalidated when the page goes away.
    void Promise.resolve().then(() => current ? refresh() : undefined).then(() => {
      if (current) setReadError("");
    }).catch((e) => {
      if (current && !document.hidden) setReadError(errorText(e));
    });
    return () => { current = false; };
  }, [
    remote,
    visible,
    remote.isAuthority,
    remote.recoveryPending,
    remote.authorityError,
    now,
  ]);
  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await work();
      await refresh();
    } catch (e) {
      setMessage("");
      setError(errorText(e));
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  const offered =
    remote.devices.get(remote.account.deviceId)?.meta?.handoffRecoveryEd
      .length === 32;
  const draft =
    pending?.kind === "offer" ||
    (pending?.kind === "state" &&
      (pending.local as { action?: string }).action === "recovery");
  const localClient =
    remote.devices.get(remote.account.deviceId)?.kind === DeviceKind.CLIENT;
  return (
    <div className="page set" data-route="account-security">
      {abandonOffer && <Alert title={l10n.t("security.abandon_this_succession_preparation")}
        text={l10n.t("security.the_public_key_may_already_be_published_abandoning_only")}
        ok={l10n.t("new_session.android_abandon")} danger onCancel={() => setAbandonOffer(false)} onOk={() => {
          setAbandonOffer(false);
          void run(async () => { await remote.authority.discardUnpublishedCode(); setCode(""); setSaved(false); });
        }} />}
      <NavBar title={page === "overview" ? l10n.t("security.account_security") : page === "recovery" ? l10n.t("security.recovery_code") : page === "pending" ? l10n.t("security.unfinished_security_operation") : remote.isAuthority ? l10n.t("security.transfer_management") : l10n.t("security.take_over_management")} onBack={page === "overview" ? onBack : () => setPage("overview")} />
      <div className="body security-body">
        <div className="set-banners">
        {remote.recoveryPending && (
          <Banner kind="warn" action={remote.isAuthority ? l10n.t("security.reject") : undefined} onAction={() => setPage("recovery")}>
            {l10n.t("security.someone_is_requesting_management_takeover_with_a_recovery_code", { value: String(remote.recoveryReadyMs > 0n ? futureTime(remote.recoveryReadyMs) : l10n.t("security.duration_48_hours")) })}
          </Banner>
        )}
        {remote.security.recoveryStatusStale && (
          <Banner kind="warn">{l10n.t("security.security_state_may_be_out_of_date_connect_and")}</Banner>
        )}
        {takingOver && (
          <Banner kind="warn">
            {l10n.t("security.completing_takeover_keep_this_page_in_the_foreground_and")}</Banner>
        )}
        {rotation && (
          <Banner kind="warn">
            {l10n.t("security.the_new_recovery_code_backup_is_unconfirmed_replace_the")}</Banner>
        )}
        {remote.authorityError && (
          <Banner kind="warn">{remote.authorityError}</Banner>
        )}
        </div>
        {page === "overview" && pending && (
          <div className="grp"><button className="row" onClick={() => setPage("pending")}>{l10n.t("security.unfinished_security_operation")}<ChevronRight className="chev" /></button></div>
        )}
        {page === "overview" && <>
        <div className="grp">
          <div className="row">{l10n.t("security.current_managing_client")}<span className="v">{remote.isAuthority ? l10n.t("security.this_device") : authorityName(remote)}</span></div>
          <div className="row">
            {l10n.t("security.remote_control_expiry")}<span className="v">{lease === undefined ? l10n.t("security.reading") : lease === null ? l10n.t("security.remote_control_expiry_not_received_yet") : futureTime(lease, now)}</span>
          </div>
          {remote.isAuthority && (
            <button
              className="row act"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await remote.authority.renewLeaseNow();
                  const pending = await remote.authority.pending();
                  if (pending && (pending.kind === "state" || pending.kind === "lease"))
                    throw new Error(l10n.t("security.finish_the_current_security_operation_before_renewing"));
                  setMessage(l10n.t("security.remote_control_renewed"));
                })
              }
            >
              {l10n.t("security.renew_now")}</button>
          )}
        </div>
        <p className="gf">{l10n.t("security.automatically_renews_when_you_return_to_the_managing_client")}</p>
        {!remote.isAuthority && <p className="gf">{l10n.t("security.open_to_renew", { value: String(authorityName(remote)) })}</p>}
        <div className="grp">
          {remote.isAuthority && <button className="row" onClick={() => setPage("recovery")}>{l10n.t("security.recovery_code")}<ChevronRight className="chev" /></button>}
          {(remote.isAuthority || localClient) && <button className="row" onClick={() => setPage("handoff")}>{remote.isAuthority ? l10n.t("security.transfer_management") : l10n.t("security.take_over_management")}<ChevronRight className="chev" /></button>}
        </div>
        </>}
        {page === "pending" && pending && (
          <div className="grp">
            <div className="row help">{l10n.t("security.a_security_operation_is_unfinished_continue_the_original_operation")}</div>
            <button
              className="row act"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await remote.authority.resumeSecurityOperation();
                })
              }
            >
              {l10n.t("security.verify_and_continue")}</button>
            {draft && pending.phase === "PREPARED" &&
              (pending.kind === "offer" || (!pending.signed && !pending.codeSaved)) && (
              <button
                className="row act"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    if (pending.kind === "offer") { setAbandonOffer(true); return; }
                    await remote.authority.discardUnpublishedCode();
                    setCode(""); setSaved(false);
                  })
                }
              >
                {pending.kind === "offer" ? l10n.t("security.abandon_this_succession_preparation_security") : l10n.t("security.abandon_unpublished_recovery_code")}
              </button>
            )}
            {pending.kind === "state" && !pending.signed && !draft && (
              <button
                className="row act"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await remote.authority.discardUnsignedStateDraft();
                  })
                }
              >
                {l10n.t("security.abandon_unsigned_operation")}</button>
            )}
          </div>
        )}
        {(page === "recovery" || page === "handoff") && code ? (
          <>
            <CodeBackup code={code} saved={saved} onSaved={setSaved} />
            {persisted !== undefined && (
              <PersistenceResult persisted={persisted} />
            )}
            <button
              className="btn pri"
              disabled={!saved || busy}
              onClick={() =>
                void run(async () => {
                  if (remote.isAuthority)
                    await remote.authority.publishRecovery();
                  else await remote.authority.offerHandoff();
                  if (await remote.authority.pending())
                    throw new Error(l10n.t("security.the_original_operation_is_unfinished_continue_verifying"));
                  setCode("");
                  setSaved(false);
                  setMessage(
                    remote.isAuthority
                      ? l10n.t("security.recovery_code_updated_the_previous_code_and_pending_recovery")
                      : l10n.t("security.recovery_code_saved_and_confirmed_waiting_for_the_current"),
                  );
                })
              }
            >
              {busy
                ? l10n.t("security.publishing")
                : remote.isAuthority
                  ? l10n.t("security.saved_publish_replacement")
                  : l10n.t("security.saved_notify_managing_client")}
            </button>
            <p className="gf">
              {l10n.t("security.nothing_is_published_before_you_save_it_an_unpublished")}</p>
          </>
        ) : page === "recovery" && remote.isAuthority ? (
          <>
          <p className="txt">{l10n.t("security.replacing_the_recovery_code_immediately_invalidates_the_old_code")}</p>
          <div className="grp">
            <button
              className="row act"
              disabled={busy || !!pending}
              onClick={() =>
                void run(async () => {
                  const p = await remote.authority.prepareRecovery();
                  setCode(p.code);
                  setSaved(false);
                })
              }
            >
              {l10n.t("security.generate_new_recovery_code")}</button>
          </div>
          </>
        ) : null}
        {page === "handoff" && (remote.isAuthority ? (
          <>
            <p className="txt">
              {l10n.t("security.once_the_successor_device_joins_and_confirms_saving_its")}</p>
            <div className="grp">
              {offers.map((o) => (
                <button
                  className="row"
                  key={o.id}
                  disabled={busy || !!pending}
                  onClick={() => setTarget(o.id)}
                >
                  {l10n.t("security.transfer_to")}{" "}
                  {clientName(remote.devices.get(o.id), remote.devices, o.id)}
                  <ChevronRight className="chev" />
                </button>
              ))}
              <button
                className="row act"
                disabled={busy}
                onClick={() => void run(async () => {})}
              >
                {l10n.t("security.refresh_successor_devices")}</button>
              {!offers.length && (
                <div className="row help">{l10n.t("security.waiting_for_the_successor_device_to_generate_and_save")}</div>
              )}
            </div>
          </>
        ) : localClient ? (
          <>
            <Banner>{BROWSER_AUTHORITY_NOTICE()}</Banner>
            <p className="txt">
              {l10n.t("security.save_the_new_recovery_code_first_then_ask_the")}</p>
            {offered && (
              <Banner kind="warn">
                {l10n.t("security.recovery_code_saved_and_confirmed_waiting_for_the_current")}</Banner>
            )}
            <button
              className="btn"
              disabled={busy || !!pending || !!code}
              onClick={() =>
                void run(async () => {
                  const p = await remote.authority.prepareHandoffCode();
                  setCode(p.code);
                  setPersisted(p.storagePersisted);
                  setSaved(false);
                })
              }
            >
              {offered ? l10n.t("security.regenerate_recovery_code") : l10n.t("security.generate_new_recovery_code")}
            </button>
          </>
        ) : (
          <p className="txt">{l10n.t("security.computers_can_only_run_clis_and_cannot_take_over")}</p>
        ))}
        {page === "handoff" && !profileReady && (
          <>
            <div className="label">{l10n.t("security.restore_account_public_key")}</div>
            <p className="txt">
              {l10n.t("security.before_succession_pair_again_afterward_enter_the_new_recovery")}</p>
            <button
              className="btn"
              disabled={busy}
              onClick={() => {
                location.hash = "/settings/rejoin";
              }}
            >
              {l10n.t("security.pair_again")}</button>
            <textarea
              className="field"
              aria-label={l10n.t("security.recovery_code_for_restoring_public_information")}
              value={profileCode}
              onChange={(e) => setProfileCode(e.target.value)}
            />
            <button
              className="btn"
              disabled={busy || !profileCode}
              onClick={() =>
                void run(async () => {
                  const { AuthoritySetup } = await import("../authority-setup");
                  const setup = new AuthoritySetup();
                  try {
                    await setup.completeAccountPublicProfile(
                      remote.account.server,
                      profileCode,
                    );
                    setProfileCode("");
                    setMessage(l10n.t("security.account_public_key_restored"));
                  } finally {
                    setup.lifecycle.dispose();
                  }
                })
              }
            >
              {l10n.t("security.restore_public_information")}</button>
          </>
        )}
        <div className="set-banners">
          {message && <Banner kind="">{message}</Banner>}
          {(error || readError) && <Banner kind="err">{error || readError}</Banner>}
        </div>
      </div>
      {target && (
        <Alert
          title={l10n.t("security.transfer_management")}
          text={l10n.t("security.confirm_that_the_successor_device_saved_the_new_recovery", { value: String(clientName(remote.devices.get(target), remote.devices, target)) })}
          ok={l10n.t("security.confirm_transfer")}
          danger
          onCancel={() => setTarget("")}
          onOk={() => {
            const id = target;
            setTarget("");
            void run(async () => {
              await remote.handoffAuthority(id);
              if (await remote.authority.pending())
                throw new Error(l10n.t("security.transfer_result_unverified_continue_the_original_operation"));
              setMessage(l10n.t("security.management_transferred_waiting_for_the_successor_to_complete_takeover"));
            });
          }}
        />
      )}
    </div>
  );
}
