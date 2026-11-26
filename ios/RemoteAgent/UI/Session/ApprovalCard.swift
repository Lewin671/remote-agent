// The decision on an approval, on the dark card (design-system §6 「终端卡」),
// wherever an approval is drawn: the home page's card, the session's dock
// and the approval's own page. 不允许 asks for a reason in place; once
// decided the card shrinks to one status line of Core's `PBCoreSessionCommandState`
// (unless Core's `holdOpenCard` keeps the deny form open during delivery/failure).
import RACore
import RAProto
import SwiftUI

/// Shared lifecycle helpers for Core's `PBCoreSessionCommandState`.
extension PBCoreSessionCommandState {
    var isRunning: Bool { status == .pending || busy }
    var isFailed: Bool { !isRunning && (status == .failed || status == .unknown || frozen) }

    /// Localized failure/status copy: prefers `statusText` (covers both FAILED and UNKNOWN/expired
    /// slots from Core), then `errorText`, then raw `errorCode`.
    var renderedErrorText: String {
        if !isRunning && hasStatusText {
            let text = statusText.rendered()
            if !text.isEmpty { return text }
        }
        if hasErrorText {
            let text = errorText.rendered()
            if !text.isEmpty { return text }
        }
        return errorCode
    }
}

/// Pure rule: closes a sheet/dialog/screen when Core's command slot advances strictly past `baseline`.
func shouldCloseAfterCommandSuccess(baseline: UInt64?, current: UInt64) -> Bool {
    guard let baseline else { return false }
    return current > baseline
}

/// Pure rule: `QuestionScreen` dismisses only when the command succeeded past `baseline` AND this was the last step.
func shouldDismissQuestionScreen(baseline: UInt64?, current: UInt64, isLastStep: Bool) -> Bool {
    shouldCloseAfterCommandSuccess(baseline: baseline, current: current) && isLastStep
}

/// Pure rule: multi-select questions require at least one chosen option before submitting.
func shouldSubmitMultiSelect(_ optionNums: [Int32]) -> Bool {
    !optionNums.isEmpty
}

/// Approval metadata + Core command state needed to decide or render a submitted decision line.
struct ApprovalTarget {
    let isHome: Bool
    let sessionId: String
    let approvalId: String
    let head: String
    let truncated: Bool
    let isSkeleton: Bool
    let locked: Bool
    let commandState: PBCoreSessionCommandState?
    let canAlwaysAllow: Bool
    let alwaysLabel: String
    let denyPlaceholder: String
    let denyingOpen: Bool
    let holdOpenCard: Bool

    init(sessionId: String, dock: PBSessionDockView) {
        let a = dock.approval
        self.isHome = false
        self.sessionId = sessionId
        self.approvalId = a.approvalID
        self.head = a.commandPreview.isEmpty ? a.toolName : a.commandPreview
        self.truncated = a.bodyTruncated
        self.isSkeleton = dock.isSkeleton || a.isSkeleton
        self.locked = self.isSkeleton
        self.commandState = dock.hasCommand ? dock.command : nil
        self.canAlwaysAllow = a.canAlwaysAllow
        self.alwaysLabel = a.hasAlwaysLabel ? a.alwaysLabel.rendered() : L10n.approvalAlwaysAllow()
        self.denyPlaceholder = a.hasDenyReasonPlaceholder ? a.denyReasonPlaceholder.rendered() : L10n.approvalReasonPlaceholder()
        self.denyingOpen = a.denyingOpen
        self.holdOpenCard = a.holdOpenCard
    }

