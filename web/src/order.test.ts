import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { expect, it } from "vitest";
import { SessionSchema } from "./gen/remoteagent/v1/local_pb";
import { SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { compareAttention, compareLive, compareEnded, type SessionLike } from "./model";

const session = (id: string, created: number, updated: number, extra = {}) => ({
  id, machineId: "m", updatedMs: BigInt(updated),
  info: create(SessionSchema, { createdAt: timestampFromMs(created), ...extra }),
});
const ids = (list: SessionLike[]) => list.map(s => s.id);
it("keeps live positions across output, offline and idle changes, then restores after approval", () => {
  const a = session("a", 1000, 9999), b = session("b", 2000, 3000);
  expect(ids([a, b].sort(compareLive))).toEqual(["b", "a"]);
  a.updatedMs = 999999n; a.info.status = SessionStatus.RUNNING;
  b.info.status = SessionStatus.IDLE;
  expect(ids([a, b].sort(compareLive))).toEqual(["b", "a"]);
  b.info.pendingApprovals = 1;
  b.info.pendingApprovals = 0;
  expect(ids([a, b].sort(compareLive))).toEqual(["b", "a"]);
});
it("orders attention by type and actual wait, independent of relay time", () => {
  const a = session("a", 100, 99999, { pendingApprovals: 1, attentionSince: timestampFromMs(2000) });
  const b = session("b", 200, 1, { pendingApprovals: 1, attentionSince: timestampFromMs(1000) });
  const q = session("q", 1, 999999, { pendingQuestions: 1, attentionSince: timestampFromMs(1) });
  expect(ids([q, a, b].sort((x, y) => compareAttention(x, y, new Map())))).toEqual(["b", "a", "q"]);
});
it("orders history by exit time and uses stable legacy fallbacks and ties", () => {
  const a = session("a", 100, 99999, { endedAt: timestampFromMs(1000) });
  const b = session("b", 200, 1, { endedAt: timestampFromMs(2000) });
  expect(ids([a, b].sort(compareEnded))).toEqual(["b", "a"]);
  const oldA = session("a", 100, 99999), oldB = session("b", 100, 1);
  expect(ids([oldB, oldA].sort(compareEnded))).toEqual(["a", "b"]);
  expect(ids([oldB, oldA].sort(compareLive))).toEqual(["a", "b"]);
});
