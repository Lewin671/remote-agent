// The decision on an approval, on the dark card (design-system §6 「终端卡」),
// wherever an approval is drawn: the home page's card, the session's dock
// and the approval's own page. 不允许 asks for a reason in place; once
// decided the card shrinks to one status line of Core's `PBSessionCommandState`.
import RACore
import RAProto
import SwiftUI

/// Approval metadata + Core command state needed to decide or render a submitted decision line.
struct ApprovalTarget {
    let sessionId: String
    let machineId: String
    let approvalId: String
    let observedSeq: UInt64
    let head: String
    let truncated: Bool
    let isSkeleton: Bool
    let expired: Bool
    let commandState: PBSessionCommandState?
    let canAlwaysAllow: Bool
    let alwaysLabel: String
    let denyPlaceholder: String

    init(
        sessionId: String,
        machineId: String,
        dock: PBSessionDockView
    ) {
        let a = dock.approval
        self.sessionId = sessionId
        self.machineId = machineId
        self.approvalId = a.approvalID
        self.observedSeq = dock.observedSeq
        self.head = a.commandPreview.isEmpty ? a.toolName : a.commandPreview
        self.truncated = a.bodyTruncated
        self.isSkeleton = a.isSkeleton
        self.expired = false
        self.commandState = dock.hasCommand ? dock.command : nil
        self.canAlwaysAllow = a.canAlwaysAllow
        self.alwaysLabel = a.hasAlwaysLabel ? a.alwaysLabel.rendered() : L10n.approvalAlwaysAllow()
        self.denyPlaceholder = a.hasDenyReasonPlaceholder ? a.denyReasonPlaceholder.rendered() : L10n.approvalReasonPlaceholder()
    }

    init(card: PBCoreAttentionCardView, commandState: PBSessionCommandState?) {
        self.sessionId = card.sessionID
        self.machineId = card.machineID
        self.approvalId = card.approvalID
        self.observedSeq = 0
        self.head = card.toolName
        self.truncated = card.truncated
        self.isSkeleton = false
        self.expired = false
        self.commandState = commandState
        self.canAlwaysAllow = card.alwaysKind != .unspecified
        self.alwaysLabel = L10n.approvalAlwaysAllow()
        self.denyPlaceholder = L10n.approvalReasonPlaceholder()
    }

    var isRunning: Bool {
        commandState?.status == .pending || commandState?.busy == true
    }

    var isFailed: Bool {
        guard let st = commandState, !isRunning else { return false }
        return st.status == .failed || st.status == .unknown || st.frozen
    }

    func isSubmitted(choice: SessionStore.ApprovalChoice?) -> Bool {
        isRunning || commandState?.frozen == true || isFailed || choice != nil
    }
}

/// The reason field while refusing, the two buttons, and under them the "always allow" action when offered.
struct ApprovalDecision: View {
    let target: ApprovalTarget
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
        let truncated = target.truncated
        let locked = state.peer(target.machineId).unlinked || target.expired || target.isSkeleton
        let always = target.canAlwaysAllow || capabilities(state.machines[target.machineId])?.approvalAlways == true
        VStack(alignment: .leading, spacing: 0) {
            if denying {
                TextField(target.denyPlaceholder, text: reason, axis: .vertical)
                    .lineLimit(1...4)
                    .font(.body)
                    .foregroundStyle(DS.ink)
                    .focused($reasonFocus)
                    .padding(.horizontal, 14).padding(.vertical, 13)
                    .background(Color.white.opacity(0.1), in: RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                    .padding(.bottom, 12)
                    .accessibilityLabel(L10n.approvalDenialReason())
                    .onAppear { reasonFocus = true }
            }
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { row(locked: locked, blocked: truncated || !measured) }
                VStack(spacing: 8) { row(locked: locked, blocked: truncated || !measured) }
            }
            if always && !denying && !cut && !truncated && !locked {
                LinkButton(target.alwaysLabel) { decide(true, always: true) }
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
            Button(L10n.commonCancel()) { denying = false }.buttonStyle(.darkCard())
            Button(L10n.approvalDeny()) { decide(false, message: context.sessionStore.reason(target.approvalId).jvmTrim()) }
                .buttonStyle(.darkCard(primary: true)).disabled(locked)
        } else {
            Button(L10n.approvalDeny()) { denying = true }.buttonStyle(.darkCard()).disabled(locked)
            if cut {
                Button(L10n.approvalFullCommand(), action: onFull).buttonStyle(.darkCard(primary: true))
            } else {
                Button(L10n.approvalAllow()) { decide(true) }.buttonStyle(.darkCard(primary: true)).disabled(locked || blocked)
            }
        }
    }

