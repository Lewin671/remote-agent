// The join flow's state and rules, apart from how it is drawn: the
// counterpart of the state in Android's ui/Join.kt, with the paths
// architecture §9 describes for a new device that stops waiting.
//
// A join by pairing runs to its own end: it is never cut off in the middle
// of a request. Stopping sets a flag the pairing reads between two looks at
// the server, so it ends within a second, and its one-off key goes with it.
// That leaves one case to get right. The other device may allow the join in
// the same moment this one cancels: the pairing has then already passed its
// last look at the flag and registers this device. Cancelling says 已取消
// only for what really stopped; an attempt that registered anyway hands
// over its account, and one that failed while registering says the outcome
// is not known.
import Foundation
import Observation
import RACore
import UIKit
import os

/// How this device is called in the join flow's sentences.
@MainActor var thisDeviceWord: String {
    UIDevice.current.userInterfaceIdiom == .pad ? "这台 iPad" : "这台手机"
}

/// The calls that reach the server, so that the rules are tested without one.
struct JoinCalls: Sendable {
    var byPairing:
        @Sendable (
            _ link: PairLink, _ onEmoji: @escaping @Sendable ([String]) -> Void,
            _ stopped: @escaping @Sendable () -> Bool,
            _ onRegistering: @escaping @Sendable () -> Void,
            _ confirm: @escaping @Sendable ([String]) async -> Bool
        ) async throws -> Account

    static func live(http: HTTP, existing: Account? = nil, vault: Vault = Vault()) -> JoinCalls {
        JoinCalls(
            byPairing: { link, onEmoji, stopped, onRegistering, confirm in
                if let pending = try vault.loadRegistrationIntent() {
                    guard pending.targetServer == link.server else { throw PairError("另一个服务器的注册尚未完成，请先重试或退出并重新开始") }
                    onRegistering()
                    let account = try await pending.resume(http: http) { account,record in try vault.saveRegistrationBootstrap(account,record:record) }
                    try vault.clearRegistrationIntent(pending)
                    return account
                }
                let joined = try await joinByPairing(
                    link: link, http: http, onEmoji: onEmoji, stopped: stopped,
                    onRegistering: onRegistering,
                    persist: { acct, record in
                        try vault.saveRegistrationBootstrap(acct, record: record)
                    }, persistRegistration: { intent in
                        try vault.saveRegistrationIntent(intent)
                    }, clearRegistration: { try vault.clearRegistrationIntent($0) }, loadIdentity: { server, pairingID in
                        try vault.readNamed(PairingIdentity.storageKey(server: server, pairingID: pairingID)).map { try JSONDecoder().decode(PairingIdentity.self, from: $0) }
                    },
                    persistIdentity: { identity in
                        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
                        try vault.savePairingIdentity(PairingIdentity.storageKey(server: identity.server, pairingID: identity.pairingID), encoder.encode(identity))
                    },
                    existing: existing, confirm: confirm)
                return joined
            })
    }
}

/// One attempt's flags, read by the pairing off the main actor.
private final class JoinAttempt: Sendable {
    private struct Flags {
        var stopped = false
        var registering = false
        var decision: Bool?
        var confirmation: CheckedContinuation<Bool, Never>?
    }

    private let flags = OSAllocatedUnfairLock(initialState: Flags())

    var stopped: Bool { flags.withLock { $0.stopped } }
    var registering: Bool { flags.withLock { $0.registering } }

    func beginRegistering() { flags.withLock { $0.registering = true } }

    /// Stops the attempt unless it is already registering; says whether it stopped.
    func comparisonDecision(_ accepted: Bool) {
        let continuation = flags.withLock { f in
            f.decision = accepted
            if accepted { f.registering = true }
            let waiting = f.confirmation
            f.confirmation = nil
            return waiting
        }
        continuation?.resume(returning: accepted)
    }
    func waitForComparison() async -> Bool {
        await withCheckedContinuation { continuation in
            let immediate: Bool? = flags.withLock { f in
                if f.stopped { return false }
                if let decision = f.decision { return decision }
                f.confirmation = continuation
                return nil
            }
            if let immediate { continuation.resume(returning: immediate) }
        }
    }
    func stop() -> Bool {
        let stopped = flags.withLock { f -> (Bool, CheckedContinuation<Bool, Never>?) in
            if f.registering { return (false, nil) }
            f.stopped = true
            f.decision = false
            let waiting = f.confirmation
            f.confirmation = nil
            return (true, waiting)
        }
        stopped.1?.resume(returning: false)
        return stopped.0
    }

}

