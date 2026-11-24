import { ConnectionGroupStage } from "../gen/remoteagent/core/v1/settings_view_pb";
import type { Mark } from "../ui";

export function stageToMark(stage: ConnectionGroupStage): Mark {
  switch (stage) {
    case ConnectionGroupStage.DIRECT: return "online";
    case ConnectionGroupStage.CONNECTING:
    case ConnectionGroupStage.MEASURING: return "run";
    default: return "off";
  }
}
