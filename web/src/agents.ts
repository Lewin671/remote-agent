import * as i18n from "./i18n";
import { Agent } from "./gen/remoteagent/v1/event_pb";

export interface AgentEntry {
  readonly agent: Agent;
  readonly name: string;
  readonly trustFolderHint: string | null;
  readonly permissionModeNotice: string | null;
  readonly showMaxEffort: boolean;
}

const CODEX_ENTRY: AgentEntry = {
  agent: Agent.CODEX,
  name: "Codex",
  trustFolderHint: null,
  permissionModeNotice: null,
  showMaxEffort: true,
};

const CLAUDE_ENTRY: AgentEntry = {
  agent: Agent.CLAUDE,
  name: "Claude Code",
  get trustFolderHint() { return i18n.t("dock.trust_folder"); },
  get permissionModeNotice() { return i18n.t("modes.permission_notice"); },
  showMaxEffort: false,
};

const ANTIGRAVITY_ENTRY: AgentEntry = {
  agent: Agent.ANTIGRAVITY,
  name: "Antigravity",
  trustFolderHint: null,
  permissionModeNotice: null,
  showMaxEffort: false,
};

const UNKNOWN_ENTRY: AgentEntry = {
  agent: -1 as Agent,
  get name() { return i18n.t("models.unknown_cli"); },
  trustFolderHint: null,
  permissionModeNotice: null,
  showMaxEffort: false,
};

const AGENTS: readonly AgentEntry[] = [CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY];

export const DEFAULT_AGENT: Agent = CODEX_ENTRY.agent;

export function agentInfo(a?: Agent | number | null): AgentEntry {
  if (a === undefined || a === null || a === Agent.UNSPECIFIED) {
    return CODEX_ENTRY;
  }
  const found = AGENTS.find((e) => e.agent === a);
  return found ?? { ...UNKNOWN_ENTRY, agent: a as Agent };
}

export function agentName(a?: Agent | number | null): string {
  return agentInfo(a).name;
}
