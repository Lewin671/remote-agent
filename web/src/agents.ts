import * as l10n from "./i18n";
import * as i18n from "./i18n";
import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import type { AgentDefaults, DaemonSettings } from "./gen/remoteagent/v1/local_pb";
import type { MachineMeta } from "./gen/remoteagent/v1/remote_pb";

export interface QuotaWindowDescriptor {
  readonly label: string;
  readonly group: string | null;
  readonly pool?: string;
}

export interface AgentEntry {
  readonly agent: Agent;
  readonly id: string;
  readonly name: string;
  readonly quotaWindows: readonly string[];
  quotaWindow(name: string): QuotaWindowDescriptor;
  planName(raw: string): string;
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
  defaults(settings?: DaemonSettings): { model: string; effort: string; mode: ApprovalMode };
  withDefaults(
    settings: DaemonSettings,
    patch: Partial<Omit<AgentDefaults, "$typeName">>,
  ): DaemonSettings;

  readonly askModeDesc: string;
  readonly planModeDesc: string;
  readonly permissionModeNotice: string | null;
  readonly showMaxEffort: boolean;
}

export function normalizePlanName(plan: string): string {
  const known: Record<string, string> = {
    prolite: "Pro Lite",
    claude_max: "Max",
    claude_pro: "Pro",
    claude_team: "Team",
    claude_enterprise: "Enterprise",
  };
  return (
    known[plan.toLowerCase()] ??
    plan
      .replace(/^claude_/i, "")
      .split(/[_\s]+/)
      .filter(Boolean)
      .map((w) => w[0].toUpperCase() + w.slice(1))
      .join(" ")
  );
}

function gptSlugLabel(id: string): string {
  return id
    .split("-")
    .map((w) => (/^gpt$/i.test(w) ? "GPT" : w.charAt(0).toUpperCase() + w.slice(1)))
    .join("-");
}

function fixedWindowLabel(name: string): QuotaWindowDescriptor {
  if (name === "primary" || name === "five_hour") return { label: l10n.t("quota.hr", { count: 5 }), group: null };
  if (name === "secondary" || name === "seven_day") return { label: l10n.t("quota.days", { count: 7 }), group: null };
  return { label: name, group: null };
}

const ANTIGRAVITY_QUOTA_WINDOWS: Record<string, () => QuotaWindowDescriptor> = {
  "gemini-5h": () => ({ label: l10n.t("quota.named_hours", { name: "Gemini", count: 5 }), group: "Gemini", pool: "Gemini" }),
  "gemini-weekly": () => ({ label: l10n.t("quota.named_days", { name: "Gemini", count: 7 }), group: "Gemini", pool: "Gemini" }),
  "gemini-argon-wtu-weekly": () => ({ label: l10n.t("quota.named_days", { name: "Gemini 4 Argon", count: 7 }), group: "Gemini", pool: "Gemini 4 Argon" }),
  "3p-5h": () => ({ label: l10n.t("quota.named_hours", { name: l10n.t("quota.third_party_models"), count: 5 }), group: l10n.t("quota.third_party_models_claude_gpt_oss"), pool: l10n.t("quota.third_party_models") }),
  "3p-weekly": () => ({ label: l10n.t("quota.named_days", { name: l10n.t("quota.third_party_models"), count: 7 }), group: l10n.t("quota.third_party_models_claude_gpt_oss"), pool: l10n.t("quota.third_party_models") }),
};

const CODEX_ENTRY: AgentEntry = {
  agent: Agent.CODEX,
  id: "codex",
  name: "Codex",
  quotaWindows: ["primary", "secondary"],
  quotaWindow(name: string): QuotaWindowDescriptor {
    return fixedWindowLabel(name);
  },
  planName(raw: string): string {
    return normalizePlanName(raw);
  },
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
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.defaultModel ?? "",
      effort: settings?.defaultEffort ?? "",
      mode: settings?.defaultMode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  withDefaults(settings: DaemonSettings, patch: Partial<Omit<AgentDefaults, "$typeName">>): DaemonSettings {
    return {
      ...settings,
      ...(patch.model !== undefined && { defaultModel: patch.model }),
      ...(patch.effort !== undefined && { defaultEffort: patch.effort }),
      ...(patch.mode !== undefined && { defaultMode: patch.mode }),
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
  quotaWindows: ["five_hour", "seven_day"],
  quotaWindow(name: string): QuotaWindowDescriptor {
    return fixedWindowLabel(name);
  },
  planName(raw: string): string {
    return normalizePlanName(raw);
  },
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
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.claude?.model ?? "",
      effort: settings?.claude?.effort ?? "",
      mode: settings?.claude?.mode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  withDefaults(settings: DaemonSettings, patch: Partial<Omit<AgentDefaults, "$typeName">>): DaemonSettings {
    const cur = this.defaults(settings);
    return {
      ...settings,
      claude: { $typeName: "remoteagent.v1.AgentDefaults", ...cur, ...patch },
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
  quotaWindows: [],
  quotaWindow(name: string): QuotaWindowDescriptor {
    return ANTIGRAVITY_QUOTA_WINDOWS[name]?.() ?? { label: name, group: name };
  },
  planName(raw: string): string {
    return raw;
  },
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
  defaults(settings?: DaemonSettings) {
    return {
      model: settings?.antigravity?.model ?? "",
      effort: settings?.antigravity?.effort ?? "",
      mode: settings?.antigravity?.mode ?? ApprovalMode.UNSPECIFIED,
    };
  },
  withDefaults(settings: DaemonSettings, patch: Partial<Omit<AgentDefaults, "$typeName">>): DaemonSettings {
    const cur = this.defaults(settings);
    return {
      ...settings,
      antigravity: { $typeName: "remoteagent.v1.AgentDefaults", ...cur, ...patch },
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
  name: l10n.t("models.unknown_cli"),
  quotaWindows: [],
  quotaWindow(name: string): QuotaWindowDescriptor {
    return { label: name, group: null };
  },
  planName(raw: string): string {
    return raw;
  },
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
  defaults() {
    return {
      model: "",
      effort: "",
      mode: ApprovalMode.UNSPECIFIED,
    };
  },
  withDefaults(settings: DaemonSettings): DaemonSettings {
    return settings;
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

export function withDefaults(
  settings: DaemonSettings,
  agent: Agent,
  patch: Partial<Omit<AgentDefaults, "$typeName">>,
): DaemonSettings {
  return agentInfo(agent).withDefaults(settings, patch);
}

export function machineVersions(meta?: MachineMeta): {
  agent: Agent;
  version: string;
  verified?: boolean;
}[] {
  const primary =
    meta?.agents.find((a) => a.agent === CODEX_ENTRY.agent) ??
    (meta?.codexVersion
      ? {
          agent: CODEX_ENTRY.agent,
          version: meta.codexVersion,
          verified: meta.codexVerified,
        }
      : undefined);
  return [
    primary,
    ...(meta?.agents.filter((a) => a.agent !== CODEX_ENTRY.agent) ?? []),
  ].filter((v): v is NonNullable<typeof v> => !!v?.version);
}
