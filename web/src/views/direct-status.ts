import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import * as l10n from "../i18n";
import { getCore } from "../core/runtime";
import { renderText as renderCoreText } from "../core/text";
import {
  type ConnCheckDirectRowView,
  ConnectionGroupStage,
  type ConnectionGroupView,
  DirectPathChoice,
  DirectRouteReason,
  DirectRouteReportSchema,
  DirectRouteState,
  DirectViewsRequestSchema,
  DirectViewsResponseSchema,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import type { Mark } from "../ui";
import type { DirectEngine, DirectStatus } from "../transport/direct-engine";

// Mirrors clientcore/domain/machine/direct.go so web, Android, and iOS share
// the same Connection group and ConnCheck Direct row semantics.

type Purpose = "file" | "read";
const FILE_PURPOSE: Purpose = "file";
const READ_PURPOSE: Purpose = "read";

export type DirectKind = "direct" | "relay" | "trying" | "off" | "unsupported";

export interface DirectView {
  kind: DirectKind;
  /** The state graphic at the head of the status row. */
  mark: Mark;
  /** The state word; ink2, never colored. */
  word: string;
  /** Second line of the status row: which single purpose goes direct, or why relay is in use. */
  sub?: string;
  /** Active read latency text when available. */
  metric?: string;
  /** The one line the connection check summarizes. */
  summary?: string;
  /** Measured speeds only, one per purpose. */
  speeds: { key: Purpose; name: string; value: string }[];
  /** Toggle and diagnostics controls driven by clientcore. */
  toggleVisible: boolean;
  toggleEnabled: boolean;
  toggleChecked: boolean;
  showDiagnosticsEntry: boolean;
  footer: string;
}

export interface ConnCheckDirectView {
  visible: boolean;
  mark: Mark;
  label: string;
  status: string;
  detail?: string;
  metric?: string;
  summary?: string;
}

function toCoreReason(reason: DirectStatus["reason"]): DirectRouteReason {
  switch (reason) {
    case "disabled": return DirectRouteReason.DISABLED;
    case "peer_unsupported": return DirectRouteReason.PEER_UNSUPPORTED;
    case "no_response": return DirectRouteReason.NO_RESPONSE;
    case "no_traversal": return DirectRouteReason.NO_TRAVERSAL;
    case "failed": return DirectRouteReason.FAILED;
    case "at_cap": return DirectRouteReason.AT_CAP;
    case "security_pending": return DirectRouteReason.SECURITY_PENDING;
    case "starting": return DirectRouteReason.STARTING;
    default: return DirectRouteReason.UNSPECIFIED;
  }
}

function toCoreState(state: DirectStatus["state"]): DirectRouteState {
  switch (state) {
    case "unsupported": return DirectRouteState.UNSUPPORTED;
    case "connected": return DirectRouteState.CONNECTED;
    case "connecting": return DirectRouteState.CONNECTING;
    case "none": return DirectRouteState.NONE;
  }
}

function stageToMark(stage: ConnectionGroupStage): Mark {
  switch (stage) {
    case ConnectionGroupStage.DIRECT: return "online";
    case ConnectionGroupStage.CONNECTING:
    case ConnectionGroupStage.MEASURING: return "run";
    default: return "off";
  }
}

function stageToKind(stage: ConnectionGroupStage, toggleVisible: boolean): DirectKind {
  if (!toggleVisible) return "unsupported";
  switch (stage) {
    case ConnectionGroupStage.DIRECT: return "direct";
    case ConnectionGroupStage.CONNECTING:
    case ConnectionGroupStage.MEASURING: return "trying";
    case ConnectionGroupStage.DISABLED: return "off";
    case ConnectionGroupStage.UNSUPPORTED: return "unsupported";
    default: return "relay";
  }
}

export function buildCoreDirectViews(
  s: DirectStatus,
  online = true,
  prefEnabled?: boolean,
): { connection: ConnectionGroupView; connCheckDirect: ConnCheckDirectRowView } {
  const enabled = prefEnabled ?? (s.reason !== "disabled");
  const activeRtt = s.read === "direct"
    ? s.readMs.direct
    : s.state === "connected"
      ? s.readMs.relay
      : undefined;
  const report = create(DirectRouteReportSchema, {
    enabled,
    capable: s.state !== "unsupported",
    state: toCoreState(s.state),
    reason: toCoreReason(s.reason),
    fileRoute: s.file === "direct" ? DirectPathChoice.DIRECT : DirectPathChoice.RELAY,
    readRoute: s.read === "direct" ? DirectPathChoice.DIRECT : DirectPathChoice.RELAY,
    warming: false,
    compared: Boolean(s.readSlow || s.fileSlow),
    readSlow: Boolean(s.readSlow),
    fileSlow: Boolean(s.fileSlow),
    hasDiagnostics: s.reason === "no_response" || s.reason === "no_traversal" || s.reason === "failed",
    hasDirectFileMibps: s.fileMiBps.direct !== undefined,
    directFileMibps: s.fileMiBps.direct ?? 0,
    hasRelayFileMibps: s.fileMiBps.relay !== undefined,
    relayFileMibps: s.fileMiBps.relay ?? 0,
    hasDirectReadMs: s.readMs.direct !== undefined,
    directReadMs: s.readMs.direct !== undefined ? Math.round(s.readMs.direct) : 0,
    hasRelayReadMs: s.readMs.relay !== undefined,
    relayReadMs: s.readMs.relay !== undefined ? Math.round(s.readMs.relay) : 0,
    hasActiveRttMs: activeRtt !== undefined,
    activeRttMs: activeRtt !== undefined ? Math.round(activeRtt) : 0,
  });
  const req = create(DirectViewsRequestSchema, {
    online,
    prefEnabled: enabled,
    report,
  });
  const raw = getCore().buildDirectViews(toBinary(DirectViewsRequestSchema, req));
  const resp = fromBinary(DirectViewsResponseSchema, raw);
  return {
    connection: resp.connection!,
    connCheckDirect: resp.connCheckDirect!,
  };
}

function buildSpeeds(s: DirectStatus, cg: ConnectionGroupView): DirectView["speeds"] {
  const out: DirectView["speeds"] = [];
  const fileValue = renderCoreText(cg.fileSpeedText);
  const readValue = renderCoreText(cg.readLatencyText);
  if (fileValue) {
    out.push({ key: FILE_PURPOSE, name: l10n.t("transport.speed_file"), value: fileValue });
  } else if (s.state === "connected") {
    out.push({ key: FILE_PURPOSE, name: l10n.t("transport.speed_file"), value: l10n.t("transport.file_speed_pending") });
  }
  if (readValue) {
    out.push({ key: READ_PURPOSE, name: l10n.t("transport.speed_read"), value: readValue });
  }
  return out;
}

/** What a computer's direct path looks like to the user right now, computed by clientcore. */
export function directView(s: DirectStatus, online = true, prefEnabled?: boolean): DirectView {
  const { connection: cg, connCheckDirect: row } = buildCoreDirectViews(s, online, prefEnabled);
  const word = s.state === "unsupported" ? l10n.t("transport.status_unsupported") : renderCoreText(cg.statusText);
  const sub = renderCoreText(cg.detailText) || undefined;
  const metric = renderCoreText(cg.metricText) || undefined;
  const footer = renderCoreText(cg.footerText) || l10n.t("transport.footer");
  const rowStatus = renderCoreText(row.statusText);
  const rowDetail = renderCoreText(row.detailText) || undefined;
  const summary = !row.visible
    ? undefined
    : [rowStatus, rowDetail].filter(Boolean).join(" · ") || undefined;

  return {
    kind: stageToKind(cg.stage, cg.toggleVisible),
    mark: stageToMark(cg.stage),
    word,
    sub,
    metric,
    summary,
    speeds: buildSpeeds(s, cg),
    toggleVisible: cg.toggleVisible,
    toggleEnabled: cg.toggleEnabled,
    toggleChecked: cg.toggleChecked,
    showDiagnosticsEntry: cg.showDiagnosticsEntry,
    footer,
  };
}

export function connCheckDirectView(s: DirectStatus, online = true, prefEnabled?: boolean): ConnCheckDirectView {
  const { connCheckDirect: row } = buildCoreDirectViews(s, online, prefEnabled);
  const label = renderCoreText(row.labelText) || l10n.t("transport.direct");
  const status = renderCoreText(row.statusText);
  const detail = renderCoreText(row.detailText) || undefined;
  const metric = renderCoreText(row.metricText) || undefined;
  const summary = !row.visible ? undefined : [status, detail, metric].filter(Boolean).join(" · ") || undefined;
  return {
    visible: row.visible,
    mark: stageToMark(row.stage),
    label,
    status,
    detail,
    metric,
    summary,
  };
}

/** The state shown for a computer whose direct path was only just turned on
 * and has not reported anything yet: trying, which is what is happening. */
export function directTrying(): DirectView {
  return directView({ state: "connecting", file: "relay", read: "relay", fileMiBps: {}, readMs: {} });
}

/** The connection check's note under its list: the old explanation of a
 * latency, then (once, not on every computer's line) that this browser cannot do it. */
export function connFooter(engine: DirectEngine, ids: string[]): string {
  const unsupported = ids.some(id => directView(engine.status(id)).kind === "unsupported");
  const base = l10n.t("connection.latency_is_one_round_trip_the_server_row_measures");
  return unsupported ? `${base}${base.endsWith(".") ? " " : ""}${l10n.t("transport.conn_footer_unsupported")}` : base;
}
