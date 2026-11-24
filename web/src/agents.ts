import * as i18n from "./i18n";
import { Agent } from "./gen/remoteagent/v1/event_pb";

interface AgentEntry {
  readonly agent: Agent;
  readonly name: string;
  // Retained until i18n/messages/modes.json drops orphan keys modes.ask_permission_desc and modes.plan_propose_desc.
  readonly askModeDesc?: string;
  readonly planModeDesc?: string;
}

const CODEX_ENTRY: AgentEntry = {
  agent: Agent.CODEX,
  name: "Codex",
};

const CLAUDE_ENTRY: AgentEntry = {
  agent: Agent.CLAUDE,
  name: "Claude Code",
  get askModeDesc() { return i18n.t("modes.ask_permission_desc"); },
  get planModeDesc() { return i18n.t("modes.plan_propose_desc"); },
};

const ANTIGRAVITY_ENTRY: AgentEntry = {
  agent: Agent.ANTIGRAVITY,
  name: "Antigravity",
};

const UNKNOWN_ENTRY: AgentEntry = {
  agent: -1 as Agent,
  get name() { return i18n.t("models.unknown_cli"); },
};

const AGENTS: readonly AgentEntry[] = [CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY];

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
