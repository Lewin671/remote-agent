import { describe, expect, it } from "vitest";
import { applied, applyOp, commitOp, emptyPrefs, groupProjects, isHidden, isPinned, type PrefOp, type PrefsSnapshot, type PrefsStore } from "./prefs";
import { projects, stateOf, needsAttention, type SessionLike } from "./model";
import { create } from "@bufbuild/protobuf";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { Agent, ApprovalMode, SessionStatus } from "./gen/remoteagent/v1/event_pb";
import type { ProjectPrefs } from "./gen/remoteagent/v1/remote_pb";

let n = 0;
const op = (kind: PrefOp["kind"], path: string): PrefOp => ({ id: "op" + ++n, kind, path, at: 1000 + n });
const paths = (p: ProjectPrefs) => ({ pinned: p.pinned.map((x) => x.path), hidden: p.hidden.map((x) => x.path) });

/** A server holding the object, with a hook to let another client write
 * between this client's read and its write. */
class FakeStore implements PrefsStore {
  cur: PrefsSnapshot = { revision: 0n, prefs: emptyPrefs() };
  puts = 0;
  beforePut?: () => void;
  loseReply = 0;
  async get() {
    return this.cur;
  }
  async put(expected: bigint, next: ProjectPrefs) {
    this.puts++;
    this.beforePut?.();
    this.beforePut = undefined;
    if (expected !== this.cur.revision) return { applied: false, current: this.cur };
    this.cur = { revision: expected + 1n, prefs: next };
    if (this.loseReply > 0) {
      this.loseReply--;
      throw new Error("connection reset");
    }
    return { applied: true, current: this.cur };
  }
  other(o: PrefOp) {
    this.cur = { revision: this.cur.revision + 1n, prefs: applyOp(this.cur.prefs, o) };
  }
}

const set = (agent: Agent, model: string, effort: string, mode: ApprovalMode) => ({ agent, model, effort, mode, standardContext: false });

