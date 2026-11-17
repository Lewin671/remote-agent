// A session row and an ended session row backed directly by core ViewModels.
import RACore
import RAProto
import SwiftUI

/// One session that has not ended: the status graphic, the title and time,
/// then 「状态 · 预览」 with the status word in `ink2` like the rest, then
/// metadata and any scheduled task note.
struct HomeSessionRow: View {
    let row: PBCoreSessionRowView
    /// The last of its list draws no line under it.
    var last = false
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        let title = row.title.rendered()
        let time = row.time.rendered()
        let status = row.statusText.rendered()
        let preview = row.preview.rendered()
        let second = preview.isEmpty ? status : "\(status) · \(preview)"
        let third = sessionMeta(row.metaLine.rendered())
        let spoken = [title, status, preview, third, time].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        Button {
            nav.open(.session(id: row.sessionID))
        } label: {
            SessionRowLayout(
                mark: StatusKind(row),
                title: title,
                time: time,
                second: second,
                third: third,
                agent: row.multipleAgents ? row.agent : nil,
                oneLine: row.tone == .live,
                last: last
            )
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityAddTraits(.isButton)
    }

    private func sessionMeta(_ base: String) -> String {
        guard let session = model.state.sessions[row.sessionID] else { return base }
        let schedules = sessionSchedules(context.schedules.tasks, session: session)
        guard let first = schedules.first else { return base }
        let note = schedules.count == 1
            ? L10n.homeScheduledFrequency(frequency: scheduleFrequency(first.task.spec.trigger))
            : L10n.homeScheduledTasks(count: schedules.count)
        return [base, note].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

/// An ended session takes one line: the title, and at the right when it
/// ended (or project + time when spanning projects).
struct HomeEndedRow: View {
    let row: PBCoreEndedRowView
    @Environment(Navigator.self) private var nav

    var body: some View {
        let title = row.title.rendered()
        let right = row.right.rendered()
        let spoken = [title, right].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        Button {
            nav.open(.session(id: row.sessionID))
        } label: {
            EndedRowLayout(title: title, right: right)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityAddTraits(.isButton)
    }
}
