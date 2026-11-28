// A session row and an ended session row backed directly by core ViewModels.
import RACore
import RAProto
import SwiftUI

// A session moves between the attention, live and ended lists, and those lists
// sit side by side in one LazyVStack. Keying their ForEach by the bare session
// ID gives the same identity to rows in different lists, and the lazy stack
// then keeps drawing the row from the list the session just left (stale
// status, wrong row style). Each list keys its rows with its own prefix; the
// home screen's ended rows add their date group (`EndedEntry`).
extension PBCoreAttentionRowView { var listID: String { "att:\(sessionID)" } }
extension PBCoreSessionRowView { var listID: String { "run:\(sessionID)" } }
extension PBCoreEndedRowView { var listID: String { "end:\(sessionID)" } }

/// One session that has not ended: the status graphic, the title and time,
/// then 「状态 · 预览」 with the status word in `ink2` like the rest, then
/// metadata and any scheduled task note.
struct HomeSessionRow: View {
    let row: PBCoreSessionRowView
    /// The last of its list draws no line under it.
    var last = false
    @Environment(Navigator.self) private var nav

    var body: some View {
        let title = row.title.rendered()
        let time = row.time.rendered()
        let status = row.statusText.rendered()
        let preview = row.preview.rendered()
        let second = preview.isEmpty ? status : "\(status) · \(preview)"
        let third = row.metaLine.rendered()
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
