import RACore

/// Renaming never replans a task; only execution-definition edits wait for an active run.
struct ScheduleEditAccess {
    let rename: Bool
    let definition: Bool
    let run: Bool
    let showsReason: Bool
    let primaryRecovery: String?
    let menuToggle: String?
    init(task: ScheduleTask, online: Bool, saving: Bool, unknown: Bool) {
        showsReason = task.group == .attention
        let failedOnce = task.status == "failed" && task.oneShot
        primaryRecovery = task.group == .attention && !task.missingTarget ? (failedOnce ? "立即运行一次" : "恢复") : nil
        if task.group == .attention {
            menuToggle = task.missingTarget && !failedOnce ? "恢复" : nil
        } else {
            menuToggle = task.group == .ended ? nil : task.group == .paused ? "恢复" : "暂停"
        }
        rename = online && !saving && !unknown
        definition = rename && task.active == nil
        run = definition && task.manualRequest == nil && task.group != .ended && task.group != .attention
    }
}
