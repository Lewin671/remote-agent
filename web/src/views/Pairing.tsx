import * as l10n from "../i18n";
import { SetupReset } from "../SetupReset";
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useRef, useState } from "react";
import { joinByPairing, PairCanceled, PairingStatusError, parsePairLink } from "../pairing";
import { errorText } from "../remote";
import { Banner } from "../ui";
import { LoaderCircle } from "../icons";

const spaced = (code: string) => `${code.slice(0, 4)} ${code.slice(4)}`;

/** How this browser joins: the phone that has joined shows a code, and it
 * is typed in here. A browser cannot scan, so the QR code goes unmentioned.
 * The path is the Android app's; the wording matches the other clients'. */
export const joinLead = () => l10n.t("security.on_the_managing_client_open_settings_add_device_and");
export const joinExistingLead = () => l10n.t("security.the_original_device_identity_keys_and_history_are_preserved");

const defaultServer = () =>
  localStorage.getItem("ra-server") ||
  import.meta.env.VITE_DEFAULT_SERVER ||
  "";

/** A joining error, as the banner the new device shows. */
export function joinBanner(e: unknown): {
  kind: "err" | "warn";
  text: string;
  action: "" | "restart" | "retry" | "refill";
} {
  const text = errorText(e);
  if (e instanceof ConnectError && e.code === Code.PermissionDenied && e.rawMessage === "login failed")
    return { kind: "err", text, action: "restart" };
  if (/security: clock/.test(text))
    return {
      kind: "warn",
      text: l10n.t("security.this_device_s_clock_is_behind_the_managing_client"),
      action: "retry",
    };
  if (e instanceof Error && /security chain is too large/.test(e.message))
    return {
      kind: "err",
      text: l10n.t("security.the_account_s_state_chain_is_too_long_to"),
      action: "",
    };
  // The relay or the device that made the code is of another release.
  if (
    e instanceof ConnectError &&
    e.code === Code.FailedPrecondition &&
    /protocol version/.test(e.rawMessage)
  )
    return {
      kind: "err",
      text: l10n.t("security.this_page_and_the_server_or_pairing_device_use"),
      action: "refill",
    };
  if (
    e instanceof ConnectError &&
    [
      Code.NotFound,
      Code.InvalidArgument,
      Code.PermissionDenied,
      Code.FailedPrecondition,
    ].includes(e.code)
  )
    return { kind: "err", text: l10n.t("security.the_pairing_code_is_wrong_or_has_already_been"), action: "refill" };
  if (e instanceof ConnectError && e.code === Code.ResourceExhausted)
    return { kind: "err", text: l10n.t("security.too_many_attempts_wait_a_minute_and_retry"), action: "refill" };
  if (e instanceof PairingStatusError && e.code === "expired_or_canceled")
    return { kind: "err", text: l10n.t("security.this_pairing_expired_generate_a_new_code_on_the"), action: "refill" };
  if (e instanceof PairingStatusError && e.code === "refused")
    return { kind: "err", text: l10n.t("security.the_other_device_refused_this_join_request"), action: "restart" };
  if (
    (e instanceof ConnectError && e.code === Code.Unavailable) ||
    e instanceof TypeError
  )
    return { kind: "warn", text: l10n.t("security.success_is_uncertain_connection_lost"), action: "retry" };
  return { kind: "err", text, action: "restart" };
}

