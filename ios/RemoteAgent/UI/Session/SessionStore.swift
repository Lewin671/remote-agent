// Pure shell-local UI state for session screens: denial reasons, temporary
// approval choices before Core confirms, dock fold toggles, multi-select
// question picks, and schedule scroll jumps.
// Composer draft text, attachments, outbox, retries, and command delivery
// are owned by Core (`PBSessionViewModel` + `CoreHost`).
import Foundation
import Observation

@MainActor
@Observable
final class SessionStore {
    struct ScheduledJump: Equatable { var runID: String; var seq: UInt64 }
    private var scheduledJumps: [String: ScheduledJump] = [:]
    func jumpToScheduledRun(_ sid: String, runID: String, seq: UInt64) { scheduledJumps[sid] = ScheduledJump(runID: runID, seq: seq) }
    func scheduledJump(_ sid: String) -> ScheduledJump? { scheduledJumps[sid] }
    func clearScheduledJump(_ sid: String) { scheduledJumps[sid] = nil }

    // MARK: Approval refusal reasons

    private var reasons: [String: String] = [:]

    func reason(_ approvalId: String) -> String { reasons[approvalId] ?? "" }

    func setReason(_ approvalId: String, _ text: String) {
        if reasons[approvalId] ?? "" != text { reasons[approvalId] = text.isEmpty ? nil : text }
    }

    // MARK: Approvals

    struct ApprovalChoice: Equatable, Sendable {
        var allow: Bool
        var message: String
        var always: Bool
    }

    private var approvalChoices: [String: ApprovalChoice] = [:]

    func approvalChoice(_ approvalId: String) -> ApprovalChoice? {
        approvalChoices[approvalId]
    }

    func setApprovalChoice(_ approvalId: String, _ choice: ApprovalChoice?) {
        approvalChoices[approvalId] = choice
    }

    // MARK: The dock

    private var dockChosen: [String: Bool] = [:]
    @ObservationIgnored private var dockCameWhileDrafting: [String: Bool] = [:]

    func dockOpen(_ key: String, drafting: () -> Bool) -> Bool {
        if let chosen = dockChosen[key] { return chosen }
        if let came = dockCameWhileDrafting[key] { return !came }
        let came = drafting()
        dockCameWhileDrafting[key] = came
        return !came
    }

    func setDockOpen(_ key: String, _ open: Bool) {
        dockChosen[key] = open
    }

    // MARK: Questions

    var singlePick: [String: Int] = [:]
    var multiPick: [String: Set<Int>] = [:]
}

extension AccountContext {
    /// The session screens' in-memory UI state for this account.
    var sessionStore: SessionStore { memory.value(SessionStore.self) { SessionStore() } }
}
