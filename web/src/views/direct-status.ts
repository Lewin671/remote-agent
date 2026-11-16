import * as l10n from "../i18n";
import type { DirectEngine, DirectStatus } from "../transport/direct-engine";

const mibps = (n: number) => (n >= 100 ? String(Math.round(n)) : n.toFixed(1));
const ms = (n: number) => String(Math.round(n));

function reason(r: NonNullable<DirectStatus["reason"]>): string {
  switch (r) {
    case "disabled": return l10n.t("transport.reason_disabled");
    case "peer_unsupported": return l10n.t("transport.reason_peer_unsupported");
    case "no_response": return l10n.t("transport.reason_no_response");
    case "no_traversal": return l10n.t("transport.reason_no_traversal");
    case "failed": return l10n.t("transport.reason_failed");
  }
}

/** The direct path in words, shared by every screen that shows it so they
 * never disagree: one headline (state, then the reason or the path each
 * purpose uses now) and the speeds that were actually measured. */
export function directLines(s: DirectStatus): { head: string; speeds: string[]; lines: string[] } {
  if (s.state === "unsupported") {
    const head = l10n.t("transport.status_unsupported");
    return { head, speeds: [], lines: [head] };
  }
  const head =
    s.state === "connected"
      ? [
          l10n.t("transport.status_connected"),
          s.file === "direct" ? l10n.t("transport.file_direct") : l10n.t("transport.file_relay"),
          s.read === "direct" ? l10n.t("transport.read_direct") : l10n.t("transport.read_relay"),
        ]
      : s.state === "connecting"
        ? [l10n.t("transport.status_connecting")]
        : [l10n.t("transport.status_none"), ...(s.reason ? [reason(s.reason)] : [])];
  const speeds: string[] = [];
  const f = s.fileMiBps, r = s.readMs;
  if (f.direct !== undefined && f.relay !== undefined) speeds.push(l10n.t("transport.file_speed_both", { direct: mibps(f.direct), relay: mibps(f.relay) }));
  else if (f.direct !== undefined) speeds.push(l10n.t("transport.file_speed_direct", { direct: mibps(f.direct) }));
  else if (f.relay !== undefined) speeds.push(l10n.t("transport.file_speed_relay", { relay: mibps(f.relay) }));
  if (r.direct !== undefined && r.relay !== undefined) speeds.push(l10n.t("transport.read_latency_both", { direct: ms(r.direct), relay: ms(r.relay) }));
  else if (r.direct !== undefined) speeds.push(l10n.t("transport.read_latency_direct", { direct: ms(r.direct) }));
  else if (r.relay !== undefined) speeds.push(l10n.t("transport.read_latency_relay", { relay: ms(r.relay) }));
  return { head: head.join(" · "), speeds, lines: [head.join(" · "), ...speeds] };
}

/** Whether any listed computer has a measured speed to explain. */
export function speedsShown(engine: DirectEngine, ids: string[]): boolean {
  return ids.some((id) => directLines(engine.status(id)).speeds.length !== 0);
}
