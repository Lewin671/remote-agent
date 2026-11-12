import * as l10n from "../i18n";
import { ChevronRight } from "../icons";
import { futureTime } from "../model";
import { Suspense, useEffect, useState } from "react";
import { Startup } from "../Startup";
import { page } from "./pages";
import {
  AuthoritySetup,
  RecoverySignatureMissing,
  authorityStartup,
  type AuthorityStartup,
  type RecoveryStatus,
} from "../authority-setup";
import { GetRecoveryContextResponse_Phase as Phase } from "../gen/remoteagent/v1/relay_pb";
import {
  BROWSER_AUTHORITY_NOTICE,
  requestAuthorityPersistence,
} from "../security-operation";
import { errorText } from "../remote";
import { Banner, NavBar } from "../ui";
import { CodeBackup, PersistenceResult } from "./AuthorityKit";
import { SetupReset } from "../SetupReset";

// Pairing and curve code are needed only after choosing to join.
const Setup = page(() => import("./Pairing").then(m => ({ default: m.Setup })));

export function AccountEntry({
  onDone,
  setup: supplied,
}: {
  onDone: () => void;
  setup?: AuthoritySetup;
}) {
  const [setup, setSetup] = useState(supplied);
  const [mode, setMode] = useState<"" | "join" | "create" | "recovery">(
    location.hash.startsWith("#/pair?") ? "join" : "",
  );
  const [startup, setStartup] = useState<AuthorityStartup>();
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
  const [status, setStatus] = useState<RecoveryStatus>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [signatureMissing, setSignatureMissing] = useState(false);
  const refresh = async () => {
    const state = await authorityStartup();
    setStartup(state);
    if (state.kind === "create" || state.kind === "recovery")
      setMode(state.kind);
  };
  useEffect(() => {
    const instance = supplied ?? new AuthoritySetup();
    setSetup(instance);
    void refresh().catch((e) => setError(errorText(e)));
    return () => {
      if (!supplied) instance.lifecycle.dispose();
    };
  }, [supplied]);
  const run = async (work: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setSignatureMissing(false);
    try {
      await work();
      await refresh();
    } catch (e) {
      setSignatureMissing(e instanceof RecoverySignatureMissing);
      setError(errorText(e));
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  if (!setup || (!startup && !error))
    return <div className="center muted">{l10n.t("security.loading")}</div>;
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
        <h3 className="hl">remote-agent</h3>
        <div className="hl-sub">{l10n.t("security.control_ai_coding_clis_on_your_computer_from_any")}</div>
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
        {error && <div className="set-banners"><Banner kind="err">{error}</Banner></div>}
      </div>
    );
  const pending = startup?.kind === mode;
  return (
    <div className="setup authority-setup">
      <NavBar
        title={mode === "create" ? l10n.t("security.create_account") : l10n.t("security.recover_management_with_a_recovery_code")}
        onBack={!pending ? () => setMode("") : undefined}
      />
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
                  const p = await setup.prepareCreation(server);
                  setCode(p.code ?? "");
                  setPersisted(p.storagePersisted);
                  return;
                }
                if (code && saved) {
                  await setup.confirmCreationCodeSaved();
                }
                if ((await authorityStartup()).kind === "create")
                  await setup.continueCreation(token);
                if (
                  !code &&
                  startup?.kind === "create" &&
                  !startup.codeSaved &&
                  (await authorityStartup()).kind === "create"
                )
                  await setup.adoptCreationWithoutCode();
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
                    setPersisted(await requestAuthorityPersistence());
                    const p = await setup.prepareEmergencyRecovery(
                      server,
                      oldCode,
                    );
                    setCode(p.code);
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
              {status && (
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
                        await setup.continueRecoverySigning(oldCode);
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
                        await setup.discardUnsignedRecovery();
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
                    if (saved) await setup.confirmRecoveryCodeSaved();
                    if (
                      status &&
                      [Phase.READY, Phase.PUBLISHED].includes(status.phase)
                    ) {
                      await setup.completeRecovery();
                      onDone();
                    } else setStatus(await setup.resumeRecovery());
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
                      await setup.discardRejectedRecovery();
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
