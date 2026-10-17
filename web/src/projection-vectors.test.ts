// One hand-reviewed oracle shared with Kotlin and Swift. No UI text is compared.
import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import corpus from "../../testdata/projection/vectors.json";
import { Agent, EventSchema, InputStage, type Event } from "./gen/remoteagent/v1/event_pb";
import { approvalsOnCalls, derive, type Derived } from "./model";

function normalize(events: Event[], d: Derived) {
  const seqs = (values: Iterable<bigint>) => [...values].sort((a, b) => a < b ? -1 : 1).map(String);
  const merged = approvalsOnCalls(events, d);
  const background = new Set(d.unsettled.filter(t => d.abortedTools.has(t.seq)).map(t => t.seq));
  for (const c of d.background) {
    for (const e of events) if (e.kind.case === "toolStarted" && e.kind.value.callId === c.callId && c.callId) background.add(e.seq);
  }
  return {
    running: d.running, approvals: [...d.approvals.keys()], resolved: Object.fromEntries([...d.resolved].map(([id, r]) => [id, r.decision])),
    questions: [...d.questions.keys()], answered: Object.fromEntries(d.answered),
    inputs: [...d.inputs].map(([id, status]) => ({ id, status, stage: d.stages.get(id) ?? InputStage.UNSPECIFIED, mode: d.accepted.get(id)?.mode ?? 0, paused: d.paused.has(id) })),
    accepted: Object.fromEntries([...d.accepted].map(([id, a]) => [id, { text: a.text, images: a.imageIds, origin: a.origin?.kind ?? 0 }])),
    unsent: [...d.unsent].sort(), local: seqs(d.typedLocally), runningTools: seqs(d.tools.map(t => t.seq)),
    unknownTools: seqs([...d.toolEnds].filter(([, end]) => end === "unsettled").map(([seq]) => seq)),
    interruptedTools: seqs(d.abortedTools), completed: Object.fromEntries([...d.toolEnds].filter(([, end]) => typeof end === "bigint").map(([seq, end]) => [String(seq), String(end)])),
    background: seqs(background), compactions: Object.fromEntries([...d.compactions].map(([seq, c]) => [String(seq), [c.before ?? null, c.after ?? null]])),
    terminalChanges: seqs(d.terminalChanges.keys()), merged: [...merged.approvals].sort(), callDecisions: seqs(merged.calls.keys()),
  };
}

for (const v of corpus.vectors) it(`shared projection: ${v.name}`, () => {
  expect(corpus.projectionVersion).toBe(1);
  const events = v.events.map(e => fromJson(EventSchema, e as JsonValue));
  for (const o of v.observations) {
    const prefix = events.slice(0, Number(o.through));
    expect(normalize(prefix, derive(prefix, v.agent as Agent)), `through ${o.through}`).toEqual(o.state);
    // Web's reducer is a full replay. Test every batch boundary using newly
    // decoded handles too; native runners exercise their incremental path.
    for (const cut of v.cuts.filter(c => c <= prefix.length)) {
      const batches = [...prefix.slice(0, cut), ...prefix.slice(cut)];
      expect(normalize(batches, derive(batches, v.agent as Agent)), `cut ${cut}`).toEqual(o.state);
    }
  }
});
