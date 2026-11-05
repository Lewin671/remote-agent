import { create, toBinary } from "@bufbuild/protobuf";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { DemoRemote } from "./dev/demo";
import { Remote } from "./remote";
import { derive, queuedIds } from "./model";
import { EventSchema, InputStage, InputStatus, SendMode } from "./gen/remoteagent/v1/event_pb";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { LiveSnapshotSchema } from "./gen/remoteagent/v1/remote_pb";
import { HeaderSchema } from "./gen/remoteagent/v1/envelope_pb";
import { PendingBubbles } from "./views/Queue";

const sid = "s-run", id = "request-example";
const input = (seq: bigint, status: InputStatus, stage = InputStage.WAITING) =>
  create(EventSchema, { seq, sessionId: sid, kind: { case: "inputState", value: {
    requestId: id, status, stage, text: "example prompt", mode: SendMode.STEER,
  } } });
const completed = () => [
  input(1n, InputStatus.QUEUED),
  input(2n, InputStatus.SUBMITTED),
  create(EventSchema, { seq: 3n, kind: { case: "userMessage", value: { requestId: id, text: "example prompt" } } }),
  create(EventSchema, { seq: 4n, kind: { case: "turnStarted", value: {} } }),
  create(EventSchema, { seq: 5n, kind: { case: "agentMessage", value: { text: "example answer" } } }),
  create(EventSchema, { seq: 6n, kind: { case: "turnEnded", value: {} } }),
];
function fixture(status?: InputStatus, stage = InputStage.WAITING) {
  const r = DemoRemote.create(new URLSearchParams());
  (r as any).outboxes.clear();
  const live = create(LiveSnapshotSchema, {
    session: { id: sid, lastSeq: 1n }, running: true,
    actions: status === undefined ? [] : [input(0n, status, stage)],
  });
  (r as any).liveSnapshots.set(sid, live);
  return { r, live, current: (events = completed()) => Remote.prototype.currentDerived.call(r, sid, derive(events)) };
}

it("history alone already knows submission and completion", () => {
  const d = derive(completed());
  expect(d.inputs.get(id)).toBe(InputStatus.SUBMITTED);
  expect(d.running).toBe(false);
  expect(queuedIds(d)).toEqual([]);
});

it.each([InputStatus.QUEUED, InputStatus.HELD_BY_CLI])("keeps submitted history newer than a queued/held snapshot: %s", status => {
  const { r, current } = fixture(status);
  expect(r.outbox(sid)).toEqual([]);
  const d = current();
  expect(d.inputs.get(id)).toBe(InputStatus.SUBMITTED);
  expect(queuedIds(d)).toEqual([]);
  expect(d.running).toBe(false);
  const html = renderToStaticMarkup(<PendingBubbles remote={r} session={r.sessions.get(sid)!} d={d} />);
  expect(html).not.toContain("example prompt");
  expect(html).not.toContain("插入这一轮");
  expect(html).not.toContain("取消");
});

it("fresh empty snapshot keeps a completed input submitted", () => {
  const { r, current } = fixture(InputStatus.QUEUED);
  expect(queuedIds(current())).toEqual([]);
  (r as any).liveSnapshots.set(sid, create(LiveSnapshotSchema, { session: { id: sid, lastSeq: 6n } }));
  const d = current();
  expect(d.inputs.get(id)).toBe(InputStatus.SUBMITTED);
  expect(queuedIds(d)).toEqual([]);
});

it("unrelated events and rederivation cannot resurrect the old queue", () => {
  const { current } = fixture(InputStatus.QUEUED);
  const more = [...completed(), create(EventSchema, { seq: 7n, kind: { case: "agentMessage", value: { text: "extra" } } })];
  expect(queuedIds(current(more))).toEqual([]);
  expect(queuedIds(current(more.map(e => ({ ...e }))))).toEqual([]);
});

it("stale SUBMITTING stage cannot resurrect a cancellable bubble", () => {
  const { current } = fixture(InputStatus.QUEUED, InputStage.SUBMITTING);
  expect(queuedIds(current())).toEqual([]);
});

it("keeps inputs accepted after an empty snapshot watermark", () => {
  const { current } = fixture();
  const d = current([input(2n, InputStatus.QUEUED)]);
  expect(d.inputs.get(id)).toBe(InputStatus.QUEUED);
  expect(queuedIds(d)).toEqual([id]);
});

it.each([InputStatus.CANCELED, InputStatus.FAILED, InputStatus.NEEDS_TERMINAL])(
  "keeps newer terminal input status %s and its details", status => {
    const { current } = fixture(InputStatus.HELD_BY_CLI);
    const d = current([input(1n, InputStatus.QUEUED), input(2n, status, InputStage.SUBMITTING)]);
    expect(d.inputs.get(id)).toBe(status);
    expect(d.stages.get(id)).toBe(InputStage.SUBMITTING);
    expect(queuedIds(d)).toEqual([]);
  },
);

