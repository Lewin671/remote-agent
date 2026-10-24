import RACore

/// Renaming never replans a task; only execution-definition edits wait for an active run.
struct ScheduleEditAccess {
    let rename: Bool
    let definition: Bool
    let run: Bool
    let showsReason: Bool
    init(task: ScheduleTask, online: Bool, saving: Bool, unknown: Bool) {
        showsReason = task.group == .attention || task.reason == "completed_while_paused"
        rename = online && !saving && !unknown
        definition = rename && task.active == nil
        run = definition && task.manualRequest == nil && task.group != .ended && task.group != .attention
    }
}
