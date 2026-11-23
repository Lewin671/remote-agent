import * as l10n from "../i18n";
import { useCoreHost, useSettingsView } from "../core/host";
import { futureTime } from "../model";
import type { Remote } from "../remote";
import { Banner } from "../ui";

// What the home page needs of the account's management: kept apart from the
// pages of settings and security, which are fetched when first opened.

export function RecoveryNotice({
  remote,
  go,
}: {
  remote: Remote;
  go: (r: string) => void;
}) {
  const vm = useSettingsView(useCoreHost(remote));
  if (!vm.isManager || !vm.recoveryPending) return null;
  return (
    <Banner
      kind="warn"
      action={l10n.t("dock.handle")}
      onAction={() => go("settings/security")}
    >
      {l10n.t("security.recovery_request_pending")}{vm.recoveryReadyMs > 0n
        ? l10n.t("security.eligible_at", { value: String(futureTime(vm.recoveryReadyMs)) })
        : ""}
    </Banner>
  );
}
