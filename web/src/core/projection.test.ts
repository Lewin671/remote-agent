import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import {
  EventBatchSchema,
  ProjectionStateSchema,
} from "../gen/remoteagent/core/v1/projection_pb";
import {
  Agent,
  ApprovalDecision,
  EventSchema,
  InputStatus,
  ToolKind,
  TurnOutcome,
} from "../gen/remoteagent/v1/event_pb";
import { LiveSnapshotSchema } from "../gen/remoteagent/v1/remote_pb";
import { DemoRemote } from "../dev/demo";
import * as coreRuntimeModule from "./runtime";
import { getCore } from "./runtime";

describe("core WASM projection binding smoke", () => {
  it("reduces an EventBatch, appends incrementally, overlays live snapshot, and frees handle", () => {
    const core = getCore();
    const batch = create(EventBatchSchema, {
      events: [
        create(EventSchema, { seq: 1n, kind: { case: "turnStarted", value: {} } }),
        create(EventSchema, { seq: 2n, kind: { case: "toolStarted", value: { callId: "c1", title: "go test", kind: ToolKind.COMMAND } } }),
        create(EventSchema, { seq: 3n, kind: { case: "approvalRequested", value: { approvalId: "a1", command: "go test" } } }),
        create(EventSchema, { seq: 4n, kind: { case: "inputState", value: { requestId: "r1", status: InputStatus.QUEUED, text: "next" } } }),
      ],
    });

    const reduced = fromBinary(
      ProjectionStateSchema,
      core.reduce(Agent.CODEX, toBinary(EventBatchSchema, batch)),
    );
    expect(reduced.running).toBe(true);
    expect(reduced.approvals.map((a) => a.approvalId)).toEqual(["a1"]);
    expect(reduced.tools.map((t) => t.callId)).toEqual(["c1"]);
    expect(reduced.pendingInputs.map((p) => p.requestId)).toEqual(["r1"]);

    const proj = core.newProjection(Agent.CODEX);
    try {
      const s1 = fromBinary(ProjectionStateSchema, proj.append(toBinary(EventBatchSchema, batch)));
      expect(s1.running).toBe(true);

      const live = create(LiveSnapshotSchema, {
        session: { id: "s1", lastSeq: 4n },
        running: true,
        actions: [],
      });
      const actions = fromBinary(
        ProjectionStateSchema,
        proj.currentActions(toBinary(LiveSnapshotSchema, live)),
      );
      expect(actions.approvals).toHaveLength(0);
      expect(actions.inputs[0]?.status).toBe(InputStatus.UNCONFIRMED);

      const s2 = fromBinary(
        ProjectionStateSchema,
        proj.append(
          toBinary(
            EventBatchSchema,
            create(EventBatchSchema, {
              events: [
                create(EventSchema, { seq: 5n, kind: { case: "approvalResolved", value: { approvalId: "a1", decision: ApprovalDecision.ALLOW } } }),
                create(EventSchema, { seq: 6n, kind: { case: "turnEnded", value: { outcome: TurnOutcome.COMPLETED } } }),
              ],
            }),
          ),
        ),
      );
      expect(s2.running).toBe(false);
      expect(s2.resolved["a1"]?.decision).toBe(ApprovalDecision.ALLOW);

      proj.reset(Agent.CLAUDE);
      const resetState = fromBinary(ProjectionStateSchema, proj.snapshot());
      expect(resetState.running).toBe(false);
      expect(resetState.inputs).toHaveLength(0);
    } finally {
      proj.free();
    }
    expect(() => proj.snapshot()).toThrow(/already freed/);
  });

  it("frees session projection handle on retain release and remote stop", () => {
    const core = getCore();
    const proj = core.newProjection(Agent.CODEX);
    const freeSpy = vi.spyOn(proj, "free");
    const newProjSpy = vi.spyOn(core, "newProjection").mockReturnValue(proj);
    vi.spyOn(coreRuntimeModule, "getCore").mockReturnValue(core);
    const r = DemoRemote.create(new URLSearchParams());
    try {
      const release = r.retainHistory("s-run");
      r.historicalDerived("s-run");
      expect(newProjSpy).toHaveBeenCalledTimes(1);
      release();
      expect(freeSpy).toHaveBeenCalledTimes(1);
    } finally {
      r.stop();
      vi.restoreAllMocks();
    }
  });
});
