import RACore
import RAProto
import SwiftUI

/// 审批详情: an approval on a page of its own, as opened from a link, with
/// the whole of what it asks for, before what it is for, and a
/// reason box that stays above the keyboard (Android's ApprovalPage).
///
/// The page shows what the daemon's events say: once anyone has answered
/// the approval, from whichever client or in the terminal, the request is
/// gone from here and the page says so, with how it was decided when the
/// events tell. A reason typed here and not sent stays, to be copied; it is
/// never carried over to another approval (architecture §9).
struct ApprovalScreen: View {
    let session: String
    let approval: String

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var chat = SessionModel()
    @State private var finder = SessionFinder()
    @State private var denying = false
    @State private var chosen: Bool?

    var body: some View {
        let state = model.state
        let s = state.sessions[session]
        let d = chat.derived
        let a = d.approvals[approval]
        Ticking { now in
            Page {
                VStack(spacing: 0) {
                    ConnectionBar()
                    if let a, isSkeleton(a) {
                        PageNote(skeletonMessage)
                    } else if let s, let a {
                        // The same dark card as the dock's: what it would run, whole, then the decision.
                        ScrollView {
                            Group {
                                if context.sessionStore.resolve(a.approvalID).submitted {
                                    ApprovalSubmitted(session: s, approval: a, chosen: chosen, floating: false)
                                } else {
                                    DarkCard {
                                        DarkCardHead(kind: approvalHead(a))
                                        ApprovalContent(approval: a, denying: denying).textSelection(.enabled).padding(.top, 14)
                                        ApprovalDecision(session: s, approval: a, now: now, denying: $denying, chosen: $chosen)
                                            .padding(.top, 16)
                                    }
                                }
                            }
                            .padding(.horizontal, DS.Space.card).padding(.vertical, 12)
                            .readableColumn()
                        }
                        .scrollDismissesKeyboard(.interactively)
                        .onAppear { if SessionDebug.shows("reason") { denying = true } }
                    } else if let outcome = d.resolved[approval] {
                        handled(outcome, by: d.resolvedBy[approval], machine: s.map { machineLabel(state, id: $0.machineId) } ?? "")
                    } else {
                        PageNote(s == nil ? lookupNote(finder.lookup) ?? "读取中…" : "读取中…")
                    }
                }
            }
            .navBar(
                "审批详情", subtitle: s.map { titleOf($0) + (a.flatMap { ApprovalDeadline($0, now: now) }.map { " · \($0.text)" } ?? "") },
                // Orange in its last minute, as on the dock and the home page's card.
                subtitleColor: a.flatMap { ApprovalDeadline($0, now: now) }?.last == true ? DS.orangeInk : nil)
        }
        .followSession(session, chat: chat, finder: finder)
    }

    /// The approval was answered, here or elsewhere: how, and by whom when the events say.
    @ViewBuilder private func handled(_ outcome: ApprovalOutcome, by origin: PBOrigin?, machine: String) -> some View {
        let who = originLabel(origin, state: model.state, me: context.me, machine: machine)
        let how = [decisionText(outcome.decision), who ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")
        let kept = context.sessionStore.reason(approval)
        VStack(spacing: 0) {
            Text("这条审批已经处理了。").type(T.sm, DS.ink2).multilineTextAlignment(.center)
            if !how.isEmpty { Text(how).type(T.meta, DS.ink2).multilineTextAlignment(.center).padding(.top, 4) }
            if !kept.isEmpty {
                // What was being typed is the user's: kept to be copied, not sent anywhere.
                Code(kept).textSelection(.enabled).padding(.top, 16)
                TextAction("复制", alignment: .center) { UIPasteboard.general.string = kept }
            }
        }
        .frame(maxWidth: .infinity)
        .padding(24)
        .readableColumn()
    }
}
