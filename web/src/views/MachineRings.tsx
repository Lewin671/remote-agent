import { MachinePollMode, routeForMachineStatus, useCoreHost, useMachineStatusView } from "../core/host";
import { renderText } from "../core/text";
import {
  MachineRingKind,
  MachineRingTone,
  type MachineRingView,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import { t } from "../i18n";
import { CpuIcon, DiskIcon, MemoryIcon } from "../icons";
import type { Remote } from "../remote";

const R = 23;
const C = 2 * Math.PI * R;

function ringToneClass(tone: MachineRingTone): string {
  switch (tone) {
    case MachineRingTone.WARN: return " warn";
    case MachineRingTone.CRITICAL: return " crit";
    default: return "";
  }
}

function ringIcon(kind: MachineRingKind) {
  switch (kind) {
    case MachineRingKind.CPU: return <CpuIcon />;
    case MachineRingKind.MEMORY: return <MemoryIcon />;
    case MachineRingKind.DISK: return <DiskIcon />;
    default: return null;
  }
}

function RingTile({ r }: { r: MachineRingView }) {
  const ratio = r.hasValue ? Math.min(1, Math.max(0, r.fraction)) : 0;
  const fill = ratio * C;
  return (
    <div className={`ms-ring${ringToneClass(r.tone)}`}>
      <span className="ms-sr">{renderText(r.a11yLabel)}</span>
      <span className="w" aria-hidden="true">
        <svg viewBox="0 0 54 54">
          <circle className="t" cx="27" cy="27" r={R} />
          {fill > 0 && (
            <circle
              className="p"
              cx="27"
              cy="27"
              r={R}
              strokeDasharray={`${fill.toFixed(1)} ${C.toFixed(1)}`}
            />
          )}
        </svg>
        <span className="g">{ringIcon(r.kind)}</span>
      </span>
      <b>{renderText(r.valueText)}</b>
      <small>{renderText(r.label)}</small>
    </div>
  );
}

/** Three status rings on the computer page opening the live MachineStatusPage. */
export function MachineRings({ remote, id, go }: { remote: Remote; id: string; go: (route: string) => void }) {
  const host = useCoreHost(remote);
  const vm = useMachineStatusView(host, id, MachinePollMode.OVERVIEW);
  if (!vm.found || !vm.supported || !vm.rings.length) return null;
  const disabled = !vm.online;
  return (
    <div className="ms-rings-wrap">
      <button
        type="button"
        className={`ms-rings${disabled || vm.dim ? " off" : ""}`}
        disabled={disabled}
        aria-label={t("machine_status.title")}
        onClick={() => go(routeForMachineStatus(id))}
      >
        {vm.rings.map((r, i) => <RingTile key={r.kind || i} r={r} />)}
      </button>
    </div>
  );
}
