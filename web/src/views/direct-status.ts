import { create } from "@bufbuild/protobuf";
import * as l10n from "../i18n";
import {
  type ConnCheckDirectRowView,
  ConnectionGroupStage,
  DirectPathChoice,
  DirectRouteReason,
  DirectRouteReportSchema,
  DirectRouteState,
  type DirectRouteReport,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { renderText as renderCoreText } from "../core/text";
import type { Mark } from "../ui";
import type { DirectStatus } from "../transport/direct-engine";

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

export function stageToMark(stage: ConnectionGroupStage): Mark {
  switch (stage) {
    case ConnectionGroupStage.DIRECT: return "online";
    case ConnectionGroupStage.CONNECTING:
    case ConnectionGroupStage.MEASURING: return "run";
    default: return "off";
  }
}

export function buildDirectRouteReport(
  s: DirectStatus,
  enabled: boolean,
  hasDiagnostics = s.reason === "peer_unsupported" || s.reason === "no_response" || s.reason === "no_traversal" || s.reason === "failed",
): DirectRouteReport {
  const activeRtt = s.read === "direct" ? s.readMs.direct : s.readMs.relay;
  return create(DirectRouteReportSchema, {
    enabled,
    capable: s.state !== "unsupported",
    daemonDisabled: s.reason === "disabled" && enabled,
    state: toCoreState(s.state),
    reason: toCoreReason(s.reason),
    fileRoute: s.file === "direct" ? DirectPathChoice.DIRECT : DirectPathChoice.RELAY,
    readRoute: s.read === "direct" ? DirectPathChoice.DIRECT : DirectPathChoice.RELAY,
    warming: false,
    compared: Boolean(s.readSlow || s.fileSlow),
    readSlow: Boolean(s.readSlow),
    fileSlow: Boolean(s.fileSlow),
    hasDiagnostics,
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
}

export function directRowTrailing(row: ConnCheckDirectRowView): string {
  return renderCoreText(row.statusText);
}

export function connFooter(unsupported: boolean): string {
  const base = l10n.t("connection.latency_is_one_round_trip_the_server_row_measures");
  return unsupported ? `${base}${base.endsWith(".") ? " " : ""}${l10n.t("transport.conn_footer_unsupported_browser")}` : base;
}
