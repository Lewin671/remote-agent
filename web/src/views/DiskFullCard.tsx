// 启动失败 (ui-spec B3): what a failed start shows when the computer's
// disk is full, in place of the raw "no space left on device".

import { t } from "../i18n";

export function DiskFullCard({ onUsage, onRetry }: { onUsage: () => void; onRetry: () => void }) {
  return (
    <div className="ms-fail-card" role="alert">
      <b>{t("machine_status.disk_full")}</b>
      <p>{t("machine_status.launch_failed_body")}</p>
      <div className="bs">
        <button onClick={onUsage}>{t("machine_status.view_usage")}</button>
        <button onClick={onRetry}>{t("common.retry")}</button>
      </div>
    </div>
  );
}
