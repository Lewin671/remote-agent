import * as l10n from "../i18n";
import { ConnectionGroupStage, type ConnCheckDirectRowView } from "../gen/remoteagent/core/v1/settings_view_pb";
import { renderText as renderCoreText } from "../core/text";
import type { Mark } from "../ui";

export function stageToMark(stage: ConnectionGroupStage): Mark {
  switch (stage) {
    case ConnectionGroupStage.DIRECT: return "online";
    case ConnectionGroupStage.CONNECTING:
    case ConnectionGroupStage.MEASURING: return "run";
    default: return "off";
  }
}

export function directRowTrailing(row: ConnCheckDirectRowView): string {
  return renderCoreText(row.statusText);
}

export function connFooter(unsupported: boolean): string {
  const base = l10n.t("connection.latency_is_one_round_trip_the_server_row_measures");
  return unsupported ? `${base}${base.endsWith(".") ? " " : ""}${l10n.t("transport.conn_footer_unsupported_browser")}` : base;
}