@MainActor
@Observable
final class JoinModel {
    enum Step: Equatable {
        /// The code and the server, to fill in.
        case form
        /// The camera, looking for a pairing QR code.
        case scan
        /// The account secret, to paste.
        case secret
        /// Reaching the server and waiting for the other device to show itself.
        case working
        /// The four pictures to compare, waiting for the other device to allow.
        case emoji([String])
    }

    private(set) var step = Step.form
    private(set) var comparisonPending = false
    /// The pairing code: digits only, eight at most. Typed through `typeCode`.
    private(set) var code = ""
    var server: String
    /// The account secret as pasted. Emptied as soon as it has been used, or the page is left.
    var secret = ""
    /// What went wrong or what happened, over the form; empty for nothing.
    private(set) var notice = ""
    /// The notice is a failure, not an account of what the user did.
    private(set) var noticeIsError = false
    /// A join by secret is on its way; it cannot be cancelled.
    private(set) var joiningWithSecret = false

    /// The other device's commitment, from a scanned code: the server's answer is checked against it.
    @ObservationIgnored private var commit: Data?
    @ObservationIgnored private var attempt: JoinAttempt?
    @ObservationIgnored private let calls: JoinCalls
    @ObservationIgnored private let onJoined: @MainActor (Account) async throws -> Void

    let retainedIdentity: Bool
    init(
        lastServer: String, calls: JoinCalls, retainedIdentity: Bool = false,
        onJoined: @escaping @MainActor (Account) async throws -> Void
    ) {
        server = lastServer.isEmpty ? "https://" : lastServer
        self.retainedIdentity = retainedIdentity
        self.calls = calls
        self.onJoined = onJoined
    }

    /// The code as it is shown: two groups of four.
    var shownCode: String {
        code.count > 4 ? code.prefix(4) + " " + code.dropFirst(4) : code
    }