describe("project preferences", () => {
  it("pins in order, hides and restores without toggling", () => {
    let p = emptyPrefs();
    p = applyOp(p, op("pin", "/a"));
    p = applyOp(p, op("pin", "/b"));
    p = applyOp(p, op("pin", "/a")); // again: no change of place
    expect(paths(p)).toEqual({ pinned: ["/a", "/b"], hidden: [] });
    p = applyOp(p, op("hide", "/a"));
    expect(paths(p)).toEqual({ pinned: ["/b"], hidden: ["/a"] });
    expect(isHidden(p, "/a")).toBe(true);
    // Restoring brings it back among the others, not pinned.
    p = applyOp(p, op("unhide", "/a"));
    expect(paths(p)).toEqual({ pinned: ["/b"], hidden: [] });
    expect(isPinned(p, "/a")).toBe(false);
    // Unpinning a removed project keeps it removed.
    p = applyOp(applyOp(p, op("hide", "/c")), op("unpin", "/c"));
    expect(isHidden(p, "/c")).toBe(true);
  });

  it("keeps at most 64 recent operation IDs, newest last", () => {
    let p = emptyPrefs();
    for (let i = 0; i < 70; i++) p = applyOp(p, op("pin", "/x"));
    expect(p.recentOps.length).toBe(64);
    expect(p.recentOps.at(-1)).toBe("op" + n);
  });

  it("redoes the operation on another client's newer copy instead of overwriting it", async () => {
    const s = new FakeStore();
    s.other(op("pin", "/other"));
    const seen = await s.get();
    // Another client hides /x while this one pins /mine from what it saw.
    s.beforePut = () => s.other(op("hide", "/x"));
    const mine = op("pin", "/mine");
    const out = await commitOp(s, mine, seen);
    expect(s.puts).toBe(2);
    expect(paths(out.prefs)).toEqual({ pinned: ["/other", "/mine"], hidden: ["/x"] });
    expect(out.revision).toBe(3n);
    expect(applied(out.prefs, mine)).toBe(true);
  });

  it("redoes project settings on conflict and preserves every stored field", async () => {
    const s = new FakeStore();
    s.other(op("pin", "/keep"));
    s.other({ ...op("settings", "/other"), settings: { agent: Agent.CLAUDE, machineId: "m2", sets: [set(Agent.CLAUDE, "sonnet", "", ApprovalMode.ASK)] } });
    const seen = await s.get();
    s.beforePut = () => s.other(op("hide", "/hidden"));
    const mine = { ...op("settings", "/mine"), settings: { agent: Agent.CODEX, machineId: "m1", sets: [set(Agent.CODEX, "gpt-6-astra", "medium", ApprovalMode.FULL_ACCESS)] } };
    const out = await commitOp(s, mine, seen);
    expect(s.puts).toBe(2);
    expect(paths(out.prefs)).toEqual({ pinned: ["/keep"], hidden: ["/hidden"] });
    expect(out.prefs.settings).toMatchObject([
      { path: "/other", agent: Agent.CLAUDE, machineId: "m2", agents: [{ agent: Agent.CLAUDE, model: "sonnet" }] },
      { path: "/mine", agent: Agent.CODEX, machineId: "m1", agents: [{ agent: Agent.CODEX, model: "gpt-6-astra", effort: "medium", mode: ApprovalMode.FULL_ACCESS }] },
    ]);
  });

  it("changes one CLI's set and leaves the rest of a stored project's settings", () => {
    const before = applyOp(emptyPrefs(), { ...op("settings", "/p"), settings: { agent: Agent.CLAUDE, machineId: "m2", sets: [set(Agent.CLAUDE, "sonnet", "high", ApprovalMode.ASK), set(Agent.CODEX, "gpt-6-sol", "low", ApprovalMode.AUTO)] } });
    const after = applyOp(before, { ...op("settings", "/p"), settings: { sets: [set(Agent.CLAUDE, "opus", "high", ApprovalMode.ASK)] } });
    expect(after.settings).toHaveLength(1);
    expect(after.settings[0]).toMatchObject({ path: "/p", agent: Agent.CLAUDE, machineId: "m2" });
    expect(after.settings[0].agents).toHaveLength(2);
    expect(after.settings[0].agents.find((a) => a.agent === Agent.CLAUDE)).toMatchObject({ model: "opus", effort: "high", mode: ApprovalMode.ASK });
    expect(after.settings[0].agents.find((a) => a.agent === Agent.CODEX)).toMatchObject({ model: "gpt-6-sol", effort: "low", mode: ApprovalMode.AUTO });
  });

  it("moves the remembered CLI without touching any set", () => {
    const before = applyOp(emptyPrefs(), { ...op("settings", "/p"), settings: { agent: Agent.CODEX, machineId: "m1", sets: [set(Agent.CODEX, "gpt-6-sol", "low", ApprovalMode.AUTO)] } });
    const after = applyOp(before, { ...op("settings", "/p"), settings: { agent: Agent.CLAUDE } });
    expect(after.settings[0]).toMatchObject({ agent: Agent.CLAUDE, machineId: "m1", agents: [{ agent: Agent.CODEX, model: "gpt-6-sol" }] });
  });

  it("does not write twice when a reply was lost but the write went in", async () => {
    const s = new FakeStore();
    s.loseReply = 1;
    const o = op("hide", "/a");
    const out = await commitOp(s, o);
    expect(s.puts).toBe(1);
    expect(paths(out.prefs).hidden).toEqual(["/a"]);
  });

  it("groups pinned first in pin order, the rest by liveness then session creation, removed apart", () => {
    const p = applyOp(applyOp(applyOp(emptyPrefs(), op("pin", "/b")), op("pin", "/a")), op("hide", "/c"));
    const list = [
      { key: "/a", hasLive: false, latestCreatedMs: 1, updatedMs: 999 },
      { key: "/b", hasLive: true, latestCreatedMs: 2 },
      { key: "/c", hasLive: false, latestCreatedMs: 9 },
      { key: "/d", hasLive: true, latestCreatedMs: 3, updatedMs: 999 },
      { key: "/e", hasLive: false, latestCreatedMs: 5, updatedMs: 0 },
    ];
    const g = groupProjects(list, p);
    expect(g.pinned.map((x) => x.key)).toEqual(["/b", "/a"]);
    expect(g.others.map((x) => x.key)).toEqual(["/d", "/e"]);
    expect(g.hidden.map((x) => x.key)).toEqual(["/c"]);
  });

  it("leaves a removed project's waiting sessions where they were", () => {
    const s: SessionLike = { id: "s", machineId: "m", updatedMs: 1n, info: create(SessionSchema, { cwd: "/c", status: SessionStatus.RUNNING, pendingApprovals: 1 }) };
    const p = applyOp(emptyPrefs(), op("hide", "/c"));
    // Hiding touches the project list only; the session still needs you.
    expect(groupProjects(projects([s]), p).hidden.map((x) => x.key)).toEqual(["/c"]);
    expect(needsAttention(s)).toBe(true);
    expect(stateOf(s).kind).toBe("approval");
  });
});
