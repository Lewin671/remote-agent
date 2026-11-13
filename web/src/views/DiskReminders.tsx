// 主页提醒 (ui-spec B1): one card for each online computer whose disk has
// fallen under 10 GB, at the top of the home, the tightest first. No close
// button: it goes when the space comes back. It opens that computer's status.

import { t } from "../i18n";
import { ResourcePressure } from "../gen/remoteagent/v1/local_pb";
import { ChevronRight, DiskIcon } from "../icons";
import { machineDiskReminder } from "../machine-status";
import { sizeText } from "../machine-status-view";
import { machineName } from "../model";
import type { Remote } from "../remote";

export function diskReminders(remote: Remote) {
  return [...remote.machines.values()]
    .flatMap((m) => {
      const disk = m.meta && machineDiskReminder(remote.onlineOf(m), m.meta);
      return disk ? [{ m, disk }] : [];
    })
    .sort((a, b) => Number(b.disk.pressure === ResourcePressure.CRITICAL) - Number(a.disk.pressure === ResourcePressure.CRITICAL) || Number(a.disk.availableBytes - b.disk.availableBytes));
}

export function DiskReminders({ remote, go }: { remote: Remote; go: (route: string) => void }) {
  return (
    <>
      {diskReminders(remote).map(({ m, disk }) => {
        const critical = disk.pressure === ResourcePressure.CRITICAL;
        const name = machineName(m);
        const size = sizeText(disk.availableBytes);
        return (
          <button key={m.id} className="ms-alert" onClick={() => go("machine-status/" + encodeURIComponent(m.id))}>
            <span className={"blk" + (critical ? " crit" : "")}><DiskIcon /></span>
            <span className="x">
              <b>{critical ? t("machine_status.home_critical") : t("machine_status.home_warning")}</b>
              <span>{critical ? t("machine_status.home_critical_detail", { computer: name, size }) : t("machine_status.home_warning_detail", { computer: name, size })}</span>
            </span>
            <ChevronRight className="chev" />
          </button>
        );
      })}
    </>
  );
}
