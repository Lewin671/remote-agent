import { create } from "@bufbuild/protobuf";
import { expect, it } from "vitest";
import { ScheduleSeen } from "./schedules";
import { derive, exitReason } from "./model";
import { EventSchema, SessionStatus } from "./gen/remoteagent/v1/event_pb";
import { task, run } from "./test/schedule-fixture";

function storage() {
  const data = new Map<string, string>();
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => { data.set(k, v); } };
}
it("counts unread runs, deduplicates active/history, and combines attention without double counting", () => {
  const seen = new ScheduleSeen(["relay", "client"], storage());
  const entry = (t = task(), machineId = "m") => ({ machineId, task: t });
  const old = run("old", { notified: true });
  seen.observe("m", [task({ runs: [old] })]);
  expect(seen.count([entry(task({ runs: [old] }))])).toBe(0);
  const current = task({ status: "failed", runs: [old, run("a", { notified: true, notification_count: 8 }), run("b", { notified: true })], active_run: run("b", { notified: true }) });
  seen.observe("m", [current]);
  expect(seen.unread("m", current)).toEqual(new Set(["a", "b"]));
  expect(seen.count([entry(current), entry(task({ id: "attention", status: "uncertain" }))])).toBe(3);
  expect(seen.count([entry({ ...current, status: "cancelled", reason: "user_cancelled" })])).toBe(0);
  expect(seen.count([])).toBe(0);
  seen.observe("other", []);
  seen.observe("other", [current]);
  expect(seen.count([entry(current), entry(current, "other")])).toBe(5); // 2 + 3
});
it("reads only the selected task, including notifications arriving during the visit", () => {
  const seen = new ScheduleSeen(["relay", "client"], storage());
  seen.observe("m", []);
  const a = task({ runs: [run("a", { notified: true })] });
  const b = task({ id: "b", runs: [run("b", { notified: true })] });
  seen.observe("m", [a, b]);
  expect(seen.setDetail("m", a.id)?.unread).toEqual(new Set(["a"]));
  expect(seen.unread("m", a).size).toBe(0);
  expect(seen.unread("m", b).size).toBe(1);
  a.runs!.push(run("during", { notified: true }));
  seen.observe("m", [a, b]);
  expect(seen.unread("m", a).size).toBe(0);
  seen.setDetail();
  a.runs!.push(run("after", { notified: true }));
  seen.observe("m", [a, b]);
  expect(seen.unread("m", a)).toEqual(new Set(["after"]));
});
it("keeps notifications unread while the page is hidden and dots them once it shows again", () => {
  const read: string[] = [];
  const seen = new ScheduleSeen(["relay", "client"], storage(), r => read.push(r.id));
  seen.observe("m", []);
  const a = task({ runs: [run("before", { notified: true, session_id: "s" })] });
  seen.observe("m", [a]);
  const visit = seen.setDetail("m", a.id)!;
  seen.setSession("s");
  expect(visit.unread).toEqual(new Set(["before"]));
  seen.setVisible(false);
  a.runs!.push(run("hidden", { notified: true, session_id: "s" }));
  seen.observe("m", [a]);
  expect(seen.unread("m", a)).toEqual(new Set(["hidden"]));
  expect(read).toEqual(["before"]);
  seen.setVisible(true);
  expect(seen.unread("m", a).size).toBe(0);
  expect(read).toEqual(["before", "hidden"]);
  expect(visit.unread).toEqual(new Set(["before", "hidden"]));
  a.runs!.push(run("shown", { notified: true }));
  seen.observe("m", [a]);
  expect(visit.unread).toEqual(new Set(["before", "hidden"]));
});
it("reads every notified run sharing the visible session, across tasks, without clearing other runs", () => {
  const seen = new ScheduleSeen(["relay", "client"], storage());
  seen.observe("m", []);
  const t = task({ runs: [run("1", { notified: true, session_id: "shared" }), run("2", { notified: true, session_id: "shared" }), run("3", { notified: true })] });
  const other = task({ id: "other", runs: [run("4", { notified: true, session_id: "shared" })] });
  seen.observe("m", [t, other]);
  seen.setSession("shared");
  expect(seen.unread("m", t)).toEqual(new Set(["3"]));
  expect(seen.unread("m", other).size).toBe(0);
  t.runs!.push(run("5", { notified: true, session_id: "shared" }));
  seen.observe("m", [t, other]);
  expect(seen.unread("m", t)).toEqual(new Set(["3"]));
  seen.setSession();
  t.runs!.push(run("6", { notified: true, session_id: "shared" }));
  seen.observe("m", [t]);
  expect(seen.unread("m", t)).toEqual(new Set(["3", "6"]));
});
it("persists seen state per relay, client identity and machine, including an initially empty list", () => {
  const store = storage();
  const t = task({ runs: [run("r", { notified: true })] });
  new ScheduleSeen(["relay-a", "account-a"], store).observe("m", []);
  const reload = new ScheduleSeen(["relay-a", "account-a"], store);
  reload.observe("m", [t]);
  expect(reload.count([{ machineId: "m", task: t }])).toBe(1);
  for (const scope of [["relay-b", "account-a"], ["relay-a", "account-b"]]) {
    const other = new ScheduleSeen(scope, store); other.observe("m", [t]);
    expect(other.count([{ machineId: "m", task: t }])).toBe(0);
  }
  reload.observe("other-machine", [t]);
  expect(reload.count([{ machineId: "other-machine", task: t }])).toBe(0);
  reload.setDetail("m", t.id);
  const reopened = new ScheduleSeen(["relay-a", "account-a"], store); reopened.observe("m", [t]);
  expect(reopened.count([{ machineId: "m", task: t }])).toBe(0);
});
it("uses the explicit latest exit event, retaining manual stop wording after a resume", () => {
  const state = (seq: bigint, status: SessionStatus, scheduledClosed: boolean) => create(EventSchema, {
    seq, kind: { case: "sessionState", value: { status, detail: "stopped", scheduledClosed } },
  });
  const automatic = state(1n, SessionStatus.EXITED, true);
  expect(exitReason("stopped", derive([automatic]).scheduledClosed)).toBe("定时任务运行已结束，会话自动关闭了。");
  expect(exitReason("stopped", derive([state(1n, SessionStatus.EXITED, false)]).scheduledClosed)).toBe("会话是被手动结束的。");
  expect(derive([automatic, state(2n, SessionStatus.STARTING, false)]).scheduledClosed).toBe(false);
  expect(exitReason("stopped", derive([automatic, state(2n, SessionStatus.EXITED, false)]).scheduledClosed)).toBe("会话是被手动结束的。");
  expect(exitReason("tmux session gone", true)).toBe("tmux 会话已经不在了。");
});

