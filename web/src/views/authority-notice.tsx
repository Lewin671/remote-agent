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
      action="处理"
      onAction={() => go("settings/security")}
    >
      恢复申请待处理
      {remote.recoveryReadyMs > 0n
        ? ` · 可生效时间：${futureTime(remote.recoveryReadyMs)}`
        : ""}
    </Banner>
  );
}
