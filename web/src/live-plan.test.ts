import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { EventSchema, type Event } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { LiveSnapshotSchema } from "./gen/remoteagent/v1/remote_pb";
import { derive } from "./model";
import { livePlanText, Remote } from "./remote";
import { skeletonNote } from "./skeleton";

// What Claude Code's plan box shows in the terminal, and what it proposed.
const screen =
  "⎿ /plan to preview\n────────\nReady to code?\nHere is Claude's plan:\n────────\n计划：在 run-P 目录创建 5 个 beta 前缀文件\n";
const markdown = "# 计划：在 run-P 目录创建 5 个 beta 前缀文件\n\n## Context\n\n- 用户要求用前缀 beta";

const history = (text = markdown): Event[] => [
  create(EventSchema, { sessionId: "s", seq: 1n, kind: { case: "agentMessage", value: { text: "先问一个问题" } } }),
  create(EventSchema, { sessionId: "s", seq: 2n, kind: { case: "planProposed", value: { text } } }),
];
const live = (planEventSeq = 2n) =>
  create(LiveSnapshotSchema, {
    session: create(SessionSchema, { id: "s", planOpen: true }),
    planEventSeq,
    planScreenHash: new Uint8Array(32).fill(1),
    planScreen: screen,
  });

it("reads a pending plan as the event the live snapshot names, not the screen", () => {
  expect(livePlanText(live(), history(), () => true)).toBe(markdown);
});

it("falls back to the screen without that plan event readable here", () => {
  // Not loaded yet, another kind at that place, not verified, or too large to carry.
  expect(livePlanText(live(3n), history(), () => true)).toBe(screen);
  expect(livePlanText(live(1n), history(), () => true)).toBe(screen);
  expect(livePlanText(live(), history(), () => false)).toBe(screen);
  expect(livePlanText(live(), history("长计划" + skeletonNote), () => true)).toBe(screen);
  expect(livePlanText(live(), history("  "), () => true)).toBe(screen);
});

it("finds the plan event in a history that does not start at the first event", () => {
  expect(livePlanText(live(), history().slice(1), () => true)).toBe(markdown);
});

it("shows the dock the event's plan while the snapshot alone says it is pending", () => {
  const events = history();
  const fake = {
    buffers: new Map([["s", { events }]]),
    liveSnapshots: new Map([["s", live()]]),
    sessions: new Map(),
    unverifiedEvent: () => false,
  };
  const current = (l = live()) => {
    fake.liveSnapshots.set("s", l);
    return Remote.prototype.currentDerived.call(fake as unknown as Remote, "s", derive(events));
  };
  expect(current().plan?.text).toBe(markdown);
  // No plan pending on the computer: none shown, whatever history says.
  expect(current(create(LiveSnapshotSchema, { session: create(SessionSchema, { id: "s" }) })).plan).toBeUndefined();
});
