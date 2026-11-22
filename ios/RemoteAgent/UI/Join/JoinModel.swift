// The join flow's state and rules, apart from how it is drawn: the
// counterpart of the state in Android's ui/Join.kt, powered by Go clientcore's
// `SecurityClient` for pre-auth onboarding and active re-pairing.
import Foundation
import Observation
import RACore
import RAProto
import UIKit
import os

/// How this device is called in the join flow's sentences.
@MainActor var thisDeviceWord: String {
    UIDevice.current.userInterfaceIdiom == .pad ? L10n.securityIosThisIpad() : L10n.securityIosThisPhone()
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
    var resumePending: (@Sendable () async throws -> Account?)? = nil
    var hasPendingRegistration: (@Sendable () async -> Bool)? = nil

    private static func adoptedAccount(
        from res: PBSecurityWorkflowResultEffect, fallbackServer: String, existing: Account?
    ) -> Account {
        let st = res.status
        let adopted = res.accountAdopted
        let server = !adopted.server.isEmpty ? adopted.server : (!st.server.isEmpty ? st.server : (existing?.server ?? fallbackServer))
        let deviceId = !adopted.deviceID.isEmpty ? adopted.deviceID : (!st.deviceID.isEmpty ? st.deviceID : (existing?.deviceId ?? ""))
        let accountId = !adopted.accountID.isEmpty ? adopted.accountID : (!st.accountID.isEmpty ? st.accountID : (existing?.accountId ?? ""))
        return Account(server: server, deviceId: deviceId, accountId: accountId)
    }

    /// Pre-auth onboarding via a temporary `StandaloneSecurityHost` (closed before returning `Account`),
    /// OR active re-pairing via an existing `Remote.securityClient`.
    @MainActor
    static func live(
        http: HTTP = AppModel.http,
        client: SecurityClient? = nil,
        existing: Account? = nil,
        vault: Vault = Vault(),
        allowPlainHttp: Bool = false
    ) -> JoinCalls {
        @Sendable func withClient<T: Sendable>(
            _ body: @Sendable (SecurityClient) async throws -> T
        ) async throws -> T {
            let ownHost: StandaloneSecurityHost?
            let secClient: SecurityClient
            if let client {
                ownHost = nil
                secClient = client
            } else {
                let host = try StandaloneSecurityHost(store: vault, http: http, allowPlainHttp: allowPlainHttp)
                ownHost = host
                secClient = host.client
                _ = try? await secClient.bootstrap()
            }
            defer { ownHost?.close() }
            return try await body(secClient)
        }

        return JoinCalls(
            byPairing: { link, onEmoji, stopped, onRegistering, confirm in
                try await withClient { secClient in
                    let info = try await secClient.startJoinPairing(
                        server: link.server,
                        code: link.code,
                        commitHint: link.commit,
                        existingRepair: existing != nil,
                        existingServer: existing?.server ?? ""
                    )
                    if stopped() {
                        try? await secClient.cancelJoinPairing(pairingId: info.pairingID)
                        throw SecurityWorkflowError(
                            message: L10n.errorsIosJoiningCanceledTheOldRequestMayStillOccupyThe(),
                            code: "canceled",
                            retryable: false
                        )
                    }

                    onEmoji(info.sasEmoji)
                    let accepted = await confirm(info.sasEmoji)
                    if !accepted || stopped() {
                        try? await secClient.cancelJoinPairing(pairingId: info.pairingID)
                        throw SecurityWorkflowError(
                            message: L10n.errorsIosJoiningCanceledTheOldRequestMayStillOccupyThe(),
                            code: "canceled",
                            retryable: false
                        )
                    }

                    onRegistering()
                    let res = try await secClient.confirmJoinSas(pairingId: info.pairingID)
                    return Self.adoptedAccount(from: res, fallbackServer: link.server, existing: existing)
                }
            },
            resumePending: {
                try await withClient { secClient in
                    let st = (try? await secClient.getStatus()) ?? secClient.currentStatus
                    guard st?.bootstrap == .pendingRegistration else { return nil }
                    let res = try await secClient.resumePendingOp()
                    return Self.adoptedAccount(from: res, fallbackServer: st?.server ?? "", existing: existing)
                }
            },
            hasPendingRegistration: {
                (try? await withClient { secClient in
                    let st = (try? await secClient.getStatus()) ?? secClient.currentStatus
                    return st?.bootstrap == .pendingRegistration
                }) ?? false
            }
        )
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
    /// Legacy secret box retained only as empty property if referenced; unused.
    var secret = ""
    /// What went wrong or what happened, over the form; empty for nothing.
    private(set) var notice = ""
    /// The notice is a failure, not an account of what the user did.
    private(set) var noticeIsError = false
    private(set) var joiningWithSecret = false

    /// The other device's commitment, from a scanned code: the server's answer is checked against it.
    @ObservationIgnored private var commit: Data?
    @ObservationIgnored private var attempt: JoinAttempt?
    @ObservationIgnored private let calls: JoinCalls
    @ObservationIgnored private let onJoined: @MainActor (Account) async throws -> Void

    let retainedIdentity: Bool
    private(set) var pendingRegistration: Bool
    init(
        lastServer: String, calls: JoinCalls, retainedIdentity: Bool = false, pendingRegistration: Bool = false,
        onJoined: @escaping @MainActor (Account) async throws -> Void
    ) {
        server = lastServer.isEmpty ? "https://" : lastServer
        self.retainedIdentity = retainedIdentity
        self.pendingRegistration = pendingRegistration
        self.calls = calls
        self.onJoined = onJoined
    }

    /// The code as it is shown: two groups of four.
    var shownCode: String {
        code.count > 4 ? code.prefix(4) + " " + code.dropFirst(4) : code
    }

    var canJoin: Bool { pendingRegistration || (code.count == 8 && server.count > 8) }
    /// Working can still be left: a pairing that has not begun to register.
    var canCancel: Bool { !joiningWithSecret && attempt?.registering != true }

    /// What was typed or pasted into the code field: its digits are kept.
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

    func showScanner() {
        notice = ""
        step = .scan
    }

    // MARK: Joining with a code

    /// 加入 on the form: checks the address, then joins with the code typed or scanned,
    /// or resumes a persisted `PENDING_REGISTRATION` without calling `startJoinPairing`.
    func joinWithCode() {
        if pendingRegistration {
            resumeRegistration()
            return
        }
        do {
            join(PairLink(code: code, server: try Secret.checkServer(server), commit: commit))
        } catch {
            fail(errorText(error))
        }
    }

    private func resumeRegistration() {
        notice = ""
        step = .working
        let attempt = JoinAttempt()
        attempt.beginRegistering()
        self.attempt = attempt
        let calls = calls
        Task {
            do {
                if let account = try await calls.resumePending?() {
                    pendingRegistration = false
                    await finished(attempt, account)
                    return
                }
                pendingRegistration = false
                if code.count == 8 {
                    let checked = try Secret.checkServer(server)
                    self.join(PairLink(code: code, server: checked, commit: commit))
                } else {
                    self.attempt = nil
                    self.step = .form
                }
            } catch {
                await failed(attempt, error)
            }
        }
    }

    /// What the camera read. Anything but a pairing link of this app is said
    /// and left.
    func scanned(_ text: String) {
        guard step == .scan else { return }
        guard let link = PairLink.parse(text) else {
            notice = L10n.securityIosThisIsNotARemoteAgentPairingQrCode()
            noticeIsError = true
            step = .form
            return
        }
        typeCode(link.code)
        server = link.server
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
                if let account = try await calls.resumePending?() {
                    pendingRegistration = false
                    await finished(attempt, account)
                    return
                }
                let account = try await calls.byPairing(
                    link,
                    { emoji in Task { @MainActor in self.show(emoji, of: attempt) } },
                    { attempt.stopped },
                    { attempt.beginRegistering() },
                    { emoji in await self.requestComparison(emoji, of: attempt) })
                pendingRegistration = false
                await finished(attempt, account)
            } catch {
                await failed(attempt, error)
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
        if self.attempt !== attempt { _ = self.attempt?.stop() }
        self.attempt = nil
        comparisonPending = false
        await hand(over: account)
    }

    private func failed(_ attempt: JoinAttempt, _ error: Error) async {
        comparisonPending = false
        let current = self.attempt === attempt
        if attempt.registering {
            if let checkPending = calls.hasPendingRegistration {
                pendingRegistration = await checkPending()
            }
            if current { self.attempt = nil }
            notice = L10n.securityIosUnsureWhetherJoinedCheckTheClientListInThe(
                detail: String(describing: errorText(error)),
                value: String(describing: thisDeviceWord)
            )
            noticeIsError = true
            if current { step = .form }
            return
        }
        guard current, !attempt.stopped else { return }
        self.attempt = nil
        fail(errorText(error))
    }

    /// Stops waiting, from 取消 or 图案不一致.
    func cancel() {
        guard let attempt else {
            if !joiningWithSecret { step = .form }
            return
        }
        guard attempt.stop() else {
            notice = L10n.errorsIosAlreadyRegisteringWaitForTheResult(value: String(describing: thisDeviceWord))
            noticeIsError = false
            return
        }
        self.attempt = nil
        typeCode("")
        notice = L10n.errorsIosJoiningCanceledTheOldRequestMayStillOccupyThe()
        noticeIsError = false
        step = .form
    }

    func joinWithSecret() {
        secret = ""
        step = .form
        notice = L10n.securityIosJoiningAnExistingAccountRequiresManagingClientApprovalUse()
        noticeIsError = true
    }

    // MARK: The end

    private func hand(over account: Account) async {
        secret = ""
        do {
            try await onJoined(account)
        } catch {
            notice = L10n.securityIosJoinedButCouldNotSaveAccountKeys(value: String(describing: errorText(error)))
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
