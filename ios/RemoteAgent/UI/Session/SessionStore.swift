// What the session screens keep in memory as pure shell UI state: drafts
// being typed, denial reasons, temporary approval choices before Core confirms,
// dock fold toggles, multi-select question picks, and schedule scroll jumps.
// Command delivery, retries, unknowns, outbox, and question progression are
// owned by Core (`SessionViewModel` + `CoreHost`).
import Foundation
import Observation
import RACore
import RAProto

@MainActor
@Observable
final class SessionStore {
    @ObservationIgnored private let draftFiles: (any RACore.SealedFiles)?
    @ObservationIgnored private let draftQueue: DispatchQueue?
    @ObservationIgnored private var loadedDrafts = Set<String>()
    @ObservationIgnored private var pendingDraftEdits = Set<String>()
    @ObservationIgnored private var forgottenDrafts = Set<String>()
    private enum DraftError {
        case restore, save
        var text: String {
            switch self {
            case .restore: return L10n.composerIosDraftRestoreFailed()
            case .save: return L10n.composerIosDraftSaveFailed()
            }
        }
    }
    private var draftErrorStates: [String: DraftError] = [:]
    var draftErrors: [String: String] { draftErrorStates.mapValues(\.text) }

    init(draftFiles: (any RACore.SealedFiles)? = nil, draftQueue: DispatchQueue? = DispatchQueue(label: "remote-agent.draft-text")) {
        self.draftFiles = draftFiles
        self.draftQueue = draftQueue
    }

    struct ScheduledJump: Equatable { var runID: String; var seq: UInt64 }
    private var scheduledJumps: [String: ScheduledJump] = [:]
    func jumpToScheduledRun(_ sid: String, runID: String, seq: UInt64) { scheduledJumps[sid] = ScheduledJump(runID: runID, seq: seq) }
    func scheduledJump(_ sid: String) -> ScheduledJump? { scheduledJumps[sid] }
    func clearScheduledJump(_ sid: String) { scheduledJumps[sid] = nil }

    // MARK: Drafts

    private var drafts: [String: String] = [:]
    private var reasons: [String: String] = [:]

    private func draftName(_ key: String) -> String { sha256(Data(key.utf8)).hex }

    /// What is being typed in a session's message box.
    func draft(_ sid: String) -> String {
        if forgottenDrafts.contains(sid) { return "" }
        if !sid.hasPrefix("schedule:"), !loadedDrafts.contains(sid), let draftFiles {
            do {
                let saved = try draftFiles.get(folder: "composer-text", name: draftName(sid)).flatMap { String(data: $0, encoding: .utf8) }
                loadedDrafts.insert(sid)
                draftErrorStates[sid] = nil
                if pendingDraftEdits.remove(sid) != nil { persistDraftText(sid, drafts[sid] ?? "") }
                else { drafts[sid] = saved }
            } catch {
                if draftErrorStates[sid] != .restore { draftErrorStates[sid] = .restore }
            }
        }
        return drafts[sid] ?? ""
    }

    func setDraft(_ sid: String, _ text: String) {
        guard !forgottenDrafts.contains(sid) else { return }
        guard draft(sid) != text else { return }
        drafts[sid] = text.isEmpty ? nil : text
        guard !sid.hasPrefix("schedule:"), draftFiles != nil else { return }
        guard loadedDrafts.contains(sid) else { pendingDraftEdits.insert(sid); return }
        persistDraftText(sid, text)
    }

    func appendToDraft(_ sid: String, _ text: String) {
        let current = draft(sid)
        let kept = current.jvmTrimEnd()
        setDraft(sid, current.jvmIsBlank ? text : kept + "\n" + text)
    }

    private func persistDraftText(_ sid: String, _ text: String) {
        guard let draftFiles else { return }
        let name = draftName(sid)
        let bytes = Data(text.utf8)
        let write: @Sendable () -> Bool = {
            do {
                if bytes.isEmpty { try draftFiles.remove(folder: "composer-text", name: name) }
                else { try draftFiles.put(folder: "composer-text", name: name, data: bytes) }
                return true
            } catch { return false }
        }
        let finish: @MainActor @Sendable (Bool) -> Void = { [weak self] saved in
            guard let self, !self.forgottenDrafts.contains(sid) else { return }
            self.draftErrorStates[sid] = saved ? nil : .save
        }
        if let draftQueue {
            draftQueue.async { let saved = write(); DispatchQueue.main.async { finish(saved) } }
        } else { finish(write()) }
    }

    func forgetSession(_ sid: String) {
        guard forgottenDrafts.insert(sid).inserted else { return }
        drafts[sid] = nil
        draftErrorStates[sid] = nil
        guard let draftFiles else { return }
        let name = draftName(sid)
        let remove: @Sendable () -> Void = { try? draftFiles.remove(folder: "composer-text", name: name) }
        if let draftQueue { draftQueue.async(execute: remove) } else { remove() }
    }

    /// The reason being typed for refusing an approval.
    func reason(_ approvalId: String) -> String { reasons[approvalId] ?? "" }

    func setReason(_ approvalId: String, _ text: String) {
        if reasons[approvalId] ?? "" != text { reasons[approvalId] = text.isEmpty ? nil : text }
    }

    // MARK: Local image keep lock

    private var keeping = Set<String>()

    func isKeeping(_ sid: String) -> Bool { keeping.contains(sid) }

    func setKeeping(_ sid: String, _ on: Bool) {
        if on { keeping.insert(sid) } else { keeping.remove(sid) }
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
    /// The session screens' memory for this account.
    var sessionStore: SessionStore { memory.value(SessionStore.self) { SessionStore(draftFiles: draftFiles) } }
}
