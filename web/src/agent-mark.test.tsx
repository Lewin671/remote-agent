// @vitest-environment happy-dom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Agent } from "./gen/remoteagent/v1/event_pb";
import { AgentMark } from "./agent-mark";

describe("AgentMark", () => {
  it("draws every CLI a session can run, in the text colour", () => {
    for (const a of [Agent.CODEX, Agent.CLAUDE, Agent.ANTIGRAVITY]) {
      const html = renderToStaticMarkup(<AgentMark agent={a} />);
      expect(html).toContain('fill="currentColor"');
      expect(html).toContain("<path");
    }
  });

  it("says the name alone, and is decoration beside the written name", () => {
    expect(renderToStaticMarkup(<AgentMark agent={Agent.CLAUDE} />)).toContain('aria-label="Claude Code"');
    const named = renderToStaticMarkup(<AgentMark agent={Agent.CLAUDE} named />);
    expect(named).toContain('aria-hidden="true"');
    expect(named).not.toContain("aria-label");
  });
});
