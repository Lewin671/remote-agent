// The rows of design-system §6: a session that goes on (「会话行」), one
// that has ended, a row of the grey list of what waits, and the section
// titles between them. Layout only: what they say is RACore's
// (`sessionRowText`, `endedRowText`, `pendingRowText`), and what a tap does
// is the caller's. Each takes the whole width; the line under a row starts
// where its text does.
import RACore
import RAProto
import SwiftUI

/// The gutter of a row: the status graphic's column, so that titles line up with or without one.
private struct Gutter: DynamicProperty {
    @ScaledMetric(relativeTo: .body) var width: CGFloat = 26
}

/// A session that has not ended: the status graphic, the title (17 / 600)
/// with the time at the right, 「状态 · 预览」 (15, `ink2`, at most two
/// lines), and a third line (13) only when there is something to say: the
/// CLI's mark where the list uses more than one CLI, then words.
struct SessionRowLayout: View {
    let mark: StatusKind?
    let title: String
    let time: String
    let second: String
    var third = ""
    /// The CLI whose mark leads the third line; nil for none.
    var agent: PBAgent?
    /// The second line holds to one line, as a running command.
    var oneLine = false
    /// The last of its list draws no line under it.
    var last = false
    private let gutter = Gutter()
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            ZStack(alignment: .leading) {
                Text(" ").font(.headline).hidden()
                if let mark { StatusMark(mark).alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 } }
            }
            .frame(width: gutter.width, alignment: .leading)
            VStack(alignment: .leading, spacing: 3) {
                TitleAndTime(title: title, time: time, weight: .semibold)
                Text(second)
                    .font(.subheadline)
                    .foregroundStyle(DS.ink2)
                    .lineLimit(oneLine ? 1 : 2)
                if agent != nil || !third.isEmpty {
                    HStack(alignment: .firstTextBaseline, spacing: 0) {
                        if let agent { AgentMark(agent, style: .footnote, scale: 1.15) }
                        // A mark alone looks unfinished: with nothing else to say, the line names the CLI.
                        let words = third.isEmpty ? (agent.map { agentName($0) } ?? "") : third
                        if !words.isEmpty { Text(words).lineLimit(typeSize.isAccessibilitySize ? 2 : 1) }
                    }
                    .font(.footnote).foregroundStyle(DS.ink2).padding(.top, 1)
                }
            }
            .padding(.vertical, 13)
            .padding(.trailing, DS.Space.page)
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay(alignment: .bottom) { if !last { RowSeparator() } }
        }
        .padding(.leading, DS.Space.page)
        .contentShape(Rectangle())
    }
}

/// An ended session: one line, the title (17, 400) and at the right when it ended; no graphic, the date group says it ended.
struct EndedRowLayout: View {
    let title: String
    let right: String
    var last = false

    var body: some View {
        TitleAndTime(title: title, time: right, weight: .regular)
            .padding(.vertical, 12)
            .padding(.trailing, DS.Space.page)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
            .overlay(alignment: .bottom) { if !last { RowSeparator() } }
            .padding(.leading, DS.Space.page)
            .contentShape(Rectangle())
    }
}

/// The title with the time at its right; at the accessibility sizes the
/// time goes under it, at the right, rather than squeezing it.
private struct TitleAndTime: View {
    let title: String
    let time: String
    let weight: Font.Weight
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let name = Text(title).font(.body.weight(weight)).foregroundStyle(DS.ink)
        let when = Text(time).font(.footnote).foregroundStyle(DS.ink2)
        if typeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 2) {
                name.lineLimit(2)
                if !time.isEmpty { when.lineLimit(1).frame(maxWidth: .infinity, alignment: .trailing) }
            }
        } else {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                name.lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                when.lineLimit(1).fixedSize()
            }
        }
    }
}

/// A row's hairline.
struct RowSeparator: View {
    @Environment(\.displayScale) private var scale

    var body: some View {
        Rectangle().fill(DS.sep).frame(height: 1 / scale).accessibilityHidden(true)
    }
}

/// A section title: 进行中 (15 / 600, `ink`), or a date group (13, `ink2`, `dated`).
struct SectionTitle: View {
    let title: String
    var dated = false

    var body: some View {
        Text(title)
            .font(dated ? .footnote : .subheadline.weight(.semibold))
            .foregroundStyle(dated ? DS.ink2 : DS.ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, DS.Space.page)
            .padding(.top, dated ? 26 : 30).padding(.bottom, 4)
            .accessibilityAddTraits(.isHeader)
    }
}

/// One row of the grey list of what waits (design-system §6 「等你的事」):
/// the orange dot, the title (17 / 600), then the type word (`orangeInk`,
/// 600) · the summary (15, `ink2`), and › at the end. `first` and `last`
/// round the list's corners; rows between have a hairline from the text.
struct PendingRowLayout: View {
    let title: String
    let kind: String
    let summary: String
    var first = true
    var last = true

    var body: some View {
        HStack(spacing: 12) {
            StatusMark(.waiting)
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.headline).foregroundStyle(DS.ink).lineLimit(1)
                Text("\(Text(kind).fontWeight(.semibold).foregroundStyle(DS.orangeInk))\(summary.isEmpty ? "" : " · " + summary)")
                    .font(.subheadline).foregroundStyle(DS.ink2).lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3).accessibilityHidden(true)
        }
        .padding(.vertical, 12).padding(.leading, 18).padding(.trailing, 14)
        .frame(minHeight: DS.minHit)
        .contentShape(Rectangle())
        .pendingListSegment(first: first, last: last)
    }
}

/// The last row of the grey list: 「还有 N 件」 in blue.
struct PendingMoreRow: View {
    let text: String

    var body: some View {
        Text(text).font(.subheadline).foregroundStyle(DS.blue)
            .padding(.leading, 39).padding(.vertical, 10)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
            .contentShape(Rectangle())
            .pendingListSegment(first: false, last: true)
    }
}

extension View {
    /// One row's part of the grey list of what waits: its ground, the list's corners where it starts and ends, and the hairline above a row that follows another.
    func pendingListSegment(first: Bool, last: Bool) -> some View {
        let r = DS.Radius.pending
        return background(
            DS.gbg,
            in: UnevenRoundedRectangle(
                topLeadingRadius: first ? r : 0, bottomLeadingRadius: last ? r : 0, bottomTrailingRadius: last ? r : 0,
                topTrailingRadius: first ? r : 0, style: .continuous)
        )
        .overlay(alignment: .top) { if !first { RowSeparator().padding(.leading, 39) } }
        .padding(.horizontal, DS.Space.card)
    }
}
