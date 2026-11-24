import * as l10n from "../i18n";
import { t } from "../i18n";
import { pairingQRCode } from "../qr-code";
import { LoaderCircle, Plus } from "../icons";
import { futureTime } from "../model";
import { useEffect, useRef, useState } from "react";
import { useCoreHost, useDevicesView, useSettingsView } from "../core/host";
import { renderText } from "../core/text";
import type { SecurityPairingInfo } from "../gen/remoteagent/core/v1/security_pb";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar, useRemote } from "../ui";

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
  const vm = useDevicesView(useCoreHost(remote));
  return vm.isManager ? (
    <button className={row ? "row has-ic act" : "addrow"} onClick={() => go("settings/add-device")}>
      <span className={row ? "lead" : undefined}><Plus /></span>
      <span className={row ? "row-name" : undefined}>{l10n.t("home.add_device")}</span>
    </button>
  ) : (
    <p className="gf">
      {renderText(vm.addDeviceHint) || l10n.t("security.add_on_authority", { name: vm.authorityName || l10n.t("security.client") })}</p>
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
  const host = useCoreHost(remote);
  const vm = useDevicesView(host);
  const settingsVm = useSettingsView(host);
  const [confirm, setConfirm] = useState<"remove" | "delete" | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const active = machine
    ? (settingsVm.machines.find((m) => m.machineId === id)?.inDeviceDirectory ?? false)
    : vm.clients.some((c) => c.deviceId === id);
  const managerName = vm.authorityName || l10n.t("security.client");
  if (!vm.isManager) {
    return (
      <p className="gf">
        {machine
          ? l10n.t("settings.remove_non_authority_footer", { name: managerName })
          : l10n.t("security.manage_on_authority", { name: managerName })}
      </p>
    );
  }
  if (id === vm.authorityDeviceId) {
    return (
      <p className="gf">{l10n.t("security.the_managing_client_cannot_be_removed_directly_transfer_management_device_management")}</p>
    );
  }
  return (
    <>
      <div className="grp">
        {active || incomplete ? (
          <button
            className="row danger"
            disabled={busy}
            onClick={() => setConfirm("remove")}
          >
            {incomplete
              ? l10n.t("security.remove_unfinished_pairing")
              : machine
                ? l10n.t("settings.remove_computer_ellipsis")
                : l10n.t("security.remove_device")}
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
                {l10n.t("settings.delete_computer_records_ellipsis")}</button>
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
  const devicesVm = useDevicesView(useCoreHost(remote));
  const [pair, setPair] = useState<SecurityPairingInfo>();
  const [qr, setQr] = useState<{ url: string; src: string }>();
  const [confirmed, setConfirmed] = useState(false);
  const [approved, setApproved] = useState(false);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const currentId = useRef<string | undefined>(undefined);
  const alive = useRef(true);
  const handedOff = useRef(false);
  const [selectedRemoveIds, setSelectedRemoveIds] = useState([] as string[]);
  const removeSelectionTouched = useRef(false);
  const metaPolls = useRef(0);
  const emoji = pair?.sasEmoji.length ? pair.sasEmoji : undefined;
  const waitingMeta = Boolean(emoji && !approved && !pair?.joinerMetaReceived);
  const replaceLabel = renderText(pair?.replaceOldLabel) || l10n.t("security.also_remove_old_devices");
  const replaceHint = renderText(pair?.replaceOldHint);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (currentId.current && !handedOff.current) void remote.cancelPairing(currentId.current).catch(() => {});
    };
  }, [remote]);
  useEffect(() => {
    if (!pair || done || (emoji && !approved && !waitingMeta)) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (stopped) return;
      try {
        if (!document.hidden) {
          if (approved) {
            const next = await remote.finishPairingDelivery(pair.pairingId);
            const st = await remote.getSecurityStatus();
            if (next.delivered || !st.pendingOp) {
              await remote.loadDevices();
              if (!stopped) {
                setPair(next);
                setDone(true);
              }
              return;
            }
          } else {
            if (waitingMeta && metaPolls.current >= 15) {
              return;
            }
            if (waitingMeta) metaPolls.current += 1;
            const next = await remote.pollPairing(pair.pairingId);
            if (!stopped && next.joined && next.sasEmoji.length > 0) {
              if (!emoji) {
                metaPolls.current = 0;
                if (next.replacementCandidates.length > 0 && !removeSelectionTouched.current) {
                  setSelectedRemoveIds(
                    next.replacementCandidates.filter((c) => c.defaultSelected).map((c) => c.deviceId),
                  );
                }
                setPair(next);
                return;
              }
              if (next.joinerMetaReceived) {
                if (next.replacementCandidates.length > 0 && !removeSelectionTouched.current) {
                  setSelectedRemoveIds(
                    next.replacementCandidates.filter((c) => c.defaultSelected).map((c) => c.deviceId),
                  );
                }
                setPair(next);
                return;
              }
            }
          }
        }
      } catch (e) {
        if (!stopped) setError(errorText(e));
      }
      if (!stopped) timer = setTimeout(poll, waitingMeta ? 1000 : 1500);
    };
    void poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [pair, approved, done, emoji, waitingMeta, remote]);
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
  if (!devicesVm.isManager)
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
          <>
            <Banner kind="">{l10n.t("security.device_joined")}</Banner>
            {renderText(pair?.removeOldNotice) && (
              <Banner kind="warn">{renderText(pair?.removeOldNotice)}</Banner>
            )}
          </>
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
                  currentId.current = p.pairingId;
                  handedOff.current = false;
                  removeSelectionTouched.current = false;
                  setSelectedRemoveIds([]);
                  if (!alive.current) {
                    await remote.cancelPairing(p.pairingId);
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
              {l10n.t("security.pair_valid_until", { time: futureTime(pair.expiresMs, Date.now(), "time") })}
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
                    {pair.replacementCandidates.length > 0 && (
                      <div className="grp">
                        <div className="row">
                          <b>{replaceLabel}</b>
                        </div>
                        {replaceHint && (
                          <div className="row help">{replaceHint}</div>
                        )}
                        {pair.replacementCandidates.map((c) => {
                          const title = c.displayName || c.description || c.shortId;
                          const lastActive = renderText(c.lastActiveText);
                          const detail = [
                            c.description && c.description !== title ? c.description : "",
                            c.shortId,
                            lastActive,
                          ]
                            .filter(Boolean)
                            .join(" · ");
                          const checked = selectedRemoveIds.includes(c.deviceId);
                          return (
                            <label key={c.deviceId} className="row backup-check">
                              <input
                                type="checkbox"
                                checked={checked}
                                onChange={(e) => {
                                  removeSelectionTouched.current = true;
                                  const on = e.target.checked;
                                  setSelectedRemoveIds((prev) =>
                                    on
                                      ? prev.includes(c.deviceId) ? prev : [...prev, c.deviceId]
                                      : prev.filter((id) => id !== c.deviceId),
                                  );
                                }}
                              />
                              <span className="row-name">
                                <b>{title}</b>
                                {detail && <small>{detail}</small>}
                              </span>
                            </label>
                          );
                        })}
                      </div>
                    )}
                    <button
                      className="btn pri"
                      disabled={busy || !confirmed}
                      onClick={() =>
                        void run(async () => {
                          // Approval and its durable delivery belong to the account,
                          // including while approve() is still awaiting its reply.
                          handedOff.current = true;
                          const removeIds = pair.replacementCandidates
                            .filter((c) => selectedRemoveIds.includes(c.deviceId))
                            .map((c) => c.deviceId);
                          const res = await remote.approvePairing(pair.pairingId, confirmed, removeIds);
                          setPair(res);
                          setApproved(true);
                          if (res.delivered) {
                            await remote.loadDevices();
                            setDone(true);
                          }
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
                      await remote.cancelPairing(pair.pairingId);
                    } finally {
                      // The local SAS key is already destroyed even if the
                      // cancel acknowledgement is lost. Durable delivery is
                      // retained by the coordinator and checked on restart.
                      currentId.current = undefined;
                      setPair(undefined);
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