    var canJoin: Bool { code.count == 8 && server.count > 8 }
    var canJoinWithSecret: Bool { !secret.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    /// Working can still be left: a pairing that has not begun to register.
    var canCancel: Bool { !joiningWithSecret && attempt?.registering != true }

    /// What was typed or pasted into the code field: its digits are kept,
    /// so a code copied as "4829 1756" goes in as it is.
    func typeCode(_ text: String) {
        let digits = String(text.filter { $0.isASCII && $0.isNumber }.prefix(8))
        guard digits != code else { return }
        code = digits
        // A code typed by hand is not the scanned one any more.
        commit = nil
    }

    // MARK: Moving between the pages

    func showForm() {
        notice = ""
        secret = ""
        step = .form
    }

    func showSecret() {
        notice = ""
        step = .secret
    }

    func showScanner() {
        notice = ""
        step = .scan
    }

    // MARK: Joining with a code

    /// 加入 on the form: checks the address, then joins with the code typed or scanned.
    func joinWithCode() {
        do {
            join(PairLink(code: code, server: try Secret.checkServer(server), commit: commit))
        } catch {
            fail(errorText(error))
        }
    }

    /// What the camera read. Anything but a pairing link of this app is said
    /// and left. The camera may still hand over a code after 取消 was
    /// tapped: the scanner is not what is shown then, and nothing is joined.
    func scanned(_ text: String) {
        guard step == .scan else { return }
        guard let link = PairLink.parse(text) else {
            notice = "这不是 remote-agent 的配对二维码"
            noticeIsError = true
            step = .form
            return
        }
        typeCode(link.code)
        server = link.server
        // After the code, whose change forgets the commitment of the one before.
        commit = link.commit
        join(link)
    }

    private func join(_ link: PairLink) {
        notice = ""
        step = .working
        let attempt = JoinAttempt()
        self.attempt = attempt
        let calls = calls
        Task {
            do {
                let account = try await calls.byPairing(
                    link,
                    { emoji in Task { @MainActor in self.show(emoji, of: attempt) } },
                    { attempt.stopped },
                    { attempt.beginRegistering() },
                    { emoji in await self.requestComparison(emoji, of: attempt) })
                await finished(attempt, account)
            } catch {
                failed(attempt, error)
            }
        }
    }

    private func requestComparison(_ emoji: [String], of attempt: JoinAttempt) async -> Bool {
        guard self.attempt === attempt, !attempt.stopped else { return false }
        step = .emoji(emoji)
        comparisonPending = true
        return await attempt.waitForComparison()
    }
    func confirmPictures() {
        guard comparisonPending, let attempt else { return }
        comparisonPending = false
        attempt.comparisonDecision(true)
    }
    private func show(_ emoji: [String], of attempt: JoinAttempt) {
        guard self.attempt === attempt, !attempt.stopped, step == .working else { return }
        step = .emoji(emoji)
    }

    private func finished(_ attempt: JoinAttempt, _ account: Account) async {
        // Registered, whatever was tapped meanwhile: the device is in the
        // account, so it is used. An attempt begun after the cancel gives way.
        if self.attempt !== attempt { _ = self.attempt?.stop() }
        self.attempt = nil
        comparisonPending = false
        await hand(over: account)
    }

    private func failed(_ attempt: JoinAttempt, _ error: Error) {
        comparisonPending = false
        let current = self.attempt === attempt
        if attempt.registering {
            // Whatever the page says now, this is what is known of the device.
            if current { self.attempt = nil }
            notice = "不确定\(thisDeviceWord)有没有加入：\(errorText(error))。可以在手机 App 的设置里看看客户端列表"
            noticeIsError = true
            if current { step = .form }
            return
        }
        // Stopped by the user: the page already says so.
        guard current, !attempt.stopped else { return }
        self.attempt = nil
        fail(errorText(error))
    }

    /// Stops waiting, from 取消 or 图案不一致: no more polling or joining by
    /// itself, the one-off pairing key goes with the attempt. The server is
    /// kept and the old code cleared. This device leaving does not withdraw
    /// the request on the server, which may still hold the code: the notice
    /// says to reject it on the other device. Once registering has begun
    /// the join goes on and its outcome is shown instead.
    func cancel() {
        guard let attempt else {
            if !joiningWithSecret { step = .form }
            return
        }
        guard attempt.stop() else {
            notice = "已经在登记\(thisDeviceWord)，稍等结果"
            noticeIsError = false
            return
        }
        self.attempt = nil
        typeCode("")
        notice = "已取消本次加入。旧请求可能还占着配对码：请在手机 App 上拒绝它，再生成新码"
        noticeIsError = false
        step = .form
    }

    // MARK: Joining with the account secret

    func joinWithSecret() {
        secret = ""
        step = .form
        notice = "加入已有账户需要在手机 App 上批准，请使用配对码。"
        noticeIsError = true
    }

    // MARK: The end

    private func hand(over account: Account) async {
        secret = ""
        do {
            try await onJoined(account)
        } catch {
            // Registered, but the Keychain did not keep the keys.
            notice = "已经加入，但没能保存账户密钥：\(errorText(error))"
            noticeIsError = true
            step = .form
        }
    }

    private func fail(_ text: String) {
        notice = text
        noticeIsError = true
        step = .form
    }

    // MARK: Fixed states

    #if DEBUG
        /// Puts the flow in a state to look at, without a server (`-joinStep`).
        func show(
            step: Step, code: String = "", server: String? = nil, secret: String = "",
            notice: String = "", error: Bool = false
        ) {
            typeCode(code)
            if let server { self.server = server }
            self.secret = secret
            self.notice = notice
            noticeIsError = error
            self.step = step
        }
    #endif
}
