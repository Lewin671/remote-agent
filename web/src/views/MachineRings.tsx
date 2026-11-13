// The entrance to 电脑状态 (ui-spec A2): three rings at the top of a
// computer's page, CPU, memory and the disk's free space, asked for every
// ten seconds while the page is in front. The whole card opens the status.

import { t } from "../i18n";
import { CpuIcon, DiskIcon, MemoryIcon } from "../icons";
import { ringsOf, type Ring } from "../machine-status-view";
import type { Remote } from "../remote";
import { useMachineStatus } from "./machine-status-hooks";

const R = 23;
const CIRCUMFERENCE = 2 * Math.PI * R;

const NAMES = {
  cpu: () => t("machine_status.cpu"),
  memory: () => t("machine_status.memory"),
  disk: () => t("machine_status.disk_available"),
} as const;

function RingFace({ ring, kind }: { ring?: Ring; kind: Ring["kind"] }) {
  const Icon = kind === "cpu" ? CpuIcon : kind === "memory" ? MemoryIcon : DiskIcon;
  const text = ring?.text ?? "—";
  return (
    <span className={"ms-ring" + (ring?.tone ? " " + ring.tone : "")}>
      <span className="w">
        <svg viewBox="0 0 54 54" aria-hidden>
          <circle className="t" cx="27" cy="27" r={R} />
          {ring && <circle className="p" cx="27" cy="27" r={R} strokeDasharray={`${ring.ratio * CIRCUMFERENCE} ${CIRCUMFERENCE}`} />}
        </svg>
        <span className="g"><Icon /></span>
      </span>
      <b aria-hidden>{text}</b>
      <small aria-hidden>{NAMES[kind]()}</small>
      <span className="ms-sr">{t("machine_status.a11y_ring", { name: NAMES[kind](), value: text })}</span>
    </span>
  );
}

/** The card is not drawn for a daemon that cannot report; an offline computer's is
 * only the tracks, with a dash, faded, and does not open. */
export function MachineRings({ remote, id, go }: { remote: Remote; id: string; go: (route: string) => void }) {
  const m = remote.machines.get(id);
  const status = useMachineStatus(remote, id, 10_000);
  if (!m?.meta?.capabilities?.machineStatus) return null;
  const online = remote.onlineOf(m);
  const rings = status.last && online ? ringsOf(status.last) : undefined;
  const faces: Ring["kind"][] = ["cpu", "memory", "disk"];
  return (
    <button className={"ms-rings" + (online ? "" : " off")} disabled={!online} onClick={() => go("machine-status/" + encodeURIComponent(id))}>
      {faces.map((kind) => <RingFace key={kind} kind={kind} ring={rings?.find((r) => r.kind === kind)} />)}
    </button>
  );
}
