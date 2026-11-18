import * as l10n from "./i18n";
import * as i18n from "./i18n";
import { Agent, ApprovalMode, SendMode } from "./gen/remoteagent/v1/event_pb";
import type { DaemonSettings } from "./gen/remoteagent/v1/local_pb";

export interface AgentEntry {
  readonly agent: Agent;
  readonly id: string;
  readonly name: string;
  modelLabel(id: string): string;
  readonly toolLabelFromItemType: boolean;
  readonly planClearContext: boolean;
  readonly planKeepLabel: string;
  readonly planApproveLabel: string;
  readonly planNote: string | null;
  readonly standardContextOption: boolean;
  readonly autoMayBeUnavailable: boolean;
  readonly trustFolderHint: string | null;
  readonly usageHint: string | null;
  readonly reconcilesUnclaimedInput: boolean;
  readonly sessionNeedsModelList: boolean;
  readonly switchesModelInSession: boolean;
  readonly forks: boolean;
  readonly steersDuringTurn: boolean;
  defaults(settings?: DaemonSettings): { model: string; effort: string; mode: ApprovalMode };

  readonly askModeDesc: string;
  readonly planModeDesc: string;
  readonly permissionModeNotice: string | null;
  readonly showMaxEffort: boolean;
}

function gptSlugLabel(id: string): string {
  return id
    .split("-")
    .map((w) => (/^gpt$/i.test(w) ? "GPT" : w.charAt(0).toUpperCase() + w.slice(1)))
    .join("-");
}

const CODEX_ENTRY: AgentEntry = {
  agent: Agent.CODEX,
  id: "codex",
  name: "Codex",
  modelLabel(id: string): string {
    return gptSlugLabel(id);
  },
  toolLabelFromItemType: false,
  planClearContext: true,
  get planKeepLabel() { return i18n.t("dock.plan_keep"); },
  get planApproveLabel() { return i18n.t("approval.approve_execute"); },
  planNote: null,
  standardContextOption: false,
  autoMayBeUnavailable: false,
  trustFolderHint: null,
  usageHint: null,
  reconcilesUnclaimedInput: false,
  sessionNeedsModelList: false,
  switchesModelInSession: true,
  forks: true,
  steersDuringTurn: true,
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.defaultModel ?? "",
      effort: settings?.defaultEffort ?? "",
      mode: settings?.defaultMode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  get askModeDesc() { return i18n.t("modes.ask_desc"); },
  get planModeDesc() { return i18n.t("modes.plan_desc"); },
  permissionModeNotice: null,
  showMaxEffort: true,
};

const CLAUDE_ENTRY: AgentEntry = {
  agent: Agent.CLAUDE,
  id: "claude",
  name: "Claude Code",
  modelLabel(id: string): string {
    return id;
  },
  toolLabelFromItemType: true,
  planClearContext: false,
  get planKeepLabel() { return i18n.t("modes.plan_edit"); },
  get planApproveLabel() { return i18n.t("approval.approve"); },
  get planNote() { return i18n.t("dock.claude_plan_note"); },
  standardContextOption: true,
  autoMayBeUnavailable: true,
  get trustFolderHint() { return i18n.t("dock.trust_folder"); },
  usageHint: null,
  reconcilesUnclaimedInput: true,
  sessionNeedsModelList: true,
  switchesModelInSession: true,
  forks: true,
  steersDuringTurn: true,
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.claude?.model ?? "",
      effort: settings?.claude?.effort ?? "",
      mode: settings?.claude?.mode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  get askModeDesc() { return i18n.t("modes.ask_permission_desc"); },
  get planModeDesc() { return i18n.t("modes.plan_propose_desc"); },
  get permissionModeNotice() { return i18n.t("modes.permission_notice"); },
  showMaxEffort: false,
};

const ANTIGRAVITY_ENTRY: AgentEntry = {
  agent: Agent.ANTIGRAVITY,
  id: "agy",
  name: "Antigravity",
  modelLabel(id: string): string {
    return id;
  },
  toolLabelFromItemType: false,
  planClearContext: false,
  get planKeepLabel() { return i18n.t("modes.plan_edit"); },
  get planApproveLabel() { return i18n.t("approval.approve"); },
  get planNote() { return i18n.t("dock.agy_plan_note"); },
  standardContextOption: false,
  autoMayBeUnavailable: true,
  trustFolderHint: null,
  usageHint: null,
  reconcilesUnclaimedInput: false,
  sessionNeedsModelList: true,
  switchesModelInSession: false,
  forks: false,
  steersDuringTurn: false,
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.antigravity?.model ?? "",
      effort: settings?.antigravity?.effort ?? "",
      mode: settings?.antigravity?.mode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  get askModeDesc() { return i18n.t("modes.agy_ask_desc"); },
  get planModeDesc() { return i18n.t("modes.agy_plan_desc"); },
  permissionModeNotice: null,
  showMaxEffort: false,
};

const UNKNOWN_ENTRY: AgentEntry = {
  agent: -1 as Agent,
  id: "",
  get name() { return l10n.t("models.unknown_cli"); },
  modelLabel(id: string): string {
    return id;
  },
  toolLabelFromItemType: false,
  planClearContext: false,
  get planKeepLabel() { return i18n.t("modes.plan_edit"); },
  get planApproveLabel() { return i18n.t("approval.approve"); },
  planNote: null,
  standardContextOption: false,
  autoMayBeUnavailable: false,
  trustFolderHint: null,
  usageHint: null,
  reconcilesUnclaimedInput: false,
  sessionNeedsModelList: false,
  switchesModelInSession: false,
  forks: false,
  steersDuringTurn: false,
  defaults() {
    return {
      model: "",
      effort: "",
      mode: ApprovalMode.UNSPECIFIED,
    };
  },
  get askModeDesc() { return i18n.t("modes.ask_generic_desc"); },
  get planModeDesc() { return i18n.t("modes.plan_propose_desc"); },
  permissionModeNotice: null,
  showMaxEffort: false,
};

export const AGENTS: readonly AgentEntry[] = [CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY];

export const DEFAULT_AGENT: Agent = CODEX_ENTRY.agent;

export function isKnownAgent(a: unknown): a is Agent {
  return AGENTS.some((e) => e.agent === a);
}

export function agentInfo(a?: Agent | number | null): AgentEntry {
  if (a === undefined || a === null || a === Agent.UNSPECIFIED) {
    return CODEX_ENTRY;
  }
  const found = AGENTS.find((e) => e.agent === a);
  return found ?? { ...UNKNOWN_ENTRY, agent: a as Agent };
}

export function agentById(id?: string | null): AgentEntry {
  if (!id) return UNKNOWN_ENTRY;
  const found = AGENTS.find((e) => e.id === id);
  return found ?? UNKNOWN_ENTRY;
}

export function agentName(a?: Agent | number | null): string {
  return agentInfo(a).name;
}

export function defaultsFor(
  settings: DaemonSettings | undefined,
  agent: Agent,
): { model: string; effort: string; mode: ApprovalMode } {
  return agentInfo(agent).defaults(settings);
}

/** Running next-turn-only CLIs use the existing queue operation. */
export function sendModeFor(agent: Agent, requested: SendMode, running: boolean): SendMode {
  return running && !agentInfo(agent).steersDuringTurn ? SendMode.QUEUE : requested;
}
