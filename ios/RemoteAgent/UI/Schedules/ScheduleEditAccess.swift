import RACore

/// Renaming never replans a task; only execution-definition edits wait for an active run.
struct ScheduleEditAccess {
    let rename: Bool
    let definition: Bool
    init(task: ScheduleTask, online: Bool, saving: Bool, unknown: Bool) {
        rename = online && !saving && !unknown
        definition = rename && task.active == nil && task.manualRequest == nil
    }
}
