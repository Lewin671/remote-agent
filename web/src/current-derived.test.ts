import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { EventSchema, InputStatus } from "./gen/remoteagent/v1/event_pb";
import { LiveSnapshotSchema } from "./gen/remoteagent/v1/remote_pb";
import { DemoRemote } from "./dev/demo";
import { Remote } from "./remote";
import { derive } from "./model";

it("replaces historical pause membership for every input in the live observation", () => {
  const r = DemoRemote.create(new URLSearchParams());
  const input = (id: string, paused: boolean) => create(EventSchema, {
    kind: { case: "inputState", value: { requestId: id, paused, status: InputStatus.QUEUED } },
  });
  const historical = derive([input("resumed", true), input("paused", false), input("absent", true)]);
  (r as any).liveSnapshots.set("s-run", create(LiveSnapshotSchema, {
    actions: [input("resumed", false), input("paused", true)],
  }));
  const out = Remote.prototype.currentDerived.call(r, "s-run", historical);
  expect(out.paused.has("resumed")).toBe(false);
  expect(out.paused.has("paused")).toBe(true);
  expect(out.paused.has("absent")).toBe(false);
  expect(out.inputs.get("absent")).toBe(InputStatus.UNCONFIRMED);
  expect(historical.paused.has("resumed")).toBe(true);
});

it("reuses historical and live projections, invalidating only their actual inputs", () => {
  const r = DemoRemote.create(new URLSearchParams());
  const current = () => Remote.prototype.currentDerived.call(r, "s-run", r.historicalDerived("s-run"));
  const historical = r.historicalDerived("s-run");
  const first = current();
  r.conn = "offline"; r.touch();
  r.devices.clear(); r.touch();
  r.sessions.get("s-plan")!.updatedMs++; r.touch();
  expect(r.historicalDerived("s-run")).toBe(historical);
  expect(current()).toBe(first);
  const events = r.events("s-run");
  events.push(create(EventSchema, { kind: { case: "agentMessage", value: { text: "new" } } }));
  expect(r.historicalDerived("s-run")).not.toBe(historical);
  const appended = current();
  expect(appended).not.toBe(first);
  expect(current()).toBe(appended);
  const live = create(LiveSnapshotSchema, { running: true });
  (r as any).liveSnapshots.set("s-run", live);
  const observed = current();
  expect(observed.running).toBe(true);
  expect(current()).toBe(observed);
  r.sessions.get("s-run")!.checkpoint = create(LiveSnapshotSchema);
  expect(current()).not.toBe(observed);
});

// The same hand-reviewed history/live oracle is read by all three clients.
import liveInputs from "../../testdata/projection/live-inputs.json";
import { fromJson, type JsonValue } from "@bufbuild/protobuf";
for (const v of liveInputs.vectors) it(`shared live inputs: ${v.name}`, () => {
  const r = DemoRemote.create(new URLSearchParams());
  const history = v.history.map(e => fromJson(EventSchema, e as JsonValue));
  (r as any).liveSnapshots.set("s-run", fromJson(LiveSnapshotSchema, v.live as JsonValue));
  const d = derive(history);
  const out = Remote.prototype.currentDerived.call(r, "s-run", d);
  expect(out.plan).toBeUndefined();
  expect([...out.inputs].map(([id, status]) => {
    const a = out.accepted.get(id);
    return { id, status, stage: out.stages.get(id) ?? 0,
      mode: status === InputStatus.UNCONFIRMED ? 0 : a?.mode ?? 0, paused: out.paused.has(id),
      text: a?.text ?? "", images: a?.imageIds ?? [],
      files: (a?.files ?? []).map(f => ({ fileId: f.fileId, name: f.name, sizeBytes: String(f.sizeBytes) })),
      origin: { kind: a?.origin?.kind ?? 0, deviceId: a?.origin?.deviceId ?? "" } };
  })).toEqual(v.expected);
  expect(d.accepted.get("queued")?.text).toBe(history[0]?.kind.case === "inputState" ? history[0].kind.value.text : undefined);
});
