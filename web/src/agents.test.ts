import { t as message } from "./i18n";
import { describe, expect, it } from "vitest";
import { Agent } from "./gen/remoteagent/v1/event_pb";
import { agentInfo, agentName } from "./agents";

describe("agents registry", () => {
  it("looks up agent info by enum", () => {
    const codex = agentInfo(Agent.CODEX);
    expect(codex.name).toBe("Codex");

    const claude = agentInfo(Agent.CLAUDE);
    expect(claude.name).toBe("Claude Code");

    const agy = agentInfo(Agent.ANTIGRAVITY);
    expect(agy.name).toBe("Antigravity");
  });

  it("maps UNSPECIFIED, null, and undefined to Codex", () => {
    expect(agentInfo(Agent.UNSPECIFIED).agent).toBe(Agent.CODEX);
    expect(agentInfo(undefined).agent).toBe(Agent.CODEX);
    expect(agentInfo(null).agent).toBe(Agent.CODEX);
    expect(agentName(Agent.UNSPECIFIED)).toBe("Codex");
    expect(agentName(undefined)).toBe("Codex");
  });

  it("maps unrecognized agent values to unknown entry and never Codex", () => {
    const unknown = agentInfo(999 as Agent);
    expect(unknown.agent).toBe(999);
    expect(unknown.name).toBe(message("models.unknown_cli"));
    expect(agentName(999 as Agent)).toBe(message("models.unknown_cli"));
    // Passing unknown.agent back into agentInfo must still be the unknown entry, never Codex.
    const roundtripped = agentInfo(unknown.agent);
    expect(roundtripped.name).toBe(message("models.unknown_cli"));
    expect(roundtripped.agent).toBe(999);
  });
});
