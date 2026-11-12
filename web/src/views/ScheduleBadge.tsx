import * as l10n from "../i18n";
import type { Remote } from "../remote";
import { unreadLabel } from "../schedules";

export function ScheduleBadge({ remote }: { remote: Remote }) {
  const attention = remote.scheduleBadge();
  return attention > 0 ? <span className="count schedule-attention-badge" aria-label={l10n.t("home.ios_badge_tasks", { count: attention })}>{unreadLabel(attention)}</span>
    : remote.scheduleNewResults() ? <span className="schedule-new-badge" role="img" aria-label={l10n.t("home.ios_new_results")} /> : null;
}
