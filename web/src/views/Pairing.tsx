import * as l10n from "../i18n";
import { SetupReset } from "../SetupReset";
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useRef, useState } from "react";
import { SecurityBootstrapKind, SecurityErrorCode } from "../gen/remoteagent/core/v1/security_pb";
import {
  createStandaloneSecurityClient,
  parsePairLinkQuery,
  SecurityWorkflowError,
  type SecurityClient,
  type StandaloneSecurityHandle,
} from "../core/security-client";
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
  if (e instanceof SecurityWorkflowError) {
    if (e.code === SecurityErrorCode.CLOCK_SKEWED) {
      return {
        kind: "warn",
        text: l10n.t("security.this_device_s_clock_is_behind_the_managing_client"),
        action: "retry",
      };
    }
    if (e.code === SecurityErrorCode.CAPACITY) {
      return {
        kind: "err",
        text: l10n.t("security.the_account_s_state_chain_is_too_long_to"),
        action: "",
      };
    }
    if (e.errorAction === "refill" || e.code === SecurityErrorCode.NOT_FOUND || e.code === SecurityErrorCode.INVALID) {
      return { kind: "err", text: e.message, action: "refill" };
    }
    if (e.uncertain || e.retryable || e.code === SecurityErrorCode.UNAVAILABLE) {
      return { kind: "warn", text: e.message, action: "retry" };
    }
    return { kind: "err", text: e.message, action: "restart" };
  }
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
  existingServer,
  client: suppliedClient,
}: {
  onDone: () => void;
  existing?: import("../storage").StoredAccount;
  existingServer?: string;
  client?: SecurityClient;
}) {
  const fromLink = location.hash.startsWith("#/pair?")
    ? parsePairLinkQuery(location.hash.slice("#/pair?".length))
    : undefined;
  const repairServer = existing?.server ?? existingServer;
  const isExistingRepair = Boolean(existing || existingServer);
  const [code, setCode] = useState(fromLink ? spaced(fromLink.code) : "");
  const [server, setServer] = useState(
    repairServer ?? fromLink?.server ?? defaultServer(),
  );
  const [emoji, setEmoji] = useState<string[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof joinBanner>>();
  const stopped = useRef(false);
  const attempt = useRef<{ canceled: boolean; pairingId: string }>({ canceled: false, pairingId: "" });
  const [registering, setRegistering] = useState(false);
  const [comparisonPending, setComparisonPending] = useState(false);
  const [pendingRegistration, setPendingRegistration] = useState(
    suppliedClient?.status?.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION,
  );
  const ownClient = useRef<StandaloneSecurityHandle | undefined>(undefined);
  const bootstrappedRef = useRef(false);
  const registeringRef = useRef(false);
  registeringRef.current = registering;
  const started = useRef(false);

  const ensureClient = async (): Promise<SecurityClient> => {
    const client = suppliedClient ?? (ownClient.current ??= createStandaloneSecurityClient()).client;
    if (!bootstrappedRef.current) {
      bootstrappedRef.current = true;
      try {
        await client.bootstrap();
      } catch (err) {
        bootstrappedRef.current = false;
        throw err;
      }
    }
    return client;
  };

  useEffect(() => {
    stopped.current = false;
    return () => {
      stopped.current = true;
      const pid = attempt.current.pairingId;
      attempt.current.canceled = true;
      attempt.current.pairingId = "";
      if (pid && !registeringRef.current) {
        const c = suppliedClient ?? ownClient.current?.client;
        if (c) void c.cancelJoinPairing(pid).catch(() => {});
      }
      ownClient.current?.dispose();
      ownClient.current = undefined;
      bootstrappedRef.current = false;
    };
  }, [suppliedClient]);

  const isPendingReg = (client: SecurityClient) =>
    client.status?.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION;

  const refreshPendingReg = async (client: SecurityClient) =>
    isPendingReg(client) ? client.status : await client.getStatus();

  const pair = async (link: { code: string; server: string; commit?: Uint8Array } = fromLink ?? { code, server }) => {
    const mine = { canceled: false, pairingId: "" };
    attempt.current = mine;
    const current = () => attempt.current === mine && !mine.canceled && !stopped.current;
    setComparisonPending(false);
    setRegistering(false);
    setBusy(true);
    setError(undefined);
    setEmoji(undefined);
    try {
      const client = await ensureClient();
      const status = await refreshPendingReg(client);
      if (status?.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION) {
        setPendingRegistration(true);
        setRegistering(true);
        await client.resumePendingOp();
        if (!current()) return;
        mine.pairingId = "";
        setPendingRegistration(false);
        location.hash = "";
        onDone();
        return;
      }
      setPendingRegistration(false);
      const info = await client.startJoinPairing({
        server: link.server,
        code: link.code.replace(/\s+/g, ""),
        commitHint: link.commit,
        existingRepair: isExistingRepair,
        existingServer: repairServer,
        rawLink: fromLink && link === fromLink ? location.hash : "",
      });
      mine.pairingId = info.pairingId;
      if (!current()) {
        if (info.pairingId) void client.cancelJoinPairing(info.pairingId).catch(() => {});
        return;
      }
      setEmoji(info.sasEmoji);
      setComparisonPending(true);
    } catch (e) {
      if (!current()) return;
      setRegistering(false);
      setComparisonPending(false);
      setError(joinBanner(e));
      setEmoji(undefined);
      setBusy(false);
    }
  };

  const confirmMatch = async () => {
    const mine = attempt.current;
    const pid = mine.pairingId;
    if (!pid) return;
    const current = () => attempt.current === mine && !mine.canceled && !stopped.current;
    setComparisonPending(false);
    setRegistering(true);
    try {
      const client = await ensureClient();
      await client.confirmJoinSas(pid);
      if (!current()) return;
      mine.pairingId = "";
      location.hash = "";
      onDone();
    } catch (e) {
      if (!current()) return;
      mine.pairingId = "";
      setRegistering(false);
      const client = suppliedClient ?? ownClient.current?.client;
      if (client) {
        const st = await refreshPendingReg(client);
        if (!current()) return;
        setPendingRegistration(st?.bootstrap === SecurityBootstrapKind.PENDING_REGISTRATION);
      }
      setError(joinBanner(e));
      setEmoji(undefined);
      setBusy(false);
    }
  };

  /** Stops waiting on this device and goes back to the code. */
  const giveUp = () => {
    const pid = attempt.current.pairingId;
    attempt.current.canceled = true;
    attempt.current.pairingId = "";
    if (pid) {
      const c = suppliedClient ?? ownClient.current?.client;
      if (c) void c.cancelJoinPairing(pid).catch(() => {});
    }
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
            onClick={() => void confirmMatch()}
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
      <h3 className="hl">{isExistingRepair ? l10n.t("security.one_time_security_setup") : l10n.t("security.join_account")}</h3>
      <>
        <p className="txt lead">
          {isExistingRepair
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
            disabled={busy || (!pendingRegistration && (code.replace(/\s/g, "").length !== 8 || !server))}
            onClick={() => void pair({ code, server })}
          >
            {busy
              ? l10n.t("connection.connecting")
              : pendingRegistration
                ? l10n.t("common.retry")
                : l10n.t("security.join")}
          </button>
        </div>
      </>
    </div>
  );
}
