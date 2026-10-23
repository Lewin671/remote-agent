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