/** Joins this browser with explicit approval from an enrolled device. */
export function Setup({
  onDone,
  existing,
}: {
  onDone: () => void;
  existing?: import("../storage").StoredAccount;
}) {
  const fromLink = location.hash.startsWith("#/pair?")
    ? parsePairLink(location.hash.slice("#/pair?".length))
    : undefined;
  const [code, setCode] = useState(fromLink ? spaced(fromLink.code) : "");
  const [server, setServer] = useState(
    existing?.server ?? fromLink?.server ?? defaultServer(),
  );
  const [emoji, setEmoji] = useState<string[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof joinBanner>>();
  const stopped = useRef(false);
  // The user gave up waiting; and whether this device already began to
  // register, after which giving up cannot undo the join.
  // One token per attempt: giving up cancels that attempt for good, and a
  // new attempt never revives an old one or takes its late callbacks.
  const attempt = useRef<{ canceled: boolean }>({ canceled: false });
  const [registering, setRegistering] = useState(false);
  const [comparisonPending, setComparisonPending] = useState(false);
  const comparison = useRef<((allowed: boolean) => void) | undefined>(
    undefined,
  );
  const started = useRef(false);
  // Mounting again, as StrictMode does in development, must not leave the
  // join in progress marked as stopped.
  useEffect(() => {
    stopped.current = false;
    return () => {
      stopped.current = true;
      comparison.current?.(false);
      comparison.current = undefined;
    };
  }, []);

  const pair = async (link = fromLink ?? { code, server }) => {
    const mine = { canceled: false };
    attempt.current = mine;
    const current = () => attempt.current === mine;
    comparison.current?.(false);
    comparison.current = undefined;
    setComparisonPending(false);
    setRegistering(false);
    setBusy(true);
    setError(undefined);
    setEmoji(undefined);
    try {
      await joinByPairing(
        link,
        (e) => current() && setEmoji(e),
        () => stopped.current || mine.canceled || !current(),
        () => current() && setRegistering(true),
        existing,
        async () => {
          if (!current() || stopped.current || mine.canceled) return false;
          setComparisonPending(true);
          return new Promise<boolean>((resolve) => {
            comparison.current = resolve;
          });
        },
      );
      if (!current()) return;
      location.hash = "";
      onDone();
    } catch (e) {
      // Given up or replaced: the page already says what it is doing.
      if (!current() || (e instanceof PairCanceled && mine.canceled)) return;
      setRegistering(false);
      setError(joinBanner(e));
      setEmoji(undefined);
      setBusy(false);
    }
  };
  /** Stops waiting on this device and goes back to the code. The server
   * is kept and the old code cleared; the old request may still hold the
   * code on the other device, which should refuse it. */
  const giveUp = () => {
    attempt.current.canceled = true;
    comparison.current?.(false);
    comparison.current = undefined;
    setComparisonPending(false);
    setEmoji(undefined);
    setBusy(false);
    setCode("");
    setError({
      kind: "warn",
      text: l10n.t("security.stopped_waiting_reject_the_old_request_on_the_managing"),
      action: "",
    });
    if (location.hash.startsWith("#/pair?")) location.hash = "";
  };
  useEffect(() => {
    // Once per page: a second join would take the pairing's only slot.
    if (fromLink && !started.current) {
      started.current = true;
      void pair(fromLink);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const banner = error && (
    <div className="pad">
      <Banner
        kind={error.kind}
        action={error.action ? { restart: l10n.t("security.start_over"), retry: l10n.t("common.retry"), refill: l10n.t("security.re_enter") }[error.action] : undefined}
        onAction={() => {
          if (error.action === "refill") {
            setCode("");
            setError(undefined);
          } else if (error.action) void pair({ code, server });
        }}
      >
        {error.text}
      </Banner>
      <SetupReset onDone={onDone} />
    </div>
  );

  if (emoji)
    return (
      <div className="setup">
        <h3 className="hl">{l10n.t("security.compare_patterns")}</h3>
        <div className="emoji">{emoji.join(" ")}</div>
        <p className="txt center-text">
          {l10n.t("security.compare_the_managing_client_s_4_patterns_if_they")}</p>
        {comparisonPending && (
          <button
            className="btn pri"
            onClick={() => {
              const finish = comparison.current;
              comparison.current = undefined;
              setComparisonPending(false);
              setRegistering(true);
              finish?.(true);
            }}
          >
            {l10n.t("security.patterns_match_trust_and_continue")}</button>
        )}
        <p className="wait-line">
          <LoaderCircle />
          {comparisonPending
            ? l10n.t("security.confirm_the_patterns_on_this_device_first")
            : registering
              ? l10n.t("security.confirmed_here_waiting_for_managing_client_approval_and_binding")
              : l10n.t("security.waiting_for_comparison")}
        </p>
        <div className="pair-wait-actions">
          <button className="btn" disabled={registering} onClick={giveUp}>
            {l10n.t("security.patterns_differ_return_to_code_entry")}</button>
          <p className="gf center-text">
            {l10n.t("security.returning_stops_this_wait_reject_the_old_request_on")}</p>
        </div>
      </div>
    );

  return (
    <div className="setup">
      <h3 className="hl">{existing ? l10n.t("security.one_time_security_setup") : l10n.t("security.join_account")}</h3>
      <>
        <p className="txt lead">
          {existing
            ? joinExistingLead()
            : joinLead()}
        </p>
        <input
          className="field code-in"
          inputMode="numeric"
          autoComplete="one-time-code"
          placeholder="0000 0000"
          value={code}
          onChange={(e) => {
            const d = e.target.value.replace(/\D/g, "").slice(0, 8);
            setCode(d.length > 4 ? spaced(d) : d);
          }}
          aria-label={l10n.t("security.pairing_code")}
        />
        <label className="field">
          <small>{l10n.t("security.server")}</small>
          <input
            placeholder="https://ra.example.com"
            value={server}
            onChange={(e) => setServer(e.target.value.trim())}
            aria-label={l10n.t("security.server")}
          />
        </label>
        {banner}
        <div className="setup-go">
          <button
            className="btn pri"
            disabled={busy || code.replace(/\s/g, "").length !== 8 || !server}
            onClick={() => void pair({ code, server })}
          >
            {busy ? l10n.t("connection.connecting") : l10n.t("security.join")}
          </button>
        </div>
      </>
    </div>
  );
}
