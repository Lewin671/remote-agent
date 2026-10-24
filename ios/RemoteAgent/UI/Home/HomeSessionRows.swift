// A session in a list (design-system §6 会话行), on the home page and on a
// project's page: three lines at most while it goes on, one once it has
// ended. What the lines say is `sessionRowText` and `endedRowText`.
import RACore
import SwiftUI

/// Where a row's text starts: the page's margin, the status dot and the gap after it.
private struct RowInset: DynamicProperty {
    @ScaledMetric(relativeTo: .caption) private var dot: CGFloat = 8

    var wrappedValue: CGFloat { Theme.Space.page + dot + 11 }
}

/// One session that has not ended: a dot, the title and time, then the
/// status word in its colour · the preview, then only what differs between
/// the rows of this list (`meta`): 分叉 · project · CLI · machine, and no
/// third line when none does. An ended session is a `HomeEndedRow`. The
/// line under a row starts where its text does.
struct HomeSessionRow: View {
    let state: RemoteState
    let session: SessionView
    let now: Int64
    var meta = RowMeta()
    /// The last of its list draws no line under it.
    var last = false
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .caption) private var dot: CGFloat = 8

    var body: some View {
        if exited(session) {
            HomeEndedRow(session: session, now: now, project: meta.project)
        } else {
            let t = sessionRowText(state, session, meta: meta, now: now)
            let tone = StatusTone(t.status.tone)
            Button {
                nav.open(.session(id: session.id))
            } label: {
                SessionListRowContent(title: t.title, right: t.time, third: sessionMeta(t.meta), color: Theme.dot(tone), last: last) {
                    statusLine(t, tone)
                }
            }
            .buttonStyle(RowPressStyle())
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(t.spoken)
            .accessibilityAddTraits(.isButton)
        }
    }

    private func sessionMeta(_ base: String) -> String {
        let schedules = sessionSchedules(context.schedules.tasks, session: session)
        guard let first = schedules.first else { return base }
        let note = schedules.count == 1 ? "定时 " + scheduleFrequency(first.task.spec.trigger) : "定时 \(schedules.count) 个任务"
        return [base, note].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// 「运行中 · 正在执行 go test」: the word carries the state's colour, the rest is muted.
    private func statusLine(_ t: SessionRowText, _ tone: StatusTone) -> some View {
        (Text(t.status.text).fontWeight(.semibold).foregroundStyle(Theme.ink(tone))
            + Text(t.preview.isEmpty ? "" : " · \(t.preview)"))
            .type(T.sm, Theme.muted)
    }
}

/// An ended session takes one line: the title, and at the right when it
/// ended, after the project where the list spans projects. No dot: the
/// group's title above says these have ended. The time is the time of day
/// for today and yesterday, the weekday within a week, else the date.
struct HomeEndedRow: View {
    let session: SessionView
    let now: Int64
    var project = false
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize
    private let inset = RowInset()

    var body: some View {
        let t = endedRowText(session, project: project, now: now)
        Button {
            nav.open(.session(id: session.id))
        } label: {
            Group {
                if typeSize.isAccessibilitySize {
                    // The two do not fit side by side: the time goes under the title, at the right.
                    VStack(alignment: .leading, spacing: 0) {
                        Text(t.title).type(T.body, Theme.fg2).lineLimit(2)
                        Text(t.right).type(T.meta, Theme.muted).lineLimit(1).frame(maxWidth: .infinity, alignment: .trailing)
                    }
                    .padding(.vertical, 6)
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(t.title).type(T.body, Theme.fg2).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        HStack(alignment: .firstTextBaseline, spacing: 0) {
                            if !t.project.isEmpty {
                                // A long project name gives way, never the time after it.
                                Text(t.project + (t.time.isEmpty ? "" : " · ")).type(T.meta, Theme.muted).lineLimit(1)
                                    .frame(maxWidth: 120, alignment: .trailing).fixedSize(horizontal: true, vertical: false)
                            }
                            Text(t.time).type(T.meta, Theme.muted).lineLimit(1).fixedSize()
                        }
                    }
                }
            }
            .padding(.leading, inset.wrappedValue).padding(.trailing, Theme.Space.page)
            .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
        }
        .buttonStyle(RowPressStyle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(t.spoken)
        .accessibilityAddTraits(.isButton)
    }
}
