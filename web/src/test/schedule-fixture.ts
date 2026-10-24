import type { ScheduleRun, ScheduleTask } from "../schedules";
export function task(patch: Partial<ScheduleTask> = {}): ScheduleTask {
  return {
    id: "task-1",
    revision: 1,
    status: "scheduled",
    source: "client",
    created_at: "2026-10-24T10:00:00Z",
    updated_at: "2026-10-24T10:00:00Z",
    dispatches: 0,
    next_run_at: "2026-10-24T15:00:00Z",
    spec: {
      title: "检查更新",
      message: "检查更新，有结果请通知我",
      target: {
        type: "new_session",
        cwd: "/example/project",
        agent: "claude",
        mode: "auto",
      },
      trigger: { type: "calendar", time: "08:00", timezone: "Asia/Shanghai" },
    },
    ...patch,
  };
}
export function run(id: string, patch: Partial<ScheduleRun> = {}): ScheduleRun {
  return {
    id,
    session_id: "session-" + id,
    scheduled_at: "2026-10-24T10:00:00Z",
    started_at: "2026-10-24T10:00:00Z",
    ended_at: "2026-10-24T10:01:00Z",
    status: "completed",
    start_seq: 2,
    after_seq: 1,
    notified: false,
    ...patch,
  };
}
