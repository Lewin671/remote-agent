import * as l10n from "../i18n";
import { t } from "../i18n";
import { pairingQRCode } from "../qr-code";
import { LoaderCircle, Plus } from "../icons";
import { futureTime } from "../model";
import { useEffect, useRef, useState } from "react";
import type { AuthorityPairing } from "../authority-pairing";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar, useRemote } from "../ui";
import { authorityName } from "./authority-notice";

/** 添加设备 on the current authority: a line of blue words with ＋ after
 * the computers' cards, or the last row of a list of them (row); on any
 * other device, where to do it instead. */
export function ManagementEntry({
  remote,
  go,
  row,
}: {
  remote: Remote;
  go: (r: string) => void;
  row?: boolean;
}) {
  return remote.isAuthority ? (
    <button className={row ? "row add" : "addrow"} onClick={() => go("settings/add-device")}>
      <Plus />
      {l10n.t("home.add_device")}</button>
  ) : (
    <p className="gf">
      {l10n.t("security.add_on_authority", { name: authorityName(remote) })}</p>
  );
}
export function DeviceActions({
  remote,
  id,
  machine,
  go,
  onRemoved,
  incomplete = false,
}: {
  remote: Remote;
  id: string;
  machine?: boolean;
  go?: (r: string) => void;
  onRemoved?: () => void;
  incomplete?: boolean;
}) {
  const [confirm, setConfirm] = useState<"remove" | "delete">();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const active = remote.devices.has(id);
  if (!remote.isAuthority)
    return (
      <p className="gf">
        {l10n.t("security.manage_on_authority", { name: authorityName(remote) })}</p>
    );
  if (id === remote.authorityDeviceId)
    return (
      <p className="gf">{l10n.t("security.the_managing_client_cannot_be_removed_directly_transfer_management_device_management")}</p>
    );
  return (
    <>
      <div className="grp">
        {active ? (
          <button
            className="row danger"
            disabled={busy}
            onClick={() => setConfirm("remove")}
          >
            {incomplete ? l10n.t("security.remove_unfinished_pairing") : l10n.t("security.remove_device")}
          </button>
        ) : (
          machine && (
            <>
              <button
                className="row act"
                disabled={busy}
                onClick={() => go?.("settings/add-device/" + id)}
              >
                {l10n.t("security.rejoin_and_resume_sessions")}</button>
              <button
                className="row danger"
                disabled={busy}
                onClick={() => setConfirm("delete")}
              >
                {l10n.t("security.delete_this_computer_s_records")}</button>
            </>
          )
        )}
      </div>
      {error && <div className="set-banners"><Banner kind="err">{error}</Banner></div>}
      {confirm && (
        <Alert
          title={confirm === "remove" ? l10n.t("security.remove_device_device_management") : l10n.t("security.delete_this_computer_s_records_device_management")}
          text={
            confirm === "remove"
              ? incomplete ? l10n.t("security.after_removing_this_unfinished_pairing_generate_a_new_code") : l10n.t("security.removal_immediately_stops_remote_access_from_this_device_the")
              : l10n.t("security.this_permanently_deletes_sessions_events_and_images_on_the")
          }
          ok={confirm === "remove" ? t("common.remove") : l10n.t("security.delete_records")}
          danger
          onCancel={() => setConfirm(undefined)}
          onOk={() => {
            const action = confirm;
            setConfirm(undefined);
            setBusy(true);
            setError("");
            void (
              action === "remove"
                ? remote.removeDevice(id)
                : remote.deleteMachineRecords(id)
            )
              .then(() => onRemoved?.())
              .catch((e) => setError(errorText(e)))
              .finally(() => setBusy(false));
          }}
        />
      )}
    </>
  );
}
export function PairCodePage({
  remote,
  replaces = "",
  onBack,
}: {
  remote: Remote;
  replaces?: string;
  onBack: () => void;
}) {
  useRemote(remote);
  const [pair, setPair] = useState<AuthorityPairing>();
  const [qr, setQr] = useState<{ url: string; src: string }>();
  const [emoji, setEmoji] = useState<string[]>();
  const [confirmed, setConfirmed] = useState(false);
  const [approved, setApproved] = useState(false);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = useRef<AuthorityPairing | undefined>(undefined);
  const alive = useRef(true);
  const handedOff = useRef(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (current.current && !handedOff.current) void current.current.cancel().catch(() => {});
    };
  }, []);
  useEffect(() => {
    if (!pair || done || (emoji && !approved)) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (stopped) return;
      try {
        if (!document.hidden) {
          if (approved) {
            await pair.finishPairingOperation();
            if (!(await remote.authority.pending())) {
              await remote.loadDevices();
              if (!stopped) setDone(true);
              return;
            }
          } else {
            const joined = await pair.joined();
            if (!stopped && joined) setEmoji(joined.emoji);
          }
        }
      } catch (e) {
        if (!stopped) setError(errorText(e));
      }
      if (!stopped) timer = setTimeout(poll, 1500);
    };
    void poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [pair, approved, done, emoji, remote]);
  const pairURL = pair?.url;
  useEffect(() => {
    if (!pairURL) return;
    let stopped = false;
    void pairingQRCode(pairURL).then((src) => {
      if (!stopped) setQr({ url: pairURL, src });
    }).catch((e) => { if (!stopped) setError(errorText(e)); });
    return () => { stopped = true; };
  }, [pairURL]);
  const run = async (job: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await job();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  if (!remote.isAuthority)
    return (
      <div className="page set">
        <NavBar title={l10n.t("home.add_device")} onBack={onBack} />
        <div className="body">
          <ManagementEntry remote={remote} go={() => {}} />
        </div>
      </div>
    );
  return (
    <div className="page set">
      <NavBar
        title={replaces ? l10n.t("security.rejoin_and_resume_sessions") : l10n.t("home.add_device")}
        onBack={onBack}
      />
      <div className="body security-body">
        <p className="txt">{l10n.t("security.computer_run_remote_agent_account_join_and_enter_the")}</p>
        {done ? (
          <Banner kind="">{l10n.t("security.device_joined")}</Banner>
        ) : !pair ? (
          <>
            {replaces && (
              <p className="txt">
                {l10n.t("security.only_this_computer_can_rejoin_its_original_archive_with")}</p>
            )}
            <button
              className="btn pri"
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  const p = await remote.startPairing(
                    location.origin + location.pathname,
                    replaces,
                  );
                  current.current = p;
                  handedOff.current = false;
                  if (!alive.current) {
                    await p.cancel();
                    return;
                  }
                  setPair(p);
                })
              }
            >
              {busy ? l10n.t("security.generating") : l10n.t("security.generate_pairing_code")}
            </button>
          </>
        ) : (
          <>
            {qr?.url === pair.url && <img className="security-qr" src={qr.src} alt={l10n.t("security.qr_code_for_adding_a_device")} />}
            <div className="pair-code">
              {pair.code.slice(0, 4)} {pair.code.slice(4)}
            </div>
            <p className="pcs">
              {l10n.t("security.pair_valid_until", { time: futureTime(pair.expires, Date.now(), "time") })}
            </p>
            {emoji ? (
              <>
                <div className="emoji">{emoji.join(" ")}</div>
                <p className="txt center-text">
                  {l10n.t("security.compare_the_4_patterns_on_the_new_device_confirm")}</p>
                {!approved && (
                  <>
                    <div className="grp">
                      <label className="row backup-check">
                        <input
                          type="checkbox"
                          checked={confirmed}
                          onChange={(e) => setConfirmed(e.target.checked)}
                        />
                        {l10n.t("security.patterns_match_the_new_device_confirmed_first")}</label>
                    </div>
                    <button
                      className="btn pri"
                      disabled={busy || !confirmed}
                      onClick={() =>
                        void run(async () => {
                          // Approval and its durable delivery belong to the account,
                          // including while approve() is still awaiting its reply.
                          handedOff.current = true;
                          await pair.approve(confirmed);
                          setApproved(true);
                        })
                      }
                    >
                      {l10n.t("security.allow_to_join")}</button>
                  </>
                )}
                {approved && (
                  <p role="status" className="wait-line">
                    <LoaderCircle />
                    {l10n.t("security.approved_waiting_for_the_new_device_to_finish_binding")}</p>
                )}
              </>
            ) : (
              <p role="status" className="wait-line">
                <LoaderCircle />
                {l10n.t("security.waiting_for_the_new_device_to_enter_the_pairing")}</p>
            )}
            {!approved && (
              <button
                className="btn"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    try {
                      await pair.cancel();
                    } finally {
                      // The local SAS key is already destroyed even if the
                      // cancel acknowledgement is lost. Durable delivery is
                      // retained by the coordinator and checked on restart.
                      current.current = undefined;
                      setPair(undefined);
                      setEmoji(undefined);
                      setConfirmed(false);
                    }
                  })
                }
              >
                {l10n.t("security.cancel_pairing")}</button>
            )}
          </>
        )}
        {error && <div className="set-banners"><Banner kind="err">{error}</Banner></div>}
      </div>
    </div>
  );
}
