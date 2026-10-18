// 添加设备 as its steps, apart from how it is drawn (the state of Android's
// PairScreen): a code is made and shown, a new device asks to join with
// it, the four pictures are compared here, and allowing hands the root key
// over. Only the new device confirming it has the key counts as done
// (architecture §9): anything else after allowing leaves the outcome open,
// and is said that way.
import Foundation
import Observation
import RACore
import RAProto

/// One pairing started from this device. `Initiation` is the real one.
protocol PairingInitiation: Sendable {
    var code: String { get }
    var link: PairLink { get }
    func joined() async throws -> (emoji: [String], kind: PBDeviceKind)
    func approve() async throws
    func cancelOnServer() async
}

extension Initiation: PairingInitiation {}

@MainActor
@Observable
final class AddDeviceModel {
    enum Phase: Equatable {
        /// Asking the server for a code.
        case starting
        /// The code is shown; no device has asked yet.
        case waiting(code: String, link: PairLink)
        /// A device asks to join: its pictures, to compare.
        case compare(emoji: [String], machine: Bool)
        /// Allowed: the key is on its way.
        case sending(emoji: [String], machine: Bool)
        case done
        /// It ended otherwise. `uncertain`: the key was offered and nothing says whether it was taken.
        case failed(String, uncertain: Bool)
    }

    private(set) var phase = Phase.starting
    private(set) var canRetryApproval = false
    @ObservationIgnored private var approved: (emoji: [String], machine: Bool)?
    @ObservationIgnored private let begin: @Sendable () async throws -> any PairingInitiation
    @ObservationIgnored private var pairing: (any PairingInitiation)?
    /// Bumped by each start and by leaving: a pairing that answers after that is not this page's any more.
    @ObservationIgnored private var run = 0

    init(begin: @escaping @Sendable () async throws -> any PairingInitiation) {
        self.begin = begin
    }

    /// Asks for a code and waits for a device to join with it. Also 换一个, after a failure.
    func start() {
        run += 1
        let run = run
        // The pairing before, if it is still open, is withdrawn.
        withdraw()
        phase = .starting
        Task {
            do {
                let pairing = try await begin()
                guard run == self.run else {
                    await pairing.cancelOnServer()
                    return
                }
                self.pairing = pairing
                phase = .waiting(code: pairing.code, link: pairing.link)
                let (emoji, kind) = try await pairing.joined()
                guard run == self.run else { return }
                phase = .compare(emoji: emoji, machine: kind == .machine)
            } catch {
                guard run == self.run else { return }
                phase = .failed(errorWords(error), uncertain: false)
            }
        }
    }

    /// 图案一致，允许.
    func approve() {
        guard case .compare(let emoji, let machine) = phase, let pairing else { return }
        approved = (emoji, machine)
        sendApproval(pairing, emoji: emoji, machine: machine)
    }

    /// The failure action preserves the operation that was already approved.
    func retry() {
        guard case .failed = phase else { return }
        if canRetryApproval, let pairing, let approved {
            sendApproval(pairing, emoji: approved.emoji, machine: approved.machine)
        } else { start() }
    }

    private func sendApproval(_ pairing: any PairingInitiation, emoji: [String], machine: Bool) {
        canRetryApproval = false
        phase = .sending(emoji: emoji, machine: machine)
        let run = run
        Task {
            do {
                try await pairing.approve()
                guard run == self.run else { return }
                self.pairing = nil
                approved = nil
                phase = .done
            } catch {
                guard run == self.run else { return }
                canRetryApproval = RACore.canRetryPairApproval(error)
                // The pairing is kept: the server may hold the key for the
                // device that asked, and leaving or 换一个 has to withdraw it.
                // The pairing's own words say what is known. Anything else
                // broke after the key was offered: the new device has to be looked at.
                if let known = error as? PairError {
                    phase = .failed(known.message, uncertain: known.message.contains("不确定") || known.message.contains("可能"))
                } else {
                    phase = .failed("不确定新设备收到没有，到新设备上看一眼是否已加入：" + errorText(error), uncertain: true)
                }
            }
        }
    }

    /// 拒绝, and leaving the page before the end: the pairing is cancelled on the server.
    func leave() {
        run += 1
        withdraw()
    }

    /// Cancels the pairing on the server unless it is done, also one whose
    /// allowing failed: the key may be there for the taking until it expires.
    private func withdraw() {
        canRetryApproval = false
        approved = nil
        guard let pairing else { return }
        self.pairing = nil
        if phase != .done { Task { await pairing.cancelOnServer() } }
    }

    private func errorWords(_ error: Error) -> String {
        (error as? PairError)?.message ?? errorText(error)
    }

    #if DEBUG
    /// A fixed step to look at, without a server (`-addDeviceStep`).
    func show(_ phase: Phase) {
        run += 1
        self.phase = phase
    }
    #endif
}
