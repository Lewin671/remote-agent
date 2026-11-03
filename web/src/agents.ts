import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import type { AgentDefaults, DaemonSettings } from "./gen/remoteagent/v1/local_pb";
import type { MachineMeta } from "./gen/remoteagent/v1/remote_pb";

export interface AgentEntry {
  readonly agent: Agent;
  readonly id: string;
  readonly name: string;
  readonly quotaWindows: readonly string[];
  planName(raw: string): string;
  modelLabel(id: string): string;
  readonly toolLabelFromItemType: boolean;
  readonly planClearContext: boolean;
  readonly planApprovalReturnsToAsk: boolean;
  readonly standardContextOption: boolean;
  readonly autoMayBeUnavailable: boolean;
  readonly trustFolderHint: string | null;
  readonly usageHint: string | null;
  readonly reconcilesUnclaimedInput: boolean;
  readonly sessionNeedsModelList: boolean;
  defaults(settings?: DaemonSettings): { model: string; effort: string; mode: ApprovalMode };
  withDefaults(
    settings: DaemonSettings,
    patch: Partial<Omit<AgentDefaults, "$typeName">>,
  ): DaemonSettings;

  readonly askModeDesc: string;
  readonly planModeDesc: string;
  readonly permissionModeNotice: string | null;
  readonly showMaxEffort: boolean;
  sheetTitle(title: string): string;
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

const CODEX_ENTRY: AgentEntry = {
  agent: Agent.CODEX,
  id: "codex",
  name: "Codex",
  quotaWindows: ["primary", "secondary"],
  planName(raw: string): string {
    return normalizePlanName(raw);
  },
  modelLabel(id: string): string {
    return gptSlugLabel(id);
  },
  toolLabelFromItemType: false,
  planClearContext: true,
  planApprovalReturnsToAsk: false,
  standardContextOption: false,
  autoMayBeUnavailable: false,
  trustFolderHint: null,
  usageHint: null,
  reconcilesUnclaimedInput: false,
  sessionNeedsModelList: false,
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
  askModeDesc: "需要审批时由你确认，超时拒绝",
  planModeDesc: "只读和出计划，你批准后才开始改",
  permissionModeNotice: null,
  showMaxEffort: true,
  sheetTitle(title: string): string {
    return title;
  },
};

const CLAUDE_ENTRY: AgentEntry = {
  agent: Agent.CLAUDE,
  id: "claude",
  name: "Claude Code",
  quotaWindows: ["five_hour", "seven_day"],
  planName(raw: string): string {
    return normalizePlanName(raw);
  },
  modelLabel(id: string): string {
    return id;
  },
  toolLabelFromItemType: true,
  planClearContext: false,
  planApprovalReturnsToAsk: true,
  standardContextOption: true,
  autoMayBeUnavailable: true,
  trustFolderHint: "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎",
  usageHint: null,
  reconcilesUnclaimedInput: true,
  sessionNeedsModelList: true,
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
  askModeDesc: "需要权限时由你确认",
  planModeDesc: "先出方案，再批准执行",
  permissionModeNotice: "照实显示终端中的模式；不提供切到 accept edits 的按钮。",
  showMaxEffort: false,
  sheetTitle(title: string): string {
    return `Claude Code · ${title}`;
  },
};

const UNKNOWN_ENTRY: AgentEntry = {
  agent: Agent.UNSPECIFIED,
  id: "",
  name: "未知 CLI",
  quotaWindows: [],
  planName(raw: string): string {
    return raw;
  },
  modelLabel(id: string): string {
    return id;
  },
  toolLabelFromItemType: false,
  planClearContext: false,
  planApprovalReturnsToAsk: false,
  standardContextOption: false,
  autoMayBeUnavailable: false,
  trustFolderHint: null,
  usageHint: null,
  reconcilesUnclaimedInput: false,
  sessionNeedsModelList: false,
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
  askModeDesc: "需要审批时由你确认",
  planModeDesc: "先出方案，再批准执行",
  permissionModeNotice: null,
  showMaxEffort: false,
  sheetTitle(title: string): string {
    return title;
  },
};

export const AGENTS: readonly AgentEntry[] = [CODEX_ENTRY, CLAUDE_ENTRY];

export const DEFAULT_AGENT: Agent = CODEX_ENTRY.agent;

export function isKnownAgent(a: unknown): a is Agent {
  return AGENTS.some((e) => e.agent === a);
}

export function agentInfo(a?: Agent | number | null): AgentEntry {
  if (a === undefined || a === null || a === Agent.UNSPECIFIED) {
    return CODEX_ENTRY;
  }
  const found = AGENTS.find((e) => e.agent === a);
  return found ?? UNKNOWN_ENTRY;
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
  verified?: string;
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
