import * as l10n from "../i18n";
import type { Mark } from "../ui";
import type { DirectEngine, DirectStatus } from "../transport/direct-engine";

// The direct path in words (design-system §6 「连接」组). One place says it, so
// the computer's settings page and the connection check never disagree: the
// settings page draws the whole of it, the connection check one line of it.

// One decimal below 100, whole numbers from there; 99.96 is already 100.
const mibps = (n: number) => (Math.round(n * 10) / 10 >= 100 ? String(Math.round(n)) : n.toFixed(1));
const ms = (n: number) => String(Math.round(n));

type Purpose = "file" | "read";

export type DirectKind = "direct" | "relay" | "trying" | "none" | "off" | "unsupported";

export interface DirectView {
  kind: DirectKind;
  /** The state graphic at the head of the status row. */
  mark: Mark;
  /** The state word; ink2, never colored. */
  word: string;
  /** Right of the word: which purposes go direct. */
  value?: string;
  /** Second line of the status row: why, or what is being compared. */
  sub?: string;
  /** The one line the connection check puts under a computer's name. */
  summary?: string;
  /** Measured speeds only, one per purpose; never a placeholder. */
  speeds: { key: Purpose; name: string; value: string }[];
}

function reason(r: NonNullable<DirectStatus["reason"]>): string | undefined {
  switch (r) {
    case "peer_unsupported": return l10n.t("transport.reason_peer_unsupported");
    case "no_response": return l10n.t("transport.reason_no_response");
    case "no_traversal": return l10n.t("transport.reason_no_traversal");
    case "failed": return l10n.t("transport.reason_failed");
    case "disabled": return undefined;
  }
}

/** Whether the relay measured no slower than the direct path: at least one
 * purpose was measured on both paths, and on none of them is direct ahead. */
export function relayNotSlower(s: Pick<DirectStatus, "fileMiBps" | "readMs">): boolean {
  const { direct: fd, relay: fr } = s.fileMiBps, { direct: rd, relay: rr } = s.readMs;
  const file = fd !== undefined && fr !== undefined, read = rd !== undefined && rr !== undefined;
  return (file || read) && (!file || fr! >= fd!) && (!read || rr! <= rd!);
}

function speeds(s: DirectStatus): DirectView["speeds"] {
  const out: DirectView["speeds"] = [];
  const f = s.fileMiBps, r = s.readMs;
  const purposeName = (key: Purpose) => (key === "file" ? l10n.t("transport.speed_file") : l10n.t("transport.speed_read"));
  if (f.direct !== undefined && f.relay !== undefined) out.push({ key: "file", name: purposeName("file"), value: l10n.t("transport.file_speed_both", { direct: mibps(f.direct), relay: mibps(f.relay) }) });
  else if (f.direct !== undefined) out.push({ key: "file", name: purposeName("file"), value: l10n.t("transport.file_speed_direct", { direct: mibps(f.direct) }) });
  else if (f.relay !== undefined) out.push({ key: "file", name: purposeName("file"), value: l10n.t("transport.file_speed_relay", { relay: mibps(f.relay) }) });
  if (r.direct !== undefined && r.relay !== undefined) out.push({ key: "read", name: purposeName("read"), value: l10n.t("transport.read_latency_both", { direct: ms(r.direct), relay: ms(r.relay) }) });
  else if (r.direct !== undefined) out.push({ key: "read", name: purposeName("read"), value: l10n.t("transport.read_latency_direct", { direct: ms(r.direct) }) });
  else if (r.relay !== undefined) out.push({ key: "read", name: purposeName("read"), value: l10n.t("transport.read_latency_relay", { relay: ms(r.relay) }) });
  return out;
}

/** What a computer's direct path looks like to the user right now. */
export function directView(s: DirectStatus): DirectView {
  if (s.state === "unsupported") return { kind: "unsupported", mark: "off", word: l10n.t("transport.status_unsupported"), speeds: [] };
  if (s.state === "none" && s.reason === "disabled") return { kind: "off", mark: "off", word: "", summary: l10n.t("transport.summary_off"), speeds: [] };
  const shown = speeds(s);
  if (s.state === "connected") {
    const file = s.file === "direct", read = s.read === "direct";
    if (file || read) {
      const word = l10n.t("transport.status_direct");
      const value = file && read ? l10n.t("transport.use_both") : file ? l10n.t("transport.use_file") : l10n.t("transport.use_read");
      return { kind: "direct", mark: "online", word, value, summary: `${word} · ${value}`, speeds: shown };
    }
    const word = l10n.t("transport.status_relay");
    return { kind: "relay", mark: "idle", word, sub: relayNotSlower(s) ? l10n.t("transport.relay_faster") : l10n.t("transport.comparing"), summary: word, speeds: shown };
  }
  if (s.state === "connecting") {
    const word = l10n.t("transport.status_connecting");
    return { kind: "trying", mark: "run", word, summary: word, speeds: shown };
  }
  const word = l10n.t("transport.status_none");
  const why = s.reason ? reason(s.reason) : undefined;
  return { kind: "none", mark: "off", word, sub: why, summary: why ? `${word} · ${why}` : word, speeds: shown };
}

/** The state shown for a computer whose direct path was only just turned on
 * and has not reported anything yet: trying, which is what is happening. */
export function directTrying(): DirectView {
  const word = l10n.t("transport.status_connecting");
  return { kind: "trying", mark: "run", word, summary: word, speeds: [] };
}

/** Whether the footer of the settings group has to explain measured speeds. */
export function footerText(view: DirectView): string {
  return view.speeds.length ? l10n.t("transport.footer_speeds") : l10n.t("transport.footer");
}

/** The connection check's note under its list: the old explanation of a
 * latency, then that a computer's is measured through the relay, then (once,
 * not on every computer's line) that this browser cannot do it. */
export function connFooter(engine: DirectEngine, ids: string[]): string {
  const unsupported = ids.some((id) => directView(engine.status(id)).kind === "unsupported");
  const sentences = [l10n.t("connection.latency_is_one_round_trip_the_server_row_measures"), ...(unsupported ? [l10n.t("transport.conn_footer_unsupported")] : [])];
  // Chinese sentences end in a full stop and need no space between them.
  return sentences.join(l10n.localeStore.snapshot().startsWith("zh") ? "" : " ");
}
