// A card of 需要处理 on the home page (Android's AttentionCard in
// ui/Home.kt): a session that waits for the user. An approval is decided
// right on the card; questions, plans and popups open the session.
//
// The buttons follow Android's ApprovalButtons (ui/Dock.kt): a tap locks
// both until the computer confirms, a definite failure offers the same
// decision again, and one whose outcome is unknown is only ever sent again
// as it was (`Attempt`, architecture §10).
import Observation
import RACore
import RAProto
import SwiftUI

/// One approval's decision as it was sent from the home page, for this run.
@MainActor
@Observable
final class HomeApprovalDecision {
    @ObservationIgnored private let attempt = Attempt()
    private(set) var allow = false
    @ObservationIgnored private var message = ""
    @ObservationIgnored private var always = false
    /// A decision was made here; the buttons stay away from then on.
    private(set) var decided = false
    private(set) var running = false
    private(set) var error = ""
    /// The decision's outcome is unknown, and it expired.
    private(set) var frozen = false
    private(set) var expired = false
    /// What is typed as the reason for 不允许, kept while the card is not on screen.
    var reason = ""

    /// Sends `allow`, or the earlier decision while its outcome is unknown.
    func resolve(_ remote: Remote, session: SessionView, approvalId: String, allow: Bool, message: String = "", always: Bool = false) {
        guard !running else { return }
        decided = true
        running = true
        error = ""
        let retry = attempt.frozen
        Task {
            do {
                let prepared = try attempt.next {
                    self.allow = allow
                    self.message = message
                    self.always = always
                    return try remote.prepare(
                        machineId: session.machineId, sessionId: session.id,
                        resolveApprovalCommand(sessionId: session.id, approvalId: approvalId, allow: allow, message: message, always: always))
                }
                try await remote.forward(prepared, retryOfUnknown: retry)
                attempt.settle()
            } catch {
                self.error = errorText(attempt.settle(error) ?? error)
            }
            frozen = attempt.frozen
            expired = attempt.expired
            running = false
        }
    }

    /// The same decision again, after it did not arrive.
    func retry(_ remote: Remote, session: SessionView, approvalId: String) {
        resolve(remote, session: session, approvalId: approvalId, allow: allow, message: message, always: always)
    }
}

/// The decisions made from the home page, by approval, until the account changes.
@MainActor
final class HomeApprovals {
    private var byApproval: [String: HomeApprovalDecision] = [:]

    func of(_ approvalId: String) -> HomeApprovalDecision {
        if let d = byApproval[approvalId] { return d }
        let d = HomeApprovalDecision()
        byApproval[approvalId] = d
        return d
    }
}

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
                HomeApprovalButtons(state: state, session: session, approval: approval)
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

/// 不允许 / 允许 on a card, and a reason with 不允许.
private struct HomeApprovalButtons: View {
    let state: RemoteState
    let session: SessionView
    let approval: PBApprovalRequested
    @Environment(AccountContext.self) private var context
    @State private var reasonOpen = false
    @FocusState private var reasonFocused: Bool

    var body: some View {
        let decision = context.memory.value { HomeApprovals() }.of(approval.approvalID)
        let remote = context.remote
        let id = approval.approvalID
        VStack(alignment: .leading, spacing: 0) {
            if decision.running || decision.frozen || decision.decided {
                if decision.running || (decision.error.isEmpty && !decision.frozen) {
                    IconText(
                        decision.running ? Lucide.loaderCircle : Lucide.check, decision.running ? "正在送到电脑…" : "已提交，等待电脑确认",
                        style: T.meta)
                }
            } else if reasonOpen {
                TextField(
                    "", text: Bindable(decision).reason, prompt: Text("拒绝理由（可选）").foregroundStyle(Theme.muted), axis: .vertical
                )
                .lineLimit(1...4)
                .type(T.body, Theme.fg)
                .focused($reasonFocused)
                .tint(Theme.blue)
                .accessibilityLabel("拒绝理由（可选）")
                .padding(.horizontal, 14).padding(.vertical, 11)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
                .padding(.bottom, 8)
                .onAppear { reasonFocused = true }
                Btns {
                    Btn("取消", background: Theme.card) { reasonOpen = false }
                    Btn("不允许", kind: .negative, background: Theme.card) {
                        Haptics.confirm()
                        decision.resolve(
                            remote, session: session, approvalId: id, allow: false,
                            message: decision.reason.trimmingCharacters(in: .whitespacesAndNewlines))
                    }
                }
            } else {
                Btns {
                    Btn("不允许", kind: .negative, background: Theme.card) { reasonOpen = true }
                    Btn("允许", kind: .primary) {
                        Haptics.confirm()
                        decision.resolve(remote, session: session, approvalId: id, allow: true)
                    }
                }
                if capabilities(state.machines[session.machineId])?.approvalAlways == true {
                    TextAction("允许，这类请求以后不再问", alignment: .center) {
                        Haptics.confirm()
                        decision.resolve(remote, session: session, approvalId: id, allow: true, always: true)
                    }
                    .frame(maxWidth: .infinity)
                    // Its 44 pt to the finger reach into the card's own padding.
                    .padding(.bottom, -8)
                }
            }
            if !decision.running && decision.frozen {
                Banner(
                    decision.expired
                        ? "不确定「\(decision.allow ? "允许" : "拒绝")」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
                        : "没送到电脑：\(decision.error.isEmpty ? "结果未知" : decision.error)",
                    tone: .error, action: decision.expired ? nil : "重试",
                    onAction: { decision.retry(remote, session: session, approvalId: id) }
                )
                .padding(.top, 8)
            } else if !decision.running && !decision.error.isEmpty {
                Banner("没送到电脑：\(decision.error)", tone: .error, action: "重试", onAction: { decision.retry(remote, session: session, approvalId: id) })
                .padding(.top, 8)
            }
        }
    }
}
