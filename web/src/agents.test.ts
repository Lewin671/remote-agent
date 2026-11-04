import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Agent, ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import { DaemonSettingsSchema } from "./gen/remoteagent/v1/local_pb";
import { AgentInfoSchema, MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  AGENTS,
  DEFAULT_AGENT,
  agentById,
  agentInfo,
  agentName,
  defaultsFor,
  isKnownAgent,
  machineVersions,
  withDefaults,
} from "./agents";

describe("agents registry", () => {
  it("lists known CLIs in stable order", () => {
    expect(AGENTS.map((a) => a.id)).toEqual(["codex", "claude", "agy"]);
    expect(AGENTS.map((a) => a.agent)).toEqual([Agent.CODEX, Agent.CLAUDE, Agent.ANTIGRAVITY]);
    expect(DEFAULT_AGENT).toBe(Agent.CODEX);
  });

  it("looks up agent info by enum", () => {
    const codex = agentInfo(Agent.CODEX);
    expect(codex.id).toBe("codex");
    expect(codex.name).toBe("Codex");
    expect(codex.quotaWindows).toEqual(["primary", "secondary"]);
    expect(codex.quotaWindow("primary")).toEqual({ label: "5 小时", group: null });
    expect(codex.quotaWindow("secondary")).toEqual({ label: "7 天", group: null });
    expect(codex.planClearContext).toBe(true);
    expect(codex.planNote).toBeNull();
    expect(codex.standardContextOption).toBe(false);
    expect(codex.reconcilesUnclaimedInput).toBe(false);
    expect(codex.sessionNeedsModelList).toBe(false);
    expect(codex.switchesModelInSession).toBe(true);
    expect(codex.forks).toBe(true);
    expect(codex.modelLabel("gpt-6-sol")).toBe("GPT-6-Sol");
    expect(codex.planName("claude_max")).toBe("Max");
    expect(codex.planName("prolite")).toBe("Pro Lite");

    const claude = agentInfo(Agent.CLAUDE);
    expect(claude.id).toBe("claude");
    expect(claude.name).toBe("Claude Code");
    expect(claude.quotaWindows).toEqual(["five_hour", "seven_day"]);
    expect(claude.quotaWindow("five_hour")).toEqual({ label: "5 小时", group: null });
    expect(claude.quotaWindow("seven_day")).toEqual({ label: "7 天", group: null });
    expect(claude.planClearContext).toBe(false);
    expect(claude.planNote).toBe("批准后使用询问模式");
    expect(claude.standardContextOption).toBe(true);
    expect(claude.reconcilesUnclaimedInput).toBe(true);
    expect(claude.sessionNeedsModelList).toBe(true);
    expect(claude.switchesModelInSession).toBe(true);
    expect(claude.forks).toBe(true);
    expect(claude.modelLabel("claude-sonnet-5-5")).toBe("claude-sonnet-5-5");
    expect(claude.planName("claude_max")).toBe("Max");
    expect(claude.planName("prolite")).toBe("Pro Lite");

    const agy = agentInfo(Agent.ANTIGRAVITY);
    expect(agy.id).toBe("agy");
    expect(agy.name).toBe("Antigravity");
    expect(agy.quotaWindows).toEqual([]);
    expect(agy.quotaWindow("gemini-5h")).toEqual({ label: "Gemini · 5 小时", group: "Gemini" });
    expect(agy.quotaWindow("gemini-weekly")).toEqual({ label: "Gemini · 7 天", group: "Gemini" });
    expect(agy.quotaWindow("gemini-argon-wtu-weekly")).toEqual({ label: "Gemini 4 Argon · 7 天", group: "Gemini" });
    expect(agy.quotaWindow("3p-5h")).toEqual({ label: "第三方模型 · 5 小时", group: "第三方模型（Claude、GPT-OSS）" });
    expect(agy.quotaWindow("3p-weekly")).toEqual({ label: "第三方模型 · 7 天", group: "第三方模型（Claude、GPT-OSS）" });
    expect(agy.quotaWindow("custom-window")).toEqual({ label: "custom-window", group: "custom-window" });
    expect(agy.planClearContext).toBe(false);
    expect(agy.planNote).toBe("批准后回到默认模式（询问）");
    expect(agy.standardContextOption).toBe(false);
    expect(agy.autoMayBeUnavailable).toBe(true);
    expect(agy.trustFolderHint).toBeNull();
    expect(agy.usageHint).toBeNull();
    expect(agy.reconcilesUnclaimedInput).toBe(false);
    expect(agy.sessionNeedsModelList).toBe(true);
    expect(agy.switchesModelInSession).toBe(false);
    expect(agy.forks).toBe(false);
    expect(agy.showMaxEffort).toBe(false);
    expect(agy.modelLabel("gemini-3.1-pro-high")).toBe("gemini-3.1-pro-high");
    expect(agy.planName("Google AI Pro")).toBe("Google AI Pro");
  });

  it("maps UNSPECIFIED, null, and undefined to Codex", () => {
    expect(agentInfo(Agent.UNSPECIFIED).id).toBe("codex");
    expect(agentInfo(undefined).id).toBe("codex");
    expect(agentInfo(null).id).toBe("codex");
    expect(agentName(Agent.UNSPECIFIED)).toBe("Codex");
    expect(agentName(undefined)).toBe("Codex");
  });

  it("maps unrecognized agent values to unknown entry and never Codex", () => {
    const unknown = agentInfo(999 as Agent);
    expect(unknown.agent).toBe(999);
    expect(unknown.id).toBe("");
    expect(unknown.name).toBe("未知 CLI");
    expect(unknown.quotaWindows).toEqual([]);
    expect(unknown.planClearContext).toBe(false);
    expect(unknown.planNote).toBeNull();
    expect(unknown.standardContextOption).toBe(false);
    expect(unknown.autoMayBeUnavailable).toBe(false);
    expect(unknown.trustFolderHint).toBeNull();
    expect(unknown.usageHint).toBeNull();
    expect(unknown.reconcilesUnclaimedInput).toBe(false);
    expect(unknown.sessionNeedsModelList).toBe(false);
    expect(unknown.switchesModelInSession).toBe(false);
    expect(unknown.forks).toBe(false);
    expect(unknown.showMaxEffort).toBe(false);
    expect(unknown.permissionModeNotice).toBeNull();
    expect(unknown.modelLabel("any-slug")).toBe("any-slug");
    expect(unknown.planName("any-plan")).toBe("any-plan");
    expect(agentName(999 as Agent)).toBe("未知 CLI");
    // Passing unknown.agent back into agentInfo must still be the unknown entry, never Codex.
    const roundtripped = agentInfo(unknown.agent);
    expect(roundtripped.id).toBe("");
    expect(roundtripped.name).toBe("未知 CLI");
    expect(roundtripped.agent).toBe(999);
    expect(agentInfo(agentById("unknown").agent).id).toBe("");
  });

  it("looks up agents by target id", () => {
    expect(agentById("codex").agent).toBe(Agent.CODEX);
    expect(agentById("claude").agent).toBe(Agent.CLAUDE);
    expect(agentById("agy").agent).toBe(Agent.ANTIGRAVITY);
    expect(agentById("").id).toBe("");
    expect(agentById("unknown").id).toBe("");
    expect(agentById(undefined).id).toBe("");
    expect(agentById(null).id).toBe("");
  });

  it("checks whether an agent enum is recognized", () => {
    expect(isKnownAgent(Agent.CODEX)).toBe(true);
    expect(isKnownAgent(Agent.CLAUDE)).toBe(true);
    expect(isKnownAgent(Agent.ANTIGRAVITY)).toBe(true);
    expect(isKnownAgent(Agent.UNSPECIFIED)).toBe(false);
    expect(isKnownAgent(999)).toBe(false);
    expect(isKnownAgent("codex")).toBe(false);
  });

  it("round-trips defaults and withDefaults for Codex", () => {
    const base = create(DaemonSettingsSchema, {
      defaultModel: "gpt-6-sol",
      defaultEffort: "medium",
      defaultMode: ApprovalMode.AUTO,
    });
    expect(defaultsFor(base, Agent.CODEX)).toEqual({
      model: "gpt-6-sol",
      effort: "medium",
      mode: ApprovalMode.AUTO,
    });

    const updated = withDefaults(base, Agent.CODEX, {
      model: "gpt-6.1",
      effort: "high",
    });
    expect(updated.defaultModel).toBe("gpt-6.1");
    expect(updated.defaultEffort).toBe("high");
    expect(updated.defaultMode).toBe(ApprovalMode.AUTO);

    expect(defaultsFor(updated, Agent.CODEX)).toEqual({
      model: "gpt-6.1",
      effort: "high",
      mode: ApprovalMode.AUTO,
    });
  });

  it("round-trips defaults and withDefaults for Claude", () => {
    const base = create(DaemonSettingsSchema, {
      claude: {
        model: "claude-sonnet-5-5",
        effort: "low",
        mode: ApprovalMode.ASK,
      },
    });
    expect(defaultsFor(base, Agent.CLAUDE)).toEqual({
      model: "claude-sonnet-5-5",
      effort: "low",
      mode: ApprovalMode.ASK,
    });

    const updated = withDefaults(base, Agent.CLAUDE, {
      model: "claude-opus-9",
    });
    expect(updated.claude?.model).toBe("claude-opus-9");
    expect(updated.claude?.effort).toBe("low");
    expect(updated.claude?.mode).toBe(ApprovalMode.ASK);

    expect(defaultsFor(updated, Agent.CLAUDE)).toEqual({
      model: "claude-opus-9",
      effort: "low",
      mode: ApprovalMode.ASK,
    });
  });

  it("round-trips defaults and withDefaults for Antigravity", () => {
    const base = create(DaemonSettingsSchema, {
      antigravity: {
        model: "gemini-3.1-pro-high",
        effort: "high",
        mode: ApprovalMode.ASK,
      },
    });
    expect(defaultsFor(base, Agent.ANTIGRAVITY)).toEqual({
      model: "gemini-3.1-pro-high",
      effort: "high",
      mode: ApprovalMode.ASK,
    });

    const updated = withDefaults(base, Agent.ANTIGRAVITY, {
      model: "gemini-3-flash",
      mode: ApprovalMode.PLAN,
    });
    expect(updated.antigravity?.model).toBe("gemini-3-flash");
    expect(updated.antigravity?.effort).toBe("high");
    expect(updated.antigravity?.mode).toBe(ApprovalMode.PLAN);

    expect(defaultsFor(updated, Agent.ANTIGRAVITY)).toEqual({
      model: "gemini-3-flash",
      effort: "high",
      mode: ApprovalMode.PLAN,
    });
  });

  it("preserves settings when updating unknown agent defaults", () => {
    const base = create(DaemonSettingsSchema, { defaultModel: "m" });
    expect(withDefaults(base, 999 as Agent, { model: "x" })).toBe(base);
    expect(defaultsFor(base, 999 as Agent)).toEqual({
      model: "",
      effort: "",
      mode: ApprovalMode.UNSPECIFIED,
    });
  });

  it("synthesizes machine versions from legacy codexVersion and modern agents list", () => {
    const legacy = create(MachineMetaSchema, {
      codexVersion: "1.2.3",
      codexVerified: true,
    });
    expect(machineVersions(legacy)).toEqual([
      { agent: Agent.CODEX, version: "1.2.3", verified: true },
    ]);

    const modern = create(MachineMetaSchema, {
      agents: [
        create(AgentInfoSchema, { agent: Agent.CODEX, version: "2.0.0" }),
        create(AgentInfoSchema, { agent: Agent.CLAUDE, version: "3.0.0" }),
      ],
    });
    expect(machineVersions(modern)).toMatchObject([
      { agent: Agent.CODEX, version: "2.0.0" },
      { agent: Agent.CLAUDE, version: "3.0.0" },
    ]);
  });
});
