export interface ScheduleTriggerJson {
  type: "calendar" | "interval" | "at" | "after_turn";
  time?: string;
  timezone?: string;
  weekdays?: number[];
  interval_seconds?: number;
  delay_seconds?: number;
  at?: string;
  until?: string;
  max_runs?: number;
}

export interface ScheduleTargetJson {
  type: "new_session" | "resume_session";
  cwd?: string;
  agent?: string;
  model?: string;
  effort?: string;
  mode?: string;
  session?: string;
}

export interface ScheduleSpecJson {
  title: string;
  message: string;
  notify?: string;
  target: ScheduleTargetJson;
  trigger: ScheduleTriggerJson;
}

export interface ScheduleRunJson {
  id: string;
  session_id?: string;
  scheduled_at?: string;
  started_at: string;
  ended_at?: string;
  status: string;
  reason?: string;
  manual?: boolean;
  start_seq?: number;
  after_seq?: number;
  notified: boolean;
  notification_seq?: number;
  notification_source?: string;
  notification_text?: string;
  final_summary?: string;
}

export interface ScheduleTaskJson {
  id: string;
  revision: number;
  status: string;
  reason?: string;
  source?: string;
  created_by_session?: string;
  takeover_session?: string;
  takeover_at?: string;
  takeover_by_device?: string;
  created_at: string;
  updated_at?: string;
  dispatches?: number;
  next_run_at?: string;
  last_run?: ScheduleRunJson;
  active_run?: ScheduleRunJson;
  runs?: ScheduleRunJson[];
  manual_request?: { id?: string; requested_at?: string };
  spec: ScheduleSpecJson;
}

export function task(patch: Partial<ScheduleTaskJson> = {}): ScheduleTaskJson {
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

export function run(id: string, patch: Partial<ScheduleRunJson> = {}): ScheduleRunJson {
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
