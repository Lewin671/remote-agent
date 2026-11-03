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
    expect(AGENTS.map((a) => a.id)).toEqual(["codex", "claude"]);
    expect(AGENTS.map((a) => a.agent)).toEqual([Agent.CODEX, Agent.CLAUDE]);
    expect(DEFAULT_AGENT).toBe(Agent.CODEX);
  });

  it("looks up agent info by enum", () => {
    const codex = agentInfo(Agent.CODEX);
    expect(codex.id).toBe("codex");
    expect(codex.name).toBe("Codex");
    expect(codex.quotaWindows).toEqual(["primary", "secondary"]);
    expect(codex.planClearContext).toBe(true);
    expect(codex.planApprovalReturnsToAsk).toBe(false);
    expect(codex.standardContextOption).toBe(false);
    expect(codex.reconcilesUnclaimedInput).toBe(false);
    expect(codex.sessionNeedsModelList).toBe(false);
    expect(codex.modelLabel("gpt-6-sol")).toBe("GPT-6-Sol");
    expect(codex.planName("claude_max")).toBe("claude_max");

    const claude = agentInfo(Agent.CLAUDE);
    expect(claude.id).toBe("claude");
    expect(claude.name).toBe("Claude Code");
    expect(claude.quotaWindows).toEqual(["five_hour", "seven_day"]);
    expect(claude.planClearContext).toBe(false);
    expect(claude.planApprovalReturnsToAsk).toBe(true);
    expect(claude.standardContextOption).toBe(true);
    expect(claude.reconcilesUnclaimedInput).toBe(true);
    expect(claude.sessionNeedsModelList).toBe(true);
    expect(claude.modelLabel("claude-sonnet-5-5")).toBe("claude-sonnet-5-5");
    expect(claude.planName("claude_max")).toBe("Max");
    expect(claude.planName("prolite")).toBe("Pro Lite");
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
    expect(unknown.id).toBe("");
    expect(unknown.name).toBe("未知 CLI");
    expect(unknown.quotaWindows).toEqual([]);
    expect(unknown.planClearContext).toBe(false);
    expect(unknown.planApprovalReturnsToAsk).toBe(false);
    expect(unknown.standardContextOption).toBe(false);
    expect(unknown.autoMayBeUnavailable).toBe(false);
    expect(unknown.trustFolderHint).toBeNull();
    expect(unknown.usageHint).toBeNull();
    expect(unknown.reconcilesUnclaimedInput).toBe(false);
    expect(unknown.sessionNeedsModelList).toBe(false);
    expect(unknown.showMaxEffort).toBe(false);
    expect(unknown.permissionModeNotice).toBeNull();
    expect(unknown.modelLabel("any-slug")).toBe("any-slug");
    expect(unknown.planName("any-plan")).toBe("any-plan");
    expect(agentName(999 as Agent)).toBe("未知 CLI");
  });

  it("looks up agents by target id", () => {
    expect(agentById("codex").agent).toBe(Agent.CODEX);
    expect(agentById("claude").agent).toBe(Agent.CLAUDE);
    expect(agentById("").id).toBe("");
    expect(agentById("unknown").id).toBe("");
    expect(agentById(undefined).id).toBe("");
    expect(agentById(null).id).toBe("");
  });

  it("checks whether an agent enum is recognized", () => {
    expect(isKnownAgent(Agent.CODEX)).toBe(true);
    expect(isKnownAgent(Agent.CLAUDE)).toBe(true);
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
      codexVerified: "ok",
    });
    expect(machineVersions(legacy)).toEqual([
      { agent: Agent.CODEX, version: "1.2.3", verified: "ok" },
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
