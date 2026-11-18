import { routeForMachineStatus, useCoreHost, useSettingsView, type Go } from "../core/host";
import { renderText } from "../core/text";
import { DiskIcon } from "../icons";
import type { Remote } from "../remote";

/** Disk reminders from Core's SettingsViewModel; critical alerts appear first. */
export function DiskReminders({ remote, go }: { remote: Remote; go: Go }) {
  const host = useCoreHost(remote);
  const { diskReminders } = useSettingsView(host);
  if (!diskReminders.length) return null;
  return (
    <>
      {diskReminders.map((d) => (
        <button
          key={d.machineId}
          type="button"
          className="ms-alert"
          onClick={() => go(routeForMachineStatus(d.machineId))}
        >
          <span className={d.critical ? "blk crit" : "blk"} aria-hidden="true">
            <DiskIcon />
          </span>
          <span className="x">
            <b>{renderText(d.title)}</b>
            {renderText(d.detail) && <span>{renderText(d.detail)}</span>}
          </span>
        </button>
      ))}
    </>
  );
}
