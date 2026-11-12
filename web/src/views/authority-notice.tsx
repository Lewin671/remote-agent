import * as l10n from "../i18n";
import { futureTime } from "../model";
import { clientName } from "../devices";
import type { Remote } from "../remote";
import { Banner, useRemote } from "../ui";

// What the home page needs of the account's management: kept apart from the
// pages of settings and security, which are fetched when first opened.

export function authorityName(remote: Remote) {
  return clientName(
    remote.devices.get(remote.authorityDeviceId),
    remote.devices,
    remote.authorityDeviceId,
  );
}
export function RecoveryNotice({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  useRemote(remote);
  if (!remote.isAuthority || !remote.recoveryPending) return null;
  return (
    <Banner
      kind="warn"
      action={l10n.t("dock.handle")}
      onAction={() => go("settings/security")}
    >
      {l10n.t("security.recovery_request_pending")}{remote.recoveryReadyMs > 0n
        ? l10n.t("security.eligible_at", { value: String(futureTime(remote.recoveryReadyMs)) })
        : ""}
    </Banner>
  );
}
