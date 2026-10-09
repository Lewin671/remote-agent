// A card of 需要处理 on the home page (Android's AttentionCard in
// ui/Home.kt): a session that waits for the user. An approval is decided
// right on the card; questions, plans and popups open the session.
//
// The buttons are the session's own (`ApprovalButtons`, as on Android), and
// what was decided is kept in one place for the account
// (`SessionStore.Resolve`): a decision made on a card is known on the
// session's page, and the other way round.
import Observation
import RACore
import RAProto
import SwiftUI

/// A waiting session's events reduced to what waits in it, kept up while its card is on screen.
@MainActor
@Observable
private final class CardEvents {
    private(set) var derived = Derived()
    @ObservationIgnored private var projection = EventProjection()

    func follow(_ remote: Remote, _ sid: String, agent: PBAgent) async {
        for await snapshot in remote.events(sid).updates() {
            derived = projection.update(snapshot.events, agent: agent)
            // The feed keeps the latest for a reader that is behind; a frame passes between two.
            if (try? await Task.sleep(nanoseconds: 16_000_000)) == nil { return }
        }
    }
}

/// A session that waits for the user, on a grey card. The cards are the whole account's, whatever the scope.
struct HomeAttentionCard: View {
    let state: RemoteState
    let session: SessionView
    let now: Int64
    let scope: HomeScope
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var events = CardEvents()

    var body: some View {
        let d = events.derived
        // What it waits for stays true while the server is out of reach.
        let status = statusOf(session, machine: state.machines[session.machineId])
        let approval = d.approvals.values.first
        let t = attentionCardText(
            state, session, scope: scope, approval: approval, question: d.questions.values.first?.questions.first?.text, now: now)
        VStack(alignment: .leading, spacing: 0) {
            Button {
                nav.open(.session(id: session.id))
            } label: {
                VStack(alignment: .leading, spacing: 0) {
                    head(t)
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(t.title).type(T.bodyB, Theme.fg).lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        if !typeSize.isAccessibilitySize { ArrowText(t.action, style: T.smB).fixedSize() }
                    }
                    .padding(.top, 4)
                    if !t.meta.isEmpty { Text(t.meta).type(T.meta, Theme.muted).lineLimit(2) }
                    if typeSize.isAccessibilitySize {
                        ArrowText(t.action, style: T.smB).frame(maxWidth: .infinity, alignment: .trailing).padding(.top, 2)
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(t.spoken)
            .accessibilityHint(t.action)
            .accessibilityAddTraits(.isButton)
            if status == .approval, let approval {
                if !approval.description_p.isEmpty {
                    Text(approvalDisplay(approval.description_p)).type(T.sm, Theme.fg2).padding(.top, 6)
                }
                if !approval.command.isEmpty {
                    Code(approvalDisplay(approval.command), maxLines: 3, background: Theme.card, border: true).padding(.vertical, 8)
                }
                ApprovalButtons(session: session, approval: approval, onCard: true)
                    .padding(.top, approval.command.isEmpty ? 8 : 0)
            }
        }
        .padding(.horizontal, 13).padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        // Notices on the card use the surface above it.
        .environment(\.onSoft, true)
        .padding(.horizontal, 12).padding(.bottom, 8)
        .task(id: session.id) { await events.follow(context.remote, session.id, agent: session.agent) }
    }

    /// 「● 请求执行」 at the left, the deadline or the time at the right; one under the other when the text is large.
    @ViewBuilder private func head(_ t: AttentionCardText) -> some View {
        let kind = HStack(spacing: 5) {
            Dot(Theme.orangeInk)
            Text(t.kind).type(T.metaB, Theme.orangeInk).lineLimit(1)
        }
        let right = Text(t.right).type(T.meta, Theme.muted)
        if typeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 0) {
                kind
                right
            }
        } else {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                kind
                Spacer(minLength: 0)
                right.lineLimit(1)
            }
        }
    }
}
