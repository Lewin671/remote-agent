import * as l10n from "../i18n";
import { ChevronRight } from "../icons";
import { futureTime } from "../model";
import { Suspense, useEffect, useRef, useState } from "react";
import { Startup } from "../Startup";
import { page } from "./pages";
import {
  BROWSER_AUTHORITY_NOTICE,
  createStandaloneSecurityClient,
  requestAuthorityPersistence,
  SecurityClient,
  type StandaloneSecurityHandle,
} from "../core/security-client";
import {
  SecurityBootstrapKind,
  type SecurityRecoveryInfo,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
import { GetRecoveryContextResponse_Phase as Phase } from "../gen/remoteagent/v1/relay_pb";
import { errorText } from "../remote";
import { Banner, NavBar } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { SetupReset } from "../SetupReset";

// Pairing and curve code are needed only after choosing to join.
const Setup = page(() => import("./Pairing").then(m => ({ default: m.Setup })));

export function AccountEntry({
  onDone,
  client: supplied,
  initialStatus,
}: {
  onDone: () => void;
  client?: SecurityClient;
  initialStatus?: SecurityWorkflowStatus;
}) {
  const ownHandle = useRef<StandaloneSecurityHandle | undefined>(undefined);
  const [client, setClient] = useState(supplied);
  const [mode, setMode] = useState<"" | "join" | "create" | "recovery">(
    location.hash.startsWith("#/pair?") ? "join" : "",
  );
  const [startup, setStartup] = useState<SecurityWorkflowStatus | undefined>(initialStatus);
  const [server, setServer] = useState(
    localStorage.getItem("ra-server") ||
      import.meta.env.VITE_DEFAULT_SERVER ||
      "",
  );
  const [token, setToken] = useState("");
  const [oldCode, setOldCode] = useState("");
  const [code, setCode] = useState("");
  const [saved, setSaved] = useState(false);
  const [persisted, setPersisted] = useState<boolean>();
  const [status, setStatus] = useState<SecurityRecoveryInfo>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [signatureMissing, setSignatureMissing] = useState(false);

  const syncMode = (st: SecurityWorkflowStatus) => {
    setStartup(st);
    if (st.bootstrap === SecurityBootstrapKind.PENDING_CREATE) {
      setMode("create");
    } else if (st.bootstrap === SecurityBootstrapKind.PENDING_RECOVERY) {
      setMode("recovery");
      if (st.pendingOp && !st.pendingOp.recoveryId && !st.pendingOp.signed) {
        setSignatureMissing(true);
      }
    }
  };

  useEffect(() => {
    const handle = supplied ? undefined : createStandaloneSecurityClient();
    ownHandle.current = handle;
    const activeClient = supplied ?? handle!.client;
    setClient(activeClient);
    let alive = true;
    void activeClient
      .bootstrap()
      .then((st) => {
        if (alive) syncMode(st);
      })
      .catch((e) => {
        if (alive) setError(errorText(e));
      });
    return () => {
      alive = false;
      ownHandle.current = undefined;
      handle?.dispose();
    };
  }, [supplied]);

  const refresh = async () => {
    if (!client) return;
    syncMode(await client.getStatus());
  };

  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setSignatureMissing(false);
    try {
      await work();
      await refresh();
    } catch (e) {
      setError(errorText(e));
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  if (!client || (!startup && !error))
    return <div className="center muted">{l10n.t("security.loading")}</div>;

  const repairBanner = startup?.legacyRepairNotice && (
    <Banner
      kind="warn"
      action={l10n.t("common.got_it")}
      onAction={() => void run(async () => {
        setStartup(await client.dismissUpgradeNotice());
      })}
    >
      <strong>{l10n.t("security.upgrade_repair_title")}</strong>{" "}
      {l10n.t("security.upgrade_repair_notice")}
    </Banner>
  );

  if (mode === "join")
    return (
      <>
        <NavBar onBack={() => setMode("")} title={l10n.t("security.join_an_existing_account")} />
        <Suspense fallback={<Startup />}><Setup onDone={onDone} /></Suspense>
      </>
    );
  if (!mode)
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
            <button className="row entry-row" aria-label={l10n.t("security.create_account")} onClick={() => setMode("create")}>
              <span className="row-name">{l10n.t("security.create_account")}<small>{l10n.t("security.this_device_becomes_the_managing_client")}</small></span>
              <span className="v"><ChevronRight /></span>
            </button>
            <button className="row entry-row" aria-label={l10n.t("security.join_an_existing_account")} onClick={() => setMode("join")}>
              <span className="row-name">{l10n.t("security.join_an_existing_account")}<small>{l10n.t("security.join_with_a_code_from_the_managing_client")}</small></span>
              <span className="v"><ChevronRight /></span>
            </button>
            <button className="row entry-row" aria-label={l10n.t("security.recover_management_with_a_recovery_code")} onClick={() => setMode("recovery")}>
              <span className="row-name">{l10n.t("security.recover_management_with_a_recovery_code")}<small>{l10n.t("security.use_when_the_managing_client_is_lost")}</small></span>
              <span className="v"><ChevronRight /></span>
            </button>
          </div>
          <p className="gf entry-note">{l10n.t("security.entry_e2ee_note")}</p>
        </div>
        {error && <div className="set-banners"><Banner kind="err">{error}</Banner></div>}
      </div>
    );
  const pending =
    (mode === "create" && startup?.bootstrap === SecurityBootstrapKind.PENDING_CREATE) ||
    (mode === "recovery" && startup?.bootstrap === SecurityBootstrapKind.PENDING_RECOVERY);
  return (
    <div className="setup authority-setup">
      <NavBar
        title={mode === "create" ? l10n.t("security.create_account") : l10n.t("security.recover_management_with_a_recovery_code")}
        onBack={!pending ? () => setMode("") : undefined}
      />
      {repairBanner}
      <Banner kind="">{BROWSER_AUTHORITY_NOTICE()}</Banner>
      {!pending && (
        <label className="field">
          <small>{l10n.t("security.server")}</small>
          <input
            aria-label={l10n.t("security.server")}
            placeholder="https://ra.example.com"
            value={server}
            onChange={(e) => setServer(e.target.value.trim())}
          />
        </label>
      )}
      {mode === "recovery" && persisted !== undefined && (
        <PersistenceResult persisted={persisted} />
      )}
      {mode === "create" ? (
        <>
          {pending && !code && (
            <Banner kind="warn">
              {l10n.t("security.the_original_creation_draft_is_preserved_continue_with_the")}</Banner>
          )}
          {persisted !== undefined && (
            <PersistenceResult persisted={persisted} />
          )}
          {code && <CodeBackup code={code} saved={saved} onSaved={setSaved} />}
          {pending && (
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
            disabled={busy || (!pending && !server) || (!!code && !saved)}
            onClick={() =>
              void run(async () => {
                if (!pending) {
                  const sp = await requestAuthorityPersistence();
                  const p = await client.prepareCreation(server, sp);
                  setCode(p.recoveryCode ?? "");
                  if (p.resumedDraft && p.codeSaved) setSaved(true);
                  setPersisted(sp);
                  return;
                }
                if (code && saved) {
                  await client.confirmCreationCodeSaved();
                }
                let cur = await client.getStatus();
                if (cur.bootstrap === SecurityBootstrapKind.PENDING_CREATE) {
                  const res = await client.continueCreation(token);
                  if (res.status) cur = res.status;
                }
                if (
                  !code &&
                  cur.bootstrap === SecurityBootstrapKind.PENDING_CREATE &&
                  !cur.pendingOp?.codeSaved
                ) {
                  await client.adoptCreationWithoutCode();
                }
                onDone();
              })
            }
          >
            {busy ? l10n.t("security.processing") : !pending ? l10n.t("security.generate_recovery_code") : l10n.t("security.continue_creation")}
          </button>
        </>
      ) : (
        <>
          <p className="txt">{l10n.t("security.if_this_browser_is_already_a_member_recovery_will")}</p>
          {!pending && (
            <>
              <p className="txt">
                {l10n.t("security.after_submission_wait_48_hours_the_current_managing_client")}</p>
              <textarea
                className="field"
                aria-label={l10n.t("security.original_recovery_code")}
                placeholder={l10n.t("security.original_recovery_code")}
                value={oldCode}
                onChange={(e) => setOldCode(e.target.value)}
              />
              <button
                className="btn pri"
                disabled={busy || !server || !oldCode.trim()}
                onClick={() =>
                  void run(async () => {
                    const sp = await requestAuthorityPersistence();
                    setPersisted(sp);
                    const p = await client.prepareEmergencyRecovery(server, oldCode);
                    setCode(p.recoveryCode);
                    if (p.recovery && p.recovery.phase !== Phase.UNSPECIFIED) setStatus(p.recovery);
                    setOldCode("");
                  })
                }
              >
                {l10n.t("security.prepare_recovery")}</button>
            </>
          )}
          {pending && (
            <>
              {code ? (
                <CodeBackup code={code} saved={saved} onSaved={setSaved} />
              ) : (
                <p className="txt">
                  {l10n.t("security.the_original_recovery_draft_is_preserved_the_new_recovery")}</p>
              )}
              {status && status.phase !== Phase.UNSPECIFIED && (
                <Banner kind={status.phase === Phase.REJECTED ? "err" : "warn"}>
                  {status.phase === Phase.REJECTED
                    ? l10n.t("security.recovery_was_rejected_you_can_apply_again")
                    : l10n.t("security.recovery_request_pending_eligible_at", { value: String(futureTime(status.readyMs)) })}
                </Banner>
              )}
              {signatureMissing && (
                <>
                  <textarea
                    className="field"
                    aria-label={l10n.t("security.original_recovery_code")}
                    value={oldCode}
                    onChange={(e) => setOldCode(e.target.value)}
                  />
                  <button
                    className="btn"
                    disabled={busy || !oldCode}
                    onClick={() =>
                      void run(async () => {
                        const next = await client.continueRecoverySigning(oldCode);
                        if (next) setStatus(next);
                        setSignatureMissing(false);
                        setOldCode("");
                      })
                    }
                  >
                    {l10n.t("security.continue_original_draft")}</button>
                  <button
                    className="btn"
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        await client.discardUnsignedRecovery();
                        setSignatureMissing(false);
                        setMode("");
                      })
                    }
                  >
                    {l10n.t("security.abandon_unsigned_draft")}</button>
                </>
              )}
              <button
                className="btn pri"
                disabled={busy || (!!code && !saved)}
                onClick={() =>
                  void run(async () => {
                    if (saved) await client.confirmRecoveryCodeSaved();
                    if (
                      status &&
                      [Phase.READY, Phase.PUBLISHED].includes(status.phase)
                    ) {
                      await client.completeRecovery();
                      onDone();
                    } else {
                      const next = await client.resumeRecovery();
                      if (next) {
                        setStatus(next);
                      } else {
                        setSignatureMissing(true);
                      }
                    }
                  })
                }
              >
                {busy
                  ? l10n.t("security.processing")
                  : status &&
                      [Phase.READY, Phase.PUBLISHED].includes(status.phase)
                    ? l10n.t("security.complete_recovery")
                    : l10n.t("security.submit_or_check_original_recovery")}
              </button>
              {status?.phase === Phase.REJECTED && (
                <button
                  className="btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await client.discardRejectedRecovery();
                      setStatus(undefined);
                      setCode("");
                      setSaved(false);
                    })
                  }
                >
                  {l10n.t("security.apply_again")}</button>
              )}
            </>
          )}
        </>
      )}
      {error && <Banner kind="err">{error}</Banner>}
      {pending && <SetupReset onDone={onDone} />}
    </div>
  );
}