    init(card: PBCoreAttentionCardView) {
        self.isHome = true
        self.sessionId = card.sessionID
        self.approvalId = card.approvalID
        self.head = card.toolName.isEmpty ? card.kindText.rendered() : card.toolName
        self.truncated = card.truncated
        self.isSkeleton = card.isSkeleton
        self.locked = card.locked || card.isSkeleton
        self.commandState = card.hasCommand ? card.command : nil
        self.canAlwaysAllow = card.alwaysKind != .unspecified
        self.alwaysLabel = card.hasAlwaysLabel ? card.alwaysLabel.rendered() : L10n.approvalAlwaysAllow()
        self.denyPlaceholder = card.hasDenyReasonPlaceholder ? card.denyReasonPlaceholder.rendered() : L10n.approvalReasonPlaceholder()
        self.denyingOpen = card.denyingOpen
        self.holdOpenCard = card.holdOpenCard
    }

    var isRunning: Bool { commandState?.isRunning == true }
    var isFailed: Bool { commandState?.isFailed == true }
    var isSubmitted: Bool { isRunning || isFailed || commandState?.frozen == true }
    var showCompactSubmitted: Bool { isSubmitted && !holdOpenCard }
    var errorText: String { commandState?.renderedErrorText ?? "" }

    func editDeny(host: CoreHost, open: Bool) async {
        if isHome {
            await host.homeEditApprovalDeny(sessionID: sessionId, approvalID: approvalId, denyingOpen: open)
        } else {
            await host.editApprovalDeny(sessionID: sessionId, approvalID: approvalId, denyingOpen: open)
        }
    }

    func resolve(host: CoreHost, allow: Bool, always: Bool = false, denyReason: String = "") async {
        if isHome {
            await host.homeResolveApproval(sessionID: sessionId, approvalID: approvalId, allow: allow, always: always, denyReason: denyReason)
        } else {
            await host.resolveApproval(sessionID: sessionId, approvalID: approvalId, allow: allow, always: always, denyReason: denyReason)
        }
    }

    func retry(host: CoreHost) async {
        if isHome {
            await host.homeRetryAttention(sessionID: sessionId, itemID: approvalId)
        } else if let slot = commandState?.slotKey, !slot.isEmpty {
            await host.retrySessionCommand(sessionID: sessionId, slotKey: slot)
        }
    }

