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
        primaryRecovery = task.group == .attention && !task.missingTarget ? (failedOnce ? L10n.scheduleRunOnceNow() : L10n.scheduleResume()) : nil
        if task.group == .attention {
            menuToggle = task.missingTarget && !failedOnce ? L10n.scheduleResume() : nil
        } else {
            menuToggle = task.group == .ended ? nil : task.group == .paused ? L10n.scheduleResume() : L10n.schedulePause()
        }
        rename = online && !saving && !unknown
        definition = rename && task.active == nil
        run = definition && task.manualRequest == nil && task.group != .ended && task.group != .attention
    }
}
