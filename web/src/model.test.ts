import { sendModeFor } from "./agents";
import { t as message, localeStore } from "./i18n";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { Agent, ApprovalMode, SendMode, SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { ModelInfoSchema, SessionSchema, type Session } from "./gen/remoteagent/v1/local_pb";
import { AgentInfoSchema, MachineMetaSchema } from "./gen/remoteagent/v1/remote_pb";
import {
  agentName,
  agentOf,
  autoUnavailable,
  findModel,
  futureTime,
  machineAgents,
  modeLabel,
  modeOf,
  modelLabel,
  plain,
  projects,
  shortenPath,
  titleOf,
  type MachineLike,
  type SessionLike,
} from "./model";
import previewVectors from "../../testdata/preview-plain.json";

function sess(info: Partial<Omit<Session, "$typeName">> = {}, extra: Partial<SessionLike> = {}): SessionLike {
  return { id: "s1", machineId: "m1", updatedMs: 1000n, info: create(SessionSchema, { status: SessionStatus.IDLE, cwd: "/Users/me/Code/foo", ...info }), ...extra };
}

const online: MachineLike = { id: "m1", online: true, lastSeenMs: 0n };

describe("session titles", () => {
  it("uses the title, else the folder and start time", () => {
    expect(titleOf(sess({ title: "修复 `登录` 超时" }))).toBe("修复 登录 超时");
    const start = new Date(2026, 8, 29, 11, 25).getTime();
    expect(titleOf(sess({ cwd: "/Users/me/Code/billing", createdAt: timestampFromMs(start) }))).toBe("billing · 11:25");
  });
});

describe("agents", () => {
  it("treats sessions without an agent as Codex", () => {
    expect(agentOf(sess())).toBe(Agent.CODEX);
    expect(agentOf(sess({ agent: Agent.CLAUDE }))).toBe(Agent.CLAUDE);
    expect(agentName(agentOf(sess({ agent: Agent.UNSPECIFIED })))).toBe("Codex");
  });

  it("offers only the CLIs a machine has", () => {
    const meta = (...agents: Agent[]) => create(MachineMetaSchema, { agents: agents.map((agent) => create(AgentInfoSchema, { agent })) });
    expect(machineAgents({ ...online, meta: meta() })).toEqual([Agent.CODEX]);
    expect(machineAgents(online)).toEqual([Agent.CODEX]);
    expect(machineAgents({ ...online, meta: meta(Agent.CODEX, Agent.CLAUDE) })).toEqual([Agent.CODEX, Agent.CLAUDE]);
    expect(machineAgents({ ...online, meta: meta(Agent.CLAUDE) })).toEqual([Agent.CLAUDE]);
  });
});

describe("approval modes", () => {
  it("prefers Session.mode, for either CLI", () => {
    expect(modeOf(sess({ mode: ApprovalMode.AUTO, approvalsReviewer: "user", approvalPolicy: "on-request" }))?.text).toBe(message("modes.auto"));
    expect(modeOf(sess({ agent: Agent.CLAUDE, mode: ApprovalMode.FULL_ACCESS, permissionMode: "bypassPermissions" }))).toMatchObject({ text: message("modes.full_access"), mode: ApprovalMode.FULL_ACCESS });
  });

  it("shows a Claude Code mode outside the four by its own name", () => {
    expect(modeOf(sess({ agent: Agent.CLAUDE, permissionMode: "acceptEdits" }))).toEqual({ cls: "", text: "accept edits", mode: ApprovalMode.UNSPECIFIED });
  });

  it("knows which models come without auto", () => {
    const models = [create(ModelInfoSchema, { slug: "claude-haiku-4-5-20251001", autoUnavailable: true }), create(ModelInfoSchema, { slug: "claude-sonnet-5-5" })];
    expect(autoUnavailable(models, "claude-haiku-4-5-20251001")).toBe(true);
    expect(autoUnavailable(models, "claude-sonnet-5-5")).toBe(false);
    expect(autoUnavailable(models, "haiku")).toBe(false);
    expect(autoUnavailable(undefined, "claude-haiku-4-5-20251001")).toBe(false);
  });

  it("formats modeLabel", () => {
    const none = { known: false, mode: ApprovalMode.UNSPECIFIED, varies: false };
    expect(modeLabel(ApprovalMode.ASK, none, Agent.CODEX)).toBe(message("modes.ask"));
    expect(modeLabel(ApprovalMode.FULL_ACCESS, none, Agent.CODEX)).toBe(message("modes.full_access"));
  });
});

describe("model labels", () => {
  const list = [
    { slug: "gpt-6-sol", displayName: "GPT-6-Sol" },
    { slug: "sonnet", displayName: "Sonnet" },
  ];
  it("names a model by its display name, the list's or Codex's", () => {
    expect(modelLabel(list, "gpt-6-sol")).toBe("GPT-6-Sol");
    expect(modelLabel(list, "gpt-6.1-sol")).toBe("GPT-6.1-Sol");
    expect(modelLabel(list, "claude-sonnet-5-5", Agent.CLAUDE)).toBe("Sonnet");
    expect(modelLabel(list, "claude-opus-9", Agent.CLAUDE)).toBe("claude-opus-9");
    expect(modelLabel(undefined, "gpt-6.1-sol")).toBe("gpt-6.1-sol");
  });
});

describe("models", () => {
  const list = [{ slug: "opus" }, { slug: "sonnet" }, { slug: "haiku" }, { slug: "gpt-6-astra" }];
  it("finds Claude's alias for the full model ID a session reports", () => {
    expect(findModel(list, "claude-sonnet-5-5")?.slug).toBe("sonnet");
    expect(findModel(list, "claude-haiku-4-5-20251001")?.slug).toBe("haiku");
    expect(findModel(list, "sonnet")?.slug).toBe("sonnet");
  });
  it("matches Codex slugs exactly", () => {
    expect(findModel(list, "gpt-6-astra")?.slug).toBe("gpt-6-astra");
    expect(findModel(list, "gpt-6-sol")).toBeUndefined();
    expect(findModel(list, "")).toBeUndefined();
  });
});

describe("whole paths (file edit titles)", () => {
  const cases: [string, string, string, string][] = [
    ["/Users/me/Code/foo bar/a.go", "/Users/me/Code/foo", "/Users/me", "~/Code/foo bar/a.go"],
    ["/Users/me/Code/foo/sub dir/b.go", "/Users/me/Code/foo", "/Users/me", "sub dir/b.go"],
    ["/private/tmp/p/x y.txt", "/tmp/p", "/Users/me", "x y.txt"],
    ["/etc/x y", "/Users/me/Code/foo", "/Users/me", "/etc/x y"],
  ];
  it.each(cases)("shows %s in %s as expected", (path, cwd, home, shown) => {
    expect(shortenPath(path, cwd, home)).toBe(shown);
  });
});

describe("text", () => {
  it("makes one-line previews", () => {
    expect(plain("改了 `a.go`，见 [文档](x)")).toBe("改了 a.go，见 文档");
    expect(plain("## 结论\n- 第一点\n1. 第二点")).toBe("结论 第一点 第二点");
  });
});

it("finds the model of a session started under its 1M name", () => {
  const models = [{ slug: "opus" }, { slug: "haiku" }];
  expect(findModel(models, "opus[1m]")).toEqual({ slug: "opus" });
  expect(findModel(models, "claude-opus-5-5")).toEqual({ slug: "opus" });
});

describe("future times", () => {
  const now = new Date(2026, 9, 20, 23, 55).getTime();
  it.each([
    [new Date(2026, 9, 20, 3, 59, 1).getTime(), "今天 03:59"],
    [new Date(2026, 9, 21, 0, 2, 59).getTime(), "明天 00:02"],
    [new Date(2026, 9, 22, 3, 59, 1).getTime(), "10月22日 03:59"],
    [new Date(2026, 9, 22, 23, 59, 59).getTime(), "10月22日 23:59"],
  ])("writes %s as %s without locale-dependent seconds or AM/PM", (time, expected) => {
    expect(futureTime(time, now)).toBe(expected);
    expect(futureTime(BigInt(time), now)).toBe(expected);
  });
  it.each([
    [new Date(2026, 0, 31, 23, 59).getTime(), new Date(2026, 1, 1, 3, 59).getTime()],
    [new Date(2026, 11, 31, 23, 59).getTime(), new Date(2027, 0, 1, 3, 59).getTime()],
    [new Date(2026, 2, 7, 23, 59).getTime(), new Date(2026, 2, 8, 3, 59).getTime()],
  ])("recognizes tomorrow across calendar boundaries", (current, time) => {
    expect(futureTime(time, current)).toBe("明天 03:59");
  });
  it("formats pairing expiry as a 24-hour clock only, including after midnight", () => {
    expect(futureTime(new Date(2026, 9, 21, 5, 2, 59).getTime(), now, "time")).toBe("05:02");
    expect(futureTime(new Date(2026, 9, 21, 0, 0, 1).getTime(), now, "time")).toBe("00:00");
  });
});

it("keeps task-owned sessions out of projects while retaining by-ID session data", () => {
  const hidden = sess({ taskOwned: true, scheduledTaskId: "task", cwd: "/task" });
  const ordinary = sess({ cwd: "/ordinary" }, { id: "user" });
  expect(projects([hidden, ordinary], []).map((p) => p.key)).toEqual(["/ordinary"]);
});

it.each(previewVectors)("plain preview: $name", ({ input, expected }) => {
  expect(plain(input)).toBe(expected);
});

it("hides unconfirmed startup modes and preserves confirmed unknown modes", () => {
  for (const permissionMode of ["", "未知审批模式", "default"]) {
    expect(modeOf(sess({ agent: Agent.ANTIGRAVITY, status: SessionStatus.STARTING, permissionMode }))).toBeUndefined();
  }
  expect(modeOf(sess({ agent: Agent.ANTIGRAVITY, status: SessionStatus.IDLE, permissionMode: "未知审批模式" }))?.text).toBe("未知审批模式");
  expect(modeOf(sess({ agent: Agent.ANTIGRAVITY, status: SessionStatus.IDLE, mode: ApprovalMode.ASK }))?.mode).toBe(ApprovalMode.ASK);
  localeStore.overrideForTesting();
});

it("picks native send modes per agent", () => {
  for (const agent of [Agent.CODEX, Agent.CLAUDE]) {
    expect(sendModeFor(agent, SendMode.STEER, true)).toBe(SendMode.STEER);
    expect(sendModeFor(agent, SendMode.QUEUE, true)).toBe(SendMode.QUEUE);
  }
  for (const mode of [SendMode.UNSPECIFIED, SendMode.STEER, SendMode.QUEUE]) {
    expect(sendModeFor(Agent.ANTIGRAVITY, mode, true)).toBe(SendMode.QUEUE);
  }
  expect(sendModeFor(Agent.ANTIGRAVITY, SendMode.STEER, false)).toBe(SendMode.STEER);
});
