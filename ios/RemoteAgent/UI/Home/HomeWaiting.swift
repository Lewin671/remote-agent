// What waits for you on the home page (design-system §6 「等你的事」「终端卡」):
// the most urgent approval as the dark card, decided right there, and the
// rest one to a row of the grey list, which open the session where its dock
// handles them.
//
// A decision is kept in one place for the account (`SessionStore.Resolve`):
// one made on the card is known on the session's page, and the other way
// round.
import Observation
import RACore
import RAProto
import SwiftUI

/// A waiting session's events reduced to what waits in it, kept up while its card or row is on screen.
@MainActor
@Observable
final class WaitingEvents {
    private(set) var derived = Derived()
    @ObservationIgnored private var projection = EventProjection()

    func follow(_ remote: Remote, _ sid: String, agent: PBAgent) async {
        for await snapshot in remote.events(sid).updates() {
            let events = snapshot.stateEvents
            derived = currentActions(projection.update(events, agent: agent), live: snapshot.live, events: events)
            // The feed keeps the latest for a reader that is behind; a frame passes between two.
            if (try? await Task.sleep(nanoseconds: 16_000_000)) == nil { return }
        }
    }
}

/// One row of the grey list: a question, a plan, a popup, or an approval after the first.
struct HomePendingRow: View {
    let state: RemoteState
    let session: SessionView
    let scope: HomeScope
    var first = true
    var last = true
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var events = WaitingEvents()

    var body: some View {
        let d = events.derived
        let t = pendingRowText(
            state, session, scope: scope, approval: d.approvals.values.first,
            question: d.questions.values.first?.questions.first?.text)
        Button {
            nav.open(.session(id: session.id))
        } label: {
            PendingRowLayout(title: t.title, kind: t.kind, summary: t.summary, first: first, last: last)
        }
        .buttonStyle(RowHighlight())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(t.spoken)
        .accessibilityAddTraits(.isButton)
        .task(id: session.id) { await events.follow(context.remote, session.id, agent: session.agent) }
    }
}

/// The most urgent approval as the dark card. Until the approval itself is read it is a row like the rest.
struct HomeApprovalCard: View {
    let state: RemoteState
    let session: SessionView
    let scope: HomeScope
    @Environment(AccountContext.self) private var context
    @State private var events = WaitingEvents()

    var body: some View {
        Group {
            if let a = events.derived.approvals.values.first {
                // Keyed by approval: the next one does not inherit this one's opened command or reason.
                ApprovalCardBody(state: state, session: session, approval: a).id(a.approvalID)
                    .padding(.horizontal, DS.Space.card)
            } else {
                HomePendingRow(state: state, session: session, scope: scope)
            }
        }
        .task(id: session.id) { await events.follow(context.remote, session.id, agent: session.agent) }
    }
}

/// The card itself (design-system §6 「终端卡」): the head, the title that
/// opens the session, 「项目 · 机器」, the command held to five whole lines,
/// why, and the decision. A command not shown whole cannot be allowed:
/// until it is opened the card offers 不允许 and 看完整命令.
private struct ApprovalCardBody: View {
    let state: RemoteState
    let session: SessionView
    let approval: PBApprovalRequested
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
        Ticking(every: 15) { now in
            let t = approvalCardText(state, session, approval: approval, timeoutNote: true, now: now)
            if context.sessionStore.resolve(approval.approvalID).submitted {
                ApprovalSubmitted(session: session, approval: approval, chosen: chosen, floating: false)
            } else {
                DarkCard {
                    DarkCardHead(kind: t.kind, right: t.time, hot: t.hot)
                    title(t)
                    if isSkeleton(approval) {
                        Text(skeletonMessage).font(.subheadline).foregroundStyle(DS.ink2).padding(.top, 12)
                    } else {
                        command
                        ApprovalDecision(
                            session: session, approval: approval, now: now, cut: more > 0 && !full,
                            onFull: { withAnimation(Motion.enter()) { full = true } },
                            // Until the command is laid out it is not known to be whole: nothing is allowed before.
                            measured: total > 0, denying: $denying, chosen: $chosen, signals: ("home.allow", "home.deny"))
                            .padding(.top, 16)
                    }
                }
            }
        }
    }

    private func title(_ t: ApprovalCardText) -> some View {
        Button {
            nav.open(.session(id: session.id))
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(t.title).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                    Image(systemName: "chevron.right").font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink3)
                }
                Text(t.place).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.top, 12)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([t.title, t.place.replacingOccurrences(of: " · ", with: L10n.commonIosSpokenSeparator())].joined(separator: L10n.commonIosSpokenSeparator()))
        .accessibilityHint(L10n.homeIosOpenSession())
        .accessibilityAddTraits(.isButton)
    }

    @ViewBuilder private var command: some View {
        let cut = more > 0 && !full
        CommandBlock(approvalBody(approval), lines: denying ? 2 : full ? nil : Self.lines, more: $more, total: $total)
            .padding(.top, 14)
            .accessibilityLabel(L10n.listStateIosCommandLabel(command: approvalBody(approval)))
        if !denying {
            if cut {
                Text(L10n.markdownIosHiddenLines(count: more)).font(.footnote).foregroundStyle(DS.ink2).padding(.top, 8)
            } else if full && total > Self.lines {
                HStack(spacing: 4) {
                    Text(L10n.markdownLineCount(count: total)).font(.footnote).foregroundStyle(DS.ink2)
                    LinkButton(L10n.markdownCollapse()) { withAnimation(Motion.enter()) { full = false } }.font(.footnote)
                }
                .padding(.vertical, -8).padding(.top, 8)
            }
        }
        if approvalTruncated(approval) {
            Text(approvalTruncatedMessage).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
        }
        if !denying && !approval.description_p.isEmpty {
            Text(approvalDisplay(approval.description_p)).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(2).padding(.top, 12)
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
