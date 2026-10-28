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
it("baselines historical notifications, counts newly notified runs once per task, and clears on opening the list", () => {
  const store = storage(), seen = new ScheduleSeen(["https://relay.example.com", "client-a"], store);
  const entry = (t = task()) => [{ machineId: "m", task: t }];
  const old = run("old", { notified: true });
  seen.observe("m", [task({ runs: [old] })]);
  expect(seen.count(entry(task({ runs: [old] })))).toBe(0);
  const active = run("new", { status: "running" });
  seen.observe("m", [task({ runs: [old], active_run: active })]);
  active.notified = true;
  const current = task({ runs: [old], active_run: active });
  seen.observe("m", [current]);
  expect(seen.count(entry(current))).toBe(1);
  current.status = "failed";
  expect(seen.count([...entry(current), { machineId: "m", task: task({ id: "another", status: "uncertain" }) }])).toBe(2);
  seen.setOpen(true);
  current.status = "scheduled";
  expect(seen.count(entry(current))).toBe(0);
  current.runs = [...current.runs!, run("while-open", { notified: true })];
  seen.observe("m", [current]);
  expect(seen.count(entry(current))).toBe(0);
  seen.setOpen(false);
  current.runs.push(run("after-leaving", { notified: true }));
  seen.observe("m", [current]);
  expect(seen.count(entry(current))).toBe(1);
  expect(seen.count(entry({ ...current, status: "cancelled", reason: "user_cancelled" }))).toBe(0);
  expect(seen.count([])).toBe(0);
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
  reload.setOpen(true);
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
