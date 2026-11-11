// The decision on an approval, on the dark card (design-system §6 「终端卡」),
// wherever an approval is drawn: the home page's card, the session's dock
// and the approval's own page. 不允许 asks for a reason in place; once
// decided the card is one line of where that stands (`ApprovalSubmitted`).
//
// What was decided, and how far it got, is the account's
// (`SessionStore.Resolve`), not a view's: a decision made on one screen is
// locked on the others. A tap locks both buttons until the computer
// confirms; an unknown outcome only offers to send the same decision again.
import RACore
import RAProto
import SwiftUI

extension SessionStore.Resolve {
    /// A decision was made, is on its way, or did not arrive: the card is one line.
    var submitted: Bool { decided || running || frozen || !error.isEmpty }
}

/// The reason field while refusing, the two buttons, and under them 允许，这类请求以后不再问 where the computer offers it.
struct ApprovalDecision: View {
    let session: SessionView
    let approval: PBApprovalRequested
    let now: Int64
    /// The command is not shown whole: 看完整命令 stands where 允许 would.
    var cut = false
    var onFull: () -> Void = {}
    /// Not known yet to be whole (not laid out): 允许 waits.
    var measured = true
    @Binding var denying: Bool
    @Binding var chosen: Bool?
    /// The debug signals that press 允许 and 不允许 here.
    var signals = ("session.allow", "session.deny")
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @FocusState private var reasonFocus: Bool

    var body: some View {
        let state = model.state
        let truncated = approvalTruncated(approval)
        // Out of reach of the server nothing can be decided, nor once the time is up.
        let locked = state.unlinked || (approval.hasDeadline && approval.deadline.seconds * 1000 <= now)
        let always = capabilities(state.machines[session.machineId])?.approvalAlways == true
        VStack(alignment: .leading, spacing: 0) {
            if denying {
                TextField("说说该怎么做，可以不填", text: reason, axis: .vertical)
                    .lineLimit(1...4)
                    .font(.body)
                    .foregroundStyle(DS.ink)
                    .focused($reasonFocus)
                    .padding(.horizontal, 14).padding(.vertical, 13)
                    .background(Color.white.opacity(0.1), in: RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                    .padding(.bottom, 12)
                    .accessibilityLabel("不允许的理由")
                    .onAppear { reasonFocus = true }
            }
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { row(locked: locked, blocked: truncated || !measured) }
                VStack(spacing: 8) { row(locked: locked, blocked: truncated || !measured) }
            }
            // Nothing can be decided now: the line that would allow is not offered, as the buttons are greyed.
            if always && !denying && !cut && !truncated && !locked {
                LinkButton("允许，这类请求以后不再问") { decide(true, always: true) }
                    .font(.subheadline)
                    .frame(maxWidth: .infinity)
                    .padding(.bottom, -8)
            }
        }
        // On a wide screen the row does not stretch past 480 (design-system §6 「终端卡」).
        .frame(maxWidth: 480, alignment: .leading)
        .frame(maxWidth: .infinity, alignment: .leading)
        .debugSignal(signals.0) { decide(true) }
        .debugSignal(signals.1) { decide(false, message: SessionDebug.text.jvmTrim()) }
    }

    @ViewBuilder private func row(locked: Bool, blocked: Bool) -> some View {
        if denying {
            Button("取消") { denying = false }.buttonStyle(.darkCard())
            Button("不允许") { decide(false, message: context.sessionStore.reason(approval.approvalID).jvmTrim()) }
                .buttonStyle(.darkCard(primary: true)).disabled(locked)
        } else {
            Button("不允许") { denying = true }.buttonStyle(.darkCard()).disabled(locked)
            if cut {
                Button("看完整命令", action: onFull).buttonStyle(.darkCard(primary: true))
            } else {
                Button("允许") { decide(true) }.buttonStyle(.darkCard(primary: true)).disabled(locked || blocked)
            }
        }
    }

    private var reason: Binding<String> {
        let store = context.sessionStore
        let id = approval.approvalID
        return Binding(get: { store.reason(id) }, set: { store.setReason(id, $0) })
    }

    private func decide(_ allow: Bool, message: String = "", always: Bool = false) {
        guard !isSkeleton(approval), !allow || !approvalTruncated(approval) else { return }
        SessionHaptics.tap()
        // The card shrinks to the line of what was answered as it is answered (§4 动效).
        withAnimation(Motion.enter()) {
            chosen = allow
            _ = context.sessionStore.decide(
                context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID, allow: allow,
                message: message, always: always)
        }
    }
}

/// Submitted: one line of where the decision stands (「已允许」「已拒绝」), or
/// why it did not arrive and 重试, which sends the same choice again. The
/// next thing that waits comes up once this one is gone from the session.
struct ApprovalSubmitted: View {
    let session: SessionView
    let approval: PBApprovalRequested
    let chosen: Bool?
    /// Docked over the chat, with the shadow of what floats; on the home page it is in the list.
    var floating = true
    @Environment(AccountContext.self) private var context

    var body: some View {
        let r = context.sessionStore.resolve(approval.approvalID)
        let allow = (r.running ? chosen : nil) ?? r.allow
        let failed = !r.running && (r.frozen || !r.error.isEmpty)
        let why: String = r.expired
            ? "不确定「\(allow ? "允许" : "拒绝")」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
            : "没送到电脑：\(r.error.isEmpty ? "结果未知" : r.error)"
        DarkCard(floating: floating) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusMark(.waiting)
                Text(approvalHead(approval)).fontWeight(.semibold).foregroundStyle(DS.orange).lineLimit(1).layoutPriority(1)
                Text(failed ? why : allow ? "已允许" : "已拒绝")
                    .foregroundStyle(failed ? DS.red : DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .multilineTextAlignment(.trailing)
                if failed && !r.expired {
                    LinkButton("重试") {
                        context.sessionStore.retryDecision(
                            context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID)
                    }
                    .fontWeight(.semibold)
                }
            }
            .font(.footnote)
            .accessibilityElement(children: .combine)
        }
    }
}

/// What an approval would run, whole, and why: the dock's and the page's content, which scrolls above the buttons.
struct ApprovalContent: View {
    let approval: PBApprovalRequested
    var denying = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if isSkeleton(approval) {
                Text(skeletonMessage).font(.subheadline).foregroundStyle(DS.ink2)
            } else {
                CommandBlock(approvalBody(approval)).accessibilityLabel("命令：" + approvalBody(approval))
                if approvalTruncated(approval) {
                    Text(approvalTruncatedMessage).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
                }
                if !denying && !approval.description_p.isEmpty {
                    Text(approvalDisplay(approval.description_p)).font(.subheadline).foregroundStyle(DS.ink2).padding(.top, 12)
                }
            }
        }
    }
}
