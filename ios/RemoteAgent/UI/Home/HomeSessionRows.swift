// A session in a list (design-system §6 会话行), on the home page and on a
// project's page: three lines at most while it goes on, one once it has
// ended. What the lines say is `sessionRowText` and `endedRowText`; how
// they are laid out is `SessionRowLayout` and `EndedRowLayout`.
import RACore
import SwiftUI

/// One session that has not ended: the status graphic, the title and time,
/// then 「状态 · 预览」 with the status word in `ink2` like the rest, then
/// only what differs between the rows of this list (`meta`; the CLI is its
/// mark at the head of the line), and the session's schedules. An ended session is a `HomeEndedRow`.
struct HomeSessionRow: View {
    let state: RemoteState
    let session: SessionView
    let now: Int64
    var meta = RowMeta()
    /// The last of its list draws no line under it.
    var last = false
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        if exited(session) {
            HomeEndedRow(session: session, now: now, project: meta.project)
        } else {
            let t = sessionRowText(state, session, meta: meta, now: now)
            Button {
                nav.open(.session(id: session.id))
            } label: {
                SessionRowLayout(
                    mark: StatusKind(t.status), title: t.title, time: t.time,
                    second: t.preview.isEmpty ? t.status.text : "\(t.status.text) · \(t.preview)",
                    third: sessionMeta(t.meta), agent: t.agent, oneLine: t.status.tone == .run, last: last)
            }
            .buttonStyle(RowHighlight())
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(t.spoken)
            .accessibilityAddTraits(.isButton)
        }
    }

    private func sessionMeta(_ base: String) -> String {
        let schedules = sessionSchedules(context.schedules.tasks, session: session)
        guard let first = schedules.first else { return base }
        let note = schedules.count == 1 ? L10n.homeScheduledFrequency(frequency: scheduleFrequency(first.task.spec.trigger)) : L10n.homeScheduledTasks(count: schedules.count)
        return [base, note].filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

/// An ended session takes one line: the title, and at the right when it
/// ended, after the project where the list spans projects. No graphic: the
/// group's title above says these have ended.
struct HomeEndedRow: View {
    let session: SessionView
    let now: Int64
    var project = false
    @Environment(Navigator.self) private var nav

    var body: some View {
        let t = endedRowText(session, project: project, now: now)
        Button {
            nav.open(.session(id: session.id))
        } label: {
            EndedRowLayout(title: t.title, right: t.right)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(t.spoken)
        .accessibilityAddTraits(.isButton)
    }
}