    private var reason: Binding<String> {
        let store = context.sessionStore
        let id = target.approvalId
        return Binding(get: { store.reason(id) }, set: { store.setReason(id, $0) })
    }

    private func decide(_ allow: Bool, message: String = "", always: Bool = false) {
        guard !target.isSkeleton, !allow || !target.truncated else { return }
        SessionHaptics.tap()
        let store = context.sessionStore
        withAnimation(Motion.enter()) {
            chosen = allow
            store.setApprovalChoice(target.approvalId, .init(allow: allow, message: message, always: always))
        }
        Task {
            await context.coreHost.submitSessionCommand(
                sessionID: target.sessionId,
                command: .with {
                    $0.resolveApproval = .with {
                        $0.sessionID = target.sessionId
                        $0.approvalID = target.approvalId
                        $0.allow = allow
                        $0.message = message
                        $0.always = always
                        if target.observedSeq > 0 {
                            $0.observedEventSeq = target.observedSeq
                        }
                    }
                },
                machineID: target.machineId
            )
        }
    }
}

/// Submitted: one line of where the decision stands (「已允许」「已拒绝」), or
/// why it did not arrive and 重试, which sends the same choice again.
struct ApprovalSubmitted: View {
    let target: ApprovalTarget
    let chosen: Bool?
    /// Docked over the chat, with the shadow of what floats; on the home page it is in the list.
    var floating = true
    @Environment(AccountContext.self) private var context

    var body: some View {
        let saved = context.sessionStore.approvalChoice(target.approvalId)
        let allow = (target.isRunning ? chosen : nil) ?? saved?.allow ?? chosen ?? true
        let st = target.commandState
        let failed = target.isFailed
        let expired = st?.expired == true
        let errText = st.flatMap { s -> String? in
            if s.hasErrorText {
                let text = s.errorText.rendered()
                if !text.isEmpty { return text }
            }
            return s.errorCode.isEmpty ? nil : s.errorCode
        } ?? ""
        let why: String = expired
            ? L10n.approvalIosExpired(decision: allow ? L10n.approvalAllow() : L10n.approvalIosDecisionDeny())
            : L10n.approvalDeliveryFailed(reason: errText.isEmpty ? L10n.approvalIosUnknownResult() : errText)
        DarkCard(floating: floating) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusMark(.waiting)
                Text(target.head).fontWeight(.semibold).foregroundStyle(DS.orange).lineLimit(1).layoutPriority(1)
                Text(failed ? why : allow ? L10n.approvalAllowed() : L10n.approvalDenied())
                    .foregroundStyle(failed ? DS.red : DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .multilineTextAlignment(.trailing)
                if failed && !expired && (st == nil || st!.canRetry) {
                    LinkButton(L10n.commonRetry()) {
                        Task {
                            await context.coreHost.submitSessionCommand(
                                sessionID: target.sessionId,
                                command: .with {
                                    $0.resolveApproval = .with {
                                        $0.sessionID = target.sessionId
                                        $0.approvalID = target.approvalId
                                        $0.allow = allow
                                        $0.message = saved?.message ?? ""
                                        $0.always = saved?.always ?? false
                                        if target.observedSeq > 0 {
                                            $0.observedEventSeq = target.observedSeq
                                        }
                                    }
                                },
                                machineID: target.machineId
                            )
                        }
                    }
                    .fontWeight(.semibold)
                }
            }
            .font(.footnote)
            .accessibilityElement(children: .combine)
        }
    }
}

/// What an approval would run, whole, and why: the dock's and the page's content, rendered from Core's `PBSessionApprovalDockView`.
struct ApprovalContent: View {
    let approval: PBSessionApprovalDockView
    var denying = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if approval.isSkeleton {
                Text(L10n.sessionSkeleton()).font(.subheadline).foregroundStyle(DS.ink2)
            } else {
                CommandBlock(approval.body).accessibilityLabel(L10n.listStateIosCommandLabel(command: approval.body))
                if approval.bodyTruncated {
                    Text(L10n.approvalTruncated()).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
                }
                if !denying && !approval.description_p.isEmpty {
                    Text(approval.description_p).font(.subheadline).foregroundStyle(DS.ink2).padding(.top, 12)
                }
            }
        }
    }
}
