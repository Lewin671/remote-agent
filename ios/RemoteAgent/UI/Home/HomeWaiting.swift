// What waits for you on the home page (design-system §6 「等你的事」「终端卡」):
// the most urgent approval as the dark card, decided right there, and the
// rest one to a row of the grey list, which open the session where its dock
// handles them.
import RACore
import RAProto
import SwiftUI

/// One row of the grey list: a question, a plan, a popup, or an approval after the first.
struct HomePendingRow: View {
    let row: PBCoreAttentionRowView
    var first = true
    var last = true
    @Environment(Navigator.self) private var nav

    var body: some View {
        let title = row.title.rendered()
        let kind = row.kindText.rendered()
        // The summary repeats the kind for most rows; it is only the line when neither is given.
        let detail = row.detail.rendered()
        let summary = kind.isEmpty && detail.isEmpty ? row.summary.rendered() : detail
        let spoken = [title, kind, summary].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        Button {
            nav.open(.session(id: row.sessionID))
        } label: {
            PendingRowLayout(title: title, kind: kind, summary: summary, first: first, last: last)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityAddTraits(.isButton)
    }
}

/// The most urgent approval as the dark card.
struct HomeApprovalCard: View {
    let card: PBCoreAttentionCardView
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var full = HomeLaunch.value("-homeCard") == "full"
    @State private var denying = HomeLaunch.value("-homeCard") == "deny"
    @State private var more = 0
    @State private var total = 0
    @State private var chosen: Bool?

    /// Whole lines of the command before 看完整命令; two while a reason is written under it.
    static let lines = 5

    var body: some View {
        let dock = context.core.sessions[card.sessionID]?.dock
        let cmdState: PBSessionCommandState? = (dock?.hasItem_p == true && dock?.kind == .approval && dock?.approval.approvalID == card.approvalID) ? dock?.command : nil
        let target = ApprovalTarget(card: card, commandState: cmdState)
        let choice = context.sessionStore.approvalChoice(card.approvalID)
        Group {
            if target.isSubmitted(choice: choice) {
                ApprovalSubmitted(target: target, chosen: chosen, floating: false)
                    .padding(.horizontal, DS.Space.card)
            } else {
                DarkCard {
                    DarkCardHead(kind: card.kindText.rendered(), right: card.time.rendered(), hot: card.hot)
                    title
                    command
                    ApprovalDecision(
                        target: target,
                        cut: more > 0 && !full,
                        onFull: { withAnimation(Motion.enter()) { full = true } },
                        // Until the command is laid out it is not known to be whole: nothing is allowed before.
                        measured: total > 0,
                        denying: $denying,
                        chosen: $chosen,
                        signals: ("home.allow", "home.deny")
                    )
                    .padding(.top, 16)
                }
                .padding(.horizontal, DS.Space.card)
            }
        }
        .task(id: card.sessionID) {
            let sid = card.sessionID
            guard !sid.isEmpty else { return }
            let token = await context.coreHost.acquireSessionWatch(sid)
            defer { Task { await context.coreHost.releaseSessionWatch(token) } }
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3600))
            }
        }
    }

    private var title: some View {
        let titleText = card.title.rendered()
        let placeText = card.`where`.rendered()
        let spoken = [
            titleText,
            placeText.replacingOccurrences(of: " · ", with: L10n.commonIosSpokenSeparator()),
        ].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())
        return Button {
            nav.open(.session(id: card.sessionID))
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(titleText).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                    Image(systemName: "chevron.right").font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink3)
                }
                Text(placeText).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.top, 12)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityHint(L10n.homeIosOpenSession())
        .accessibilityAddTraits(.isButton)
    }

    @ViewBuilder private var command: some View {
        let cut = more > 0 && !full
        CommandBlock(card.body, lines: full ? nil : Self.lines, more: $more, total: $total)
            .padding(.top, 14)
            .accessibilityLabel(L10n.listStateIosCommandLabel(command: card.body))
        if cut {
            Text(L10n.markdownIosHiddenLines(count: more)).font(.footnote).foregroundStyle(DS.ink2).padding(.top, 8)
        } else if full && total > Self.lines {
            HStack(spacing: 4) {
                Text(L10n.markdownLineCount(count: total)).font(.footnote).foregroundStyle(DS.ink2)
                LinkButton(L10n.markdownCollapse()) { withAnimation(Motion.enter()) { full = false } }.font(.footnote)
            }
            .padding(.vertical, -8).padding(.top, 8)
        }
        if card.truncated {
            Text(L10n.approvalTruncated()).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
        }
    }
}

/// How a row answers a touch: its ground darkens under the finger, as a system list row's does.
struct RowHighlight: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .opacity(configuration.isPressed ? 0.6 : 1)
            .animation(Motion.enter(Motion.fast), value: configuration.isPressed)
    }
}