    func abandon(host: CoreHost) async {
        if isHome {
            await host.homeAbandonAttention(sessionID: sessionId, itemID: approvalId)
        } else if let slot = commandState?.slotKey, !slot.isEmpty {
            await host.abandonSessionCommand(sessionID: sessionId, slotKey: slot)
        }
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
    /// Debug override to start with the deny form open.
    var forceDenying = false
    /// The debug signals that press 允许 and 不允许 here.
    var signals = ("session.allow", "session.deny")
    @Environment(AccountContext.self) private var context
    @FocusState private var reasonFocus: Bool

    var body: some View {
        let sessionHeader = context.core.sessions[target.sessionId]?.header
        let unlinked = sessionHeader?.computerUnlinked ?? context.core.settings.unlinked
        let denying = target.denyingOpen || forceDenying
        let locked = target.locked || unlinked || target.commandState?.expired == true || target.isSkeleton
        let sendingDeny = denying && target.isRunning
        let failedDeny = denying && target.isFailed
        VStack(alignment: .leading, spacing: 0) {
            if denying {
                TextField(target.denyPlaceholder, text: reason, axis: .vertical)
                    .lineLimit(1...4)
                    .font(.body)
                    .foregroundStyle(sendingDeny ? DS.ink2 : DS.ink)
                    .disabled(sendingDeny)
                    .focused($reasonFocus)
                    .padding(.horizontal, 14).padding(.vertical, 13)
                    .background(Color.white.opacity(0.1), in: RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                    .padding(.bottom, 12)
                    .accessibilityLabel(L10n.approvalDenialReason())
                    .onAppear { reasonFocus = true }
            }
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { row(denying: denying, locked: locked, blocked: target.truncated || !measured, sendingDeny: sendingDeny, failedDeny: failedDeny) }
                VStack(spacing: 8) { row(denying: denying, locked: locked, blocked: target.truncated || !measured, sendingDeny: sendingDeny, failedDeny: failedDeny) }
            }
            if failedDeny {
                let err = target.errorText.isEmpty ? L10n.approvalIosUnknownResult() : target.errorText
                Text(L10n.approvalDeliveryFailed(reason: err))
                    .font(.footnote)
                    .foregroundStyle(DS.red)
                    .padding(.top, 10)
            }
            if target.canAlwaysAllow && !denying && !cut && !target.truncated && !locked {
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
        .debugSignal(signals.1) { decide(false, message: SessionDebug.text.trimmingCharacters(in: .whitespacesAndNewlines)) }
    }

    @ViewBuilder private func row(denying: Bool, locked: Bool, blocked: Bool, sendingDeny: Bool, failedDeny: Bool) -> some View {
        if denying {
            let denyLabel = sendingDeny ? L10n.approvalDenying() : (failedDeny ? L10n.approvalRetryDeny() : L10n.approvalDeny())
            Button(L10n.commonCancel()) { setDenying(false) }
                .buttonStyle(.darkCard())
                .disabled(sendingDeny)
            Button(denyLabel) { decide(false, message: context.sessionStore.reason(target.approvalId)) }
                .buttonStyle(.darkCard(primary: true))
                .disabled(locked || sendingDeny)
        } else {
            Button(L10n.approvalDeny()) { setDenying(true) }.buttonStyle(.darkCard()).disabled(locked)
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

    private func setDenying(_ open: Bool) {
        let host = context.coreHost
        let target = target
        Task { await target.editDeny(host: host, open: open) }
    }

    private func decide(_ allow: Bool, message: String = "", always: Bool = false) {
        guard !target.isSkeleton, !allow || !target.truncated else { return }
        SessionHaptics.tap()
        let host = context.coreHost
        let target = target
        Task { await target.resolve(host: host, allow: allow, always: always, denyReason: message) }
    }
}

/// Submitted: one line of where the decision stands (Core's `submittedSummary`), or
/// why it did not arrive and 重试 / 取消, delegated to Core semantic actions.
struct ApprovalSubmitted: View {
    let target: ApprovalTarget
    /// Docked over the chat, with the shadow of what floats; on the home page it is in the list.
    var floating = true
    @Environment(AccountContext.self) private var context

    var body: some View {
        let st = target.commandState
        let failed = target.isFailed
        let expired = st?.expired == true
        let errText = target.errorText
        let statusLine: String = {
            if !failed {
                if let s = st, s.hasSubmittedSummary {
                    let rendered = s.submittedSummary.rendered()
                    if !rendered.isEmpty { return rendered }
                }
                if let s = st, s.hasStatusText {
                    let rendered = s.statusText.rendered()
                    if !rendered.isEmpty { return rendered }
                }
                return L10n.dockWaitingConfirmation()
            }
            if expired {
                if !errText.isEmpty { return errText }
                if let s = st, s.hasSubmittedSummary {
                    let rendered = s.submittedSummary.rendered()
                    if !rendered.isEmpty { return rendered }
                }
                return L10n.approvalIosUnknownResult()
            }
            return L10n.approvalDeliveryFailed(reason: errText.isEmpty ? L10n.approvalIosUnknownResult() : errText)
        }()
        DarkCard(floating: floating) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusMark(.waiting)
                Text(target.head).fontWeight(.semibold).foregroundStyle(DS.orange).lineLimit(1).layoutPriority(1)
                Text(statusLine)
                    .foregroundStyle(failed ? DS.red : DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .multilineTextAlignment(.trailing)
                if failed && !expired && st?.canRetry == true {
                    LinkButton(L10n.commonRetry()) {
                        let host = context.coreHost
                        let target = target
                        Task { await target.retry(host: host) }
                    }
                    .fontWeight(.semibold)
                }
                if failed && st?.canAbandon == true {
                    LinkButton(L10n.commonCancel()) {
                        let host = context.coreHost
                        let target = target
                        Task { await target.abandon(host: host) }
                    }
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
    var isSkeleton = false
    var denying = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if isSkeleton {
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