it("does not make a read run unread again when it notifies repeatedly, and isolates matching task IDs on different machines", () => {
  const seen = new ScheduleSeen(["relay", "client"], storage());
  const t = task({ runs: [run("same", { notified: true, notification_count: 1 })] });
  seen.observe("a", []); seen.observe("b", []);
  seen.observe("a", [t]); seen.observe("b", [t]);
  seen.setDetail("a", t.id);
  expect(seen.unread("a", t).size).toBe(0);
  expect(seen.unread("b", t).size).toBe(1);
  seen.setDetail();
  t.runs![0].notification_count = 3;
  seen.observe("a", [t]); seen.observe("b", [t]);
  expect(seen.unread("a", t).size).toBe(0);
  expect(seen.unread("b", t).size).toBe(1);
});

it("captures entry unread when the selected task first arrives, then keeps that visit snapshot", () => {
  const seen = new ScheduleSeen(["relay", "client"], storage());
  seen.observe("m", []);
  const visit = seen.setDetail("m", "task-1")!;
  expect(visit.unread).toBeUndefined();
  seen.observe("m", [task({ id: "other", runs: [run("other", { notified: true })] })]);
  expect(visit.unread).toBeUndefined();
  const t = task({ runs: [run("new", { notified: true })] });
  seen.observe("m", [t]);
  expect(visit.unread).toEqual(new Set(["new"]));
  expect(seen.unread("m", t).size).toBe(0);
  t.runs!.push(run("later", { notified: true }));
  seen.observe("m", [t]);
  expect(visit.unread).toEqual(new Set(["new"]));
  expect(seen.unread("m", t).size).toBe(0);
});

it("prunes read identities to retained notified history only on a complete machine snapshot", () => {
  const store = storage();
  const seen = new ScheduleSeen(["relay", "client"], store);
  const old = task({ runs: [run("retained", { notified: true }), run("evicted", { notified: true }), run("quiet", { notified: true })] });
  seen.observe("m", [old]); seen.observe("other", [old]);
  const key = 'ra-schedule-seen:["relay","client","m"]';
  const otherKey = 'ra-schedule-seen:["relay","client","other"]';
  const before = store.getItem(key);
  seen.observe("m", [], { complete: false }); // Loading/partial data cannot erase the baseline.
  expect(store.getItem(key)).toBe(before);
  const current = task({ runs: [old.runs![0], run("quiet"), run("unread", { notified: true })] });
  seen.observe("m", [current]);
  expect(JSON.parse(store.getItem(key)!)).toEqual([JSON.stringify([old.id, "retained"])]);
  expect(JSON.parse(store.getItem(otherKey)!)).toHaveLength(3);
  expect(seen.unread("m", current)).toEqual(new Set(["unread"]));
  const reload = new ScheduleSeen(["relay", "client"], store);
  reload.observe("m", [current]);
  expect(reload.unread("m", current)).toEqual(new Set(["unread"]));
  seen.observe("m", []); // A successful, genuinely empty list can prune everything.
  expect(JSON.parse(store.getItem(key)!)).toEqual([]);
});

it("requests cleanup for read runs, handles new notification seq on a visible run, and contains callback errors", () => {
  const closed: number[] = [];
  const seen = new ScheduleSeen(["relay", "client"], storage(), r => { closed.push(r.notification_seq!); throw new Error("cleanup failed"); });
  seen.observe("m", []);
  const t = task({ runs: [run("r", { notified: true, notification_seq: 7 })] });
  seen.observe("m", [t]);
  expect(closed).toEqual([]);
  seen.setDetail("m", t.id);
  expect(closed).toEqual([7]);
  expect(seen.unread("m", t).size).toBe(0);
  seen.observe("m", [t]);
  expect(closed).toEqual([7]);
  t.runs![0].notification_seq = 9;
  seen.observe("m", [t]);
  expect(closed).toEqual([7, 9]);
});

it("retains newer read identities when an older verified cache loads before the live list", () => {
  const store = storage();
  const a = run("a", { notified: true });
  const b = run("b", { notified: true });
  const current = task({ runs: [a, b] });
  new ScheduleSeen(["relay", "client"], store).observe("m", [current]);
  const reload = new ScheduleSeen(["relay", "client"], store);
  reload.observe("m", [task({ runs: [a] })], { prune: false });
  reload.observe("m", [current]);
  expect(reload.unread("m", current).size).toBe(0);
});
