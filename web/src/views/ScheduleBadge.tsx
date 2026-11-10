import type { Remote } from "../remote";
import { unreadLabel } from "../schedules";

export function ScheduleBadge({ remote }: { remote: Remote }) {
  const attention = remote.scheduleBadge();
  return attention > 0 ? <span className="count schedule-attention-badge" aria-label={`${attention} 件需要处理`}>{unreadLabel(attention)}</span>
    : remote.scheduleNewResults() ? <span className="schedule-new-badge" role="img" aria-label="有新结果" /> : null;
}