it("keeps newer pause, stage and mode without suppressing other snapshot inputs", () => {
  const { r, live, current } = fixture(InputStatus.HELD_BY_CLI);
  const queued = input(2n, InputStatus.QUEUED, InputStage.SUBMITTING);
  if (queued.kind.case !== "inputState") throw new Error("expected input");
  queued.kind.value.paused = true;
  queued.kind.value.mode = SendMode.QUEUE;
  const other = input(0n, InputStatus.QUEUED);
  if (other.kind.case !== "inputState") throw new Error("expected input");
  other.kind.value.requestId = "other";
  live.actions.push(other);
  const history = derive([input(1n, InputStatus.QUEUED), queued]);
  const d = Remote.prototype.currentDerived.call(r, sid, history);
  expect(d.inputs.get(id)).toBe(InputStatus.QUEUED);
  expect(d.paused.has(id)).toBe(true);
  expect(d.stages.get(id)).toBe(InputStage.SUBMITTING);
  expect(d.accepted.get(id)?.mode).toBe(SendMode.QUEUE);
  expect(d.inputs.get("other")).toBe(InputStatus.QUEUED);
  expect(history.inputs.has("other")).toBe(false);
  expect(history.accepted.get(id)?.mode).toBe(SendMode.QUEUE);
  expect(current([queued]).paused.has(id)).toBe(true);
});

it("uses a refreshed snapshot at the event watermark to restore real pending state", () => {
  const { r, live } = fixture();
  const history = derive([input(1n, InputStatus.QUEUED)]);
  const current = () => Remote.prototype.currentDerived.call(r, sid, history);
  expect(current().inputs.get(id)).toBe(InputStatus.UNCONFIRMED);
  const held = input(0n, InputStatus.HELD_BY_CLI, InputStage.SUBMITTING);
  (r as any).liveSnapshots.set(sid, create(LiveSnapshotSchema, { ...live, actions: [held] }));
  expect(current().inputs.get(id)).toBe(InputStatus.HELD_BY_CLI);
  expect(current().stages.get(id)).toBe(InputStage.SUBMITTING);
  expect(queuedIds(current())).toEqual([id]);
});

it.each(["turnStarted", "turnEnded"] as const)("orders running by the %s event, not unrelated later events", kind => {
  const { r, live } = fixture();
  const history = derive([
    create(EventSchema, { seq: 2n, kind: { case: kind, value: {} } }),
    create(EventSchema, { seq: 4n, kind: { case: "agentMessage", value: { text: "later" } } }),
  ]);
  live.running = kind === "turnEnded";
  expect(Remote.prototype.currentDerived.call(r, sid, history).running).toBe(kind === "turnStarted");
  (r as any).liveSnapshots.set(sid, create(LiveSnapshotSchema, {
    ...live, session: create(SessionSchema, { id: sid, lastSeq: 2n }),
  }));
  expect(Remote.prototype.currentDerived.call(r, sid, history).running).toBe(live.running);
});

it.each(["turnStarted", "turnEnded"] as const)("does not let an older checkpoint overwrite %s without a live reply", kind => {
  const { r } = fixture();
  (r as any).liveSnapshots.delete(sid);
  const history = derive([create(EventSchema, { seq: 2n, kind: { case: kind, value: {} } })]);
  const checkpoint = create(LiveSnapshotSchema, {
    session: { id: sid, lastSeq: 1n }, running: kind === "turnEnded",
  });
  r.sessions.get(sid)!.checkpoint = checkpoint;
  expect(Remote.prototype.currentDerived.call(r, sid, history).running).toBe(kind === "turnStarted");
  r.sessions.get(sid)!.checkpoint = create(LiveSnapshotSchema, {
    ...checkpoint, session: create(SessionSchema, { id: sid, lastSeq: 2n }),
  });
  expect(Remote.prototype.currentDerived.call(r, sid, history).running).toBe(checkpoint.running);
});

it("delayed snapshot response cannot resurrect a submitted message", async () => {
  const { r, live, current } = fixture(InputStatus.QUEUED);
  (r as any).liveSnapshots.delete(sid);
  let finish!: (reply: unknown) => void;
  (r as any).forward = () => new Promise(resolve => { finish = resolve; });
  const job = Remote.prototype.refreshLiveSnapshot.call(r, sid);
  expect(queuedIds(current())).toEqual([]);
  finish({ kind: { case: "liveSnapshot", value: live } });
  await job;
  expect(queuedIds(current())).toEqual([]);
});

it("out-of-order ingest clears outbox and queue despite the old snapshot", async () => {
  const { r } = fixture(InputStatus.QUEUED);
  const b = { events: [], pending: new Map(), bytes: 0, loaded: true };
  (r as any).buffers.set(sid, b);
  (r as any).outboxes.set(sid, [{ sendId: id, text: "example prompt" }]);
  const all = completed().map(e => ({ ...e, sessionId: sid }));
  // Only transport authentication is stubbed; run the production ingest/reducer path.
  (r as any).openSecureMessage = async (_envelope: unknown, want: { seq: bigint }) => ({
    msg: all.find(e => e.seq === want.seq), size: 1,
  });
  const row = (seq: bigint) => ({ seq, envelope: {
    header: toBinary(HeaderSchema, create(HeaderSchema, { version: 3 })),
  } });
  await (r as any).ingest(r.sessions.get(sid), [row(2n)], false, true, false);
  expect(b.events).toHaveLength(0);
  await (r as any).ingest(r.sessions.get(sid), [row(1n), ...[3n, 4n, 5n, 6n].map(row)], false, true, false);
  expect((b as any).inputStatuses.get(id)).toBe(InputStatus.SUBMITTED);
  expect(r.outbox(sid)).toEqual([]);
  const historical = derive(b.events);
  expect(queuedIds(historical)).toEqual([]);
  const current = Remote.prototype.currentDerived.call(r, sid, historical);
  expect(queuedIds(current)).toEqual([]);
});
