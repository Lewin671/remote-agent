import * as l10n from "../i18n";
import { t } from "../i18n";
import { pairingQRCode } from "../qr-code";
import { LoaderCircle, Plus } from "../icons";
import { useEffect, useState } from "react";
import {
  AddDeviceActionKind,
  AddDeviceStep,
  SecurityBannerTone,
  useAddDeviceView,
  useCoreHost,
  useDevicesView,
  useSettingsView,
} from "../core/host";
import { renderText as fmt } from "../core/text";
import { errorText, type Remote } from "../remote";
import { Alert, Banner, NavBar } from "../ui";

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
      {fmt(vm.addDeviceHint) || l10n.t("security.add_on_authority", { name: vm.authorityName || l10n.t("security.client") })}</p>
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
  const host = useCoreHost(remote);
  const vm = useAddDeviceView(host);
  const [qr, setQr] = useState<{ url: string; src: string }>();
  const [qrError, setQrError] = useState("");

  useEffect(() => {
    host.addDeviceOpen({
      replacesDeviceId: replaces,
      webOrigin: location.origin + location.pathname,
      autoStart: false,
    });
    return () => host.addDeviceClose(replaces);
  }, [host, replaces]);

  const pairURL = vm.qrUrl;
  useEffect(() => {
    if (!pairURL) {
      setQr(undefined);
      return;
    }
    let stopped = false;
    setQrError("");
    void pairingQRCode(pairURL)
      .then((src) => {
        if (!stopped) setQr({ url: pairURL, src });
      })
      .catch((e) => {
        if (!stopped) setQrError(errorText(e));
      });
    return () => {
      stopped = true;
    };
  }, [pairURL]);

  if (vm.step === AddDeviceStep.NOT_MANAGER) {
    return (
      <div className="page set">
        <NavBar title={l10n.t("home.add_device")} onBack={onBack} />
        <div className="body">
          <p className="gf">{fmt(vm.nonManagerHint)}</p>
        </div>
      </div>
    );
  }

  const showCode =
    vm.step === AddDeviceStep.WAITING_JOINER ||
    vm.step === AddDeviceStep.COMPARE_SAS ||
    vm.step === AddDeviceStep.APPROVING ||
    vm.step === AddDeviceStep.DELIVERING;
  const bannerText = qrError || (vm.banner?.visible ? fmt(vm.banner.text) : "");
  const bannerKind = !qrError && vm.banner?.tone === SecurityBannerTone.WARN ? "warn" : "err";

  return (
    <div className="page set">
      <NavBar
        title={replaces ? l10n.t("security.rejoin_and_resume_sessions") : l10n.t("home.add_device")}
        onBack={onBack}
      />
      <div className="body security-body">
        <p className="txt">{l10n.t("security.computer_run_remote_agent_account_join_and_enter_the")}</p>
        {vm.step === AddDeviceStep.DONE && (
          <>
            <Banner kind="">{l10n.t("security.device_joined")}</Banner>
            {fmt(vm.removeOldWarning) && (
              <Banner kind="warn">{fmt(vm.removeOldWarning)}</Banner>
            )}
          </>
        )}
        {vm.step !== AddDeviceStep.DONE && !showCode && (
          <>
            {replaces && (
              <p className="txt">
                {l10n.t("security.only_this_computer_can_rejoin_its_original_archive_with")}
              </p>
            )}
            <button
              className="btn pri"
              disabled={!vm.canGenerate && !vm.canRetry}
              onClick={() => host.addDeviceAction(AddDeviceActionKind.GENERATE)}
            >
              {vm.busy ? l10n.t("security.generating") : l10n.t("security.generate_pairing_code")}
            </button>
          </>
        )}
        {vm.step !== AddDeviceStep.DONE && showCode && (
          <>
            {qr?.url === vm.qrUrl && (
              <img className="security-qr" src={qr.src} alt={l10n.t("security.qr_code_for_adding_a_device")} />
            )}
            {vm.formattedCode && <div className="pair-code">{vm.formattedCode}</div>}
            {fmt(vm.validUntilText) && <p className="pcs">{fmt(vm.validUntilText)}</p>}

            {vm.sasEmoji.length > 0 ? (
              <>
                <div className="emoji">{vm.sasEmoji.join(" ")}</div>
                <p className="txt center-text">
                  {l10n.t("security.compare_the_4_patterns_on_the_new_device_confirm")}
                </p>
                {vm.step === AddDeviceStep.COMPARE_SAS && (
                  <>
                    <div className="grp">
                      <label className="row backup-check">
                        <input
                          type="checkbox"
                          checked={vm.sasConfirmedChecked}
                          onChange={(e) =>
                            host.addDeviceAction(AddDeviceActionKind.SET_SAS_CONFIRMED, {
                              flag: e.target.checked,
                            })
                          }
                        />
                        {l10n.t("security.patterns_match_the_new_device_confirmed_first")}
                      </label>
                    </div>
                    {vm.replacementCandidates.length > 0 && (
                      <div className="grp">
                        <div className="row">
                          <b>{fmt(vm.replaceOldLabel) || l10n.t("security.also_remove_old_devices")}</b>
                        </div>
                        {fmt(vm.replaceOldHint) && (
                          <div className="row help">{fmt(vm.replaceOldHint)}</div>
                        )}
                        {vm.replacementCandidates.map((c) => {
                          const detail = fmt(c.subtitle);
                          return (
                            <label key={c.deviceId} className="row backup-check">
                              <input
                                type="checkbox"
                                checked={c.selected}
                                onChange={(e) =>
                                  host.addDeviceAction(AddDeviceActionKind.TOGGLE_REMOVE_CANDIDATE, {
                                    deviceId: c.deviceId,
                                    flag: e.target.checked,
                                  })
                                }
                              />
                              <span className="row-name">
                                <b>{c.title}</b>
                                {detail && <small>{detail}</small>}
                              </span>
                            </label>
                          );
                        })}
                      </div>
                    )}
                    <button
                      className="btn pri"
                      disabled={!vm.canApprove}
                      onClick={() => host.addDeviceAction(AddDeviceActionKind.APPROVE)}
                    >
                      {l10n.t("security.allow_to_join")}
                    </button>
                  </>
                )}
                {(vm.step === AddDeviceStep.APPROVING || vm.step === AddDeviceStep.DELIVERING) && (
                  <p role="status" className="wait-line">
                    <LoaderCircle />
                    {l10n.t("security.approved_waiting_for_the_new_device_to_finish_binding")}
                  </p>
                )}
              </>
            ) : (
              <p role="status" className="wait-line">
                <LoaderCircle />
                {l10n.t("security.waiting_for_the_new_device_to_enter_the_pairing")}
              </p>
            )}
            {vm.canCancel && (
              <button
                className="btn"
                disabled={vm.busy}
                onClick={() => host.addDeviceAction(AddDeviceActionKind.CANCEL)}
              >
                {l10n.t("security.cancel_pairing")}
              </button>
            )}
          </>
        )}
        {bannerText && <div className="set-banners"><Banner kind={bannerKind}>{bannerText}</Banner></div>}
      </div>
    </div>
  );
}
