import Foundation
import RAProto
import Security

/// Light QR/deep-link query parser for UI prefill only; all cryptographic verification runs in Go `clientcore`.
public struct PairLink: Equatable, Sendable {
    public let code: String
    public let server: String
    public let commit: Data?

    public init(code: String, server: String, commit: Data? = nil) {
        self.code = code
        self.server = server
        self.commit = commit
    }

    public func url(web: String) -> String {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))
        let qServer = server.addingPercentEncoding(withAllowedCharacters: allowed) ?? server
        var parts = ["code=\(code)"]
        if let commit, !commit.isEmpty {
            parts.append("c=\(commit.base64URL)")
        }
        parts.append("s=\(qServer)")
        return web.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/#/pair?" + parts.joined(separator: "&")
    }

    public static func parse(_ raw: String) -> PairLink? {
        guard let idx = raw.range(of: "#/pair?")?.upperBound else { return nil }
        let query = String(raw[idx...])
        var code = ""
        var server = ""
        var commit: Data?
        for item in query.split(separator: "&") {
            let kv = item.split(separator: "=", maxSplits: 1)
            guard kv.count == 2 else { continue }
            let key = String(kv[0])
            guard let val = String(kv[1]).replacingOccurrences(of: "+", with: " ").removingPercentEncoding else { return nil }
            switch key {
            case "code": code = val
            case "s": server = val
            case "c":
                guard let decoded = Data(base64URL: val), decoded.count == keySize else { return nil }
                commit = decoded
            default: break
            }
        }
        guard code.count == 8, code.allSatisfy({ $0.isASCII && $0.isNumber }) else { return nil }
        if let commit, commit.count != keySize { return nil }
        guard let checked = try? Secret.checkServer(server) else { return nil }
        return PairLink(code: code, server: checked, commit: commit)
    }
}

public struct SecurityWorkflowError: Error, LocalizedError, Equatable, Sendable {
    public let message: String
    public let code: PBSecurityErrorCode
    public let retryable: Bool
    public let uncertain: Bool
    public let errorAction: String

    public init(
        message: String,
        code: PBSecurityErrorCode = .unspecified,
        retryable: Bool = true,
        uncertain: Bool = false,
        errorAction: String = ""
    ) {
        self.message = message
        self.code = code
        self.retryable = retryable
        self.uncertain = uncertain
        self.errorAction = errorAction
    }

    public var errorDescription: String? { message }
}

private let resolvableI18nPrefixes = ["security.", "errors.", "common.", "settings.", "connection.", "home."]

public func translateSecurityKey(_ raw: String) -> String {
    guard !raw.isEmpty, !raw.contains(" "),
          resolvableI18nPrefixes.contains(where: { raw.hasPrefix($0) }),
          raw.range(of: #"^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$"#, options: .regularExpression) != nil
    else { return raw }
    return L10nText.resolve(key: raw)
}

public func translateSecurityErrorMessage(_ res: PBSecurityWorkflowResultEffect) -> String {
    if res.hasErrorText {
        let rendered = renderCoreText(res.errorText)
        if !rendered.isEmpty { return rendered }
    }
    let raw = !res.error.isEmpty
        ? res.error
        : (res.structuredErrorCode != .unspecified ? String(describing: res.structuredErrorCode) : "security error")
    return translateSecurityKey(raw)
}

public func renderExitAdviceMessage(_ advice: PBSecurityExitAdvice) -> String {
    if advice.hasMessageText {
        let rendered = renderCoreText(advice.messageText)
        if !rendered.isEmpty { return rendered }
    }
    return translateSecurityKey(advice.message)
}

public struct PreparedCreationResult: Equatable, Sendable {
    public let recoveryCode: String
    public let resumedDraft: Bool
    public let codeSaved: Bool
}

public struct PreparedEmergencyRecoveryResult: Equatable, Sendable {
    public let recoveryCode: String
    public let recovery: PBSecurityRecoveryInfo?
}

/// Lock-protected one-shot bridge between a `CheckedContinuation` and async completion or task cancellation.
final class ContinuationWaiter<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Error>?
    private var earlyResult: Result<T, Error>?

    func install(_ cont: CheckedContinuation<T, Error>) -> Bool {
        let immediate: Result<T, Error>? = lock.withLock {
            if let early = earlyResult {
                earlyResult = nil
                return early
            }
            continuation = cont
            return nil
        }
        if let immediate {
            cont.resume(with: immediate)
            return false
        }
        return true
    }

    func resolve(_ result: Result<T, Error>) {
        let cont: CheckedContinuation<T, Error>? = lock.withLock {
            if let c = continuation {
                continuation = nil
                return c
            }
            if earlyResult == nil {
                earlyResult = result
            }
            return nil
        }
        cont?.resume(with: result)
    }
}

private func awaitWaiter<R: Sendable>(
    isClosed: @escaping @Sendable () -> Bool,
    closedError: @escaping @Sendable () -> Error,
    register: (ContinuationWaiter<R>) throws -> UInt64,
    unregister: @escaping @Sendable (UInt64) -> ContinuationWaiter<R>?,
    dispatch: (UInt64) -> Void
) async throws -> R {
    let waiter = ContinuationWaiter<R>()
    let id = try register(waiter)
    return try await withTaskCancellationHandler {
        try await withCheckedThrowingContinuation { continuation in
            guard waiter.install(continuation) else { return }
            if isClosed() {
                unregister(id)?.resolve(.failure(closedError()))
                return
            }
            dispatch(id)
            if isClosed() {
                unregister(id)?.resolve(.failure(closedError()))
            }
        }
    } onCancel: {
        unregister(id)?.resolve(.failure(CancellationError()))
    }
}

/// Async promise/continuation wrapper over `SecurityWorkflowActionEvent` / `SecurityWorkflowResultEffect`.
public final class SecurityClient: @unchecked Sendable {
    private let lock = NSLock()
    private let dispatchEvent: @Sendable (PBCoreEvent) -> Void
    private let setForegroundFn: @Sendable (Bool) -> Void
    private let isClosedFn: @Sendable () -> Bool
    private let onDismissUpgradeNotice: (@Sendable () -> Void)?
    private var nextActionID: UInt64 = 1
    private var waiters: [UInt64: ContinuationWaiter<PBSecurityWorkflowResultEffect>] = [:]
    private var statusBox: PBSecurityWorkflowStatus?
    private var statusListeners: [UUID: @Sendable (PBSecurityWorkflowStatus) -> Void] = [:]

    public init(
        dispatchEvent: @escaping @Sendable (PBCoreEvent) -> Void,
        setForegroundFn: @escaping @Sendable (Bool) -> Void,
        isClosedFn: @escaping @Sendable () -> Bool,
        onDismissUpgradeNotice: (@Sendable () -> Void)? = nil
    ) {
        self.dispatchEvent = dispatchEvent
        self.setForegroundFn = setForegroundFn
        self.isClosedFn = isClosedFn
        self.onDismissUpgradeNotice = onDismissUpgradeNotice
    }

    public var currentStatus: PBSecurityWorkflowStatus? {
        lock.withLock { statusBox }
    }

    @discardableResult
    public func observeStatus(_ listener: @escaping @Sendable (PBSecurityWorkflowStatus) -> Void) -> UUID {
        let id = UUID()
        let current = lock.withLock { () -> PBSecurityWorkflowStatus? in
            statusListeners[id] = listener
            return statusBox
        }
        if let current { listener(current) }
        return id
    }

    public func removeStatusObserver(_ id: UUID) {
        _ = lock.withLock { statusListeners.removeValue(forKey: id) }
    }

    public func handleWorkflowResult(_ res: PBSecurityWorkflowResultEffect) {
        let (waiter, listeners, newStatus) = lock.withLock { () -> (
            ContinuationWaiter<PBSecurityWorkflowResultEffect>?,
            [@Sendable (PBSecurityWorkflowStatus) -> Void],
            PBSecurityWorkflowStatus?
        ) in
            let st: PBSecurityWorkflowStatus? = res.hasStatus ? res.status : nil
            if let st { statusBox = st }
            let w = waiters.removeValue(forKey: res.actionID)
            return (w, st == nil ? [] : Array(statusListeners.values), st)
        }
        if let newStatus {
            for cb in listeners { cb(newStatus) }
        }
        guard let waiter else { return }
        if !res.ok {
            let msg = translateSecurityErrorMessage(res)
            waiter.resolve(.failure(SecurityWorkflowError(
                message: msg,
                code: res.structuredErrorCode,
                retryable: res.retryable,
                uncertain: res.uncertain,
                errorAction: res.errorAction
            )))
        } else {
            waiter.resolve(.success(res))
        }
    }

    public func handleStatusChanged(_ status: PBSecurityWorkflowStatus) {
        let listeners = lock.withLock { () -> [@Sendable (PBSecurityWorkflowStatus) -> Void] in
            statusBox = status
            return Array(statusListeners.values)
        }
        for cb in listeners { cb(status) }
    }

    public func rejectAll(reason: String = "security client closed") {
        let pending = lock.withLock { () -> [ContinuationWaiter<PBSecurityWorkflowResultEffect>] in
            let all = Array(waiters.values)
            waiters.removeAll()
            return all
        }
        let err = SecurityWorkflowError(message: reason, code: .canceled, retryable: false)
        for waiter in pending { waiter.resolve(.failure(err)) }
    }

    public func setForeground(_ active: Bool) {
        setForegroundFn(active)
    }

    private static let closedErr = SecurityWorkflowError(message: "security client closed", code: .canceled, retryable: false)

    private func run(_ build: (inout PBSecurityWorkflowActionEvent) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await awaitWaiter(
            isClosed: isClosedFn,
            closedError: { Self.closedErr },
            register: { waiter in
                try lock.withLock {
                    guard !isClosedFn() else { throw Self.closedErr }
                    try Task.checkCancellation()
                    let id = nextActionID
                    nextActionID &+= 1
                    waiters[id] = waiter
                    return id
                }
            },
            unregister: { [self] id in lock.withLock { waiters.removeValue(forKey: id) } },
            dispatch: { id in
                var action = PBSecurityWorkflowActionEvent()
                action.actionID = id
                build(&action)
                dispatchEvent(PBCoreEvent.with { $0.securityAction = action })
            }
        )
    }

    private func empty(_ set: (inout PBSecurityWorkflowActionEvent, PBSecurityActionEmpty) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await run { set(&$0, PBSecurityActionEmpty()) }
    }

    private func bool(_ flag: Bool, _ set: (inout PBSecurityWorkflowActionEvent, PBSecurityBoolAction) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await run { set(&$0, .with { $0.flag = flag }) }
    }

    private func str(_ value: String, _ set: (inout PBSecurityWorkflowActionEvent, PBSecurityStringAction) -> Void) async throws -> PBSecurityWorkflowResultEffect {
        try await run { set(&$0, .with { $0.value = value }) }
    }

    @discardableResult
    public func bootstrap() async throws -> PBSecurityWorkflowStatus {
        try await empty { $0.bootstrap = $1 }.status
    }

    public func getStatus() async throws -> PBSecurityWorkflowStatus {
        try await empty { $0.getStatus = $1 }.status
    }

    @discardableResult
    public func dismissUpgradeNotice() async throws -> PBSecurityWorkflowStatus {
        let res = try await empty { $0.dismissUpgradeNotice = $1 }
        onDismissUpgradeNotice?()
        return res.status
    }

    @discardableResult
    public func syncNow(touch: Bool = false, authToken: String = "") async throws -> PBSecurityWorkflowStatus {
        try await run { $0.syncNow = .with { $0.touch = touch; $0.authToken = authToken } }.status
    }

    @discardableResult
    public func renewLease(force: Bool = true) async throws -> PBSecurityWorkflowStatus {
        try await bool(force) { $0.renewLease = $1 }.status
    }

    @discardableResult
    public func resumePendingOp() async throws -> PBSecurityWorkflowResultEffect {
        try await empty { $0.resumePendingOp = $1 }
    }

    public func discardPendingOp(abandonHandoff: Bool = false) async throws {
        _ = try await bool(abandonHandoff) { $0.discardPendingOp = $1 }
    }

    public func prepareCreation(server: String, storagePersisted: Bool = true) async throws -> PreparedCreationResult {
        let res = try await run { $0.prepareCreation = .with { $0.server = server; $0.storagePersisted = storagePersisted } }
        return PreparedCreationResult(recoveryCode: res.recoveryCode, resumedDraft: res.resumedDraft, codeSaved: res.codeSaved)
    }

    public func confirmCreationCodeSaved() async throws { _ = try await empty { $0.confirmCreationCodeSaved = $1 } }

    @discardableResult
    public func continueCreation(setupToken: String = "") async throws -> PBSecurityWorkflowResultEffect {
        try await str(setupToken) { $0.continueCreation = $1 }
    }

    @discardableResult
    public func adoptCreationWithoutCode() async throws -> PBSecurityWorkflowResultEffect {
        try await empty { $0.adoptCreationWithoutCode = $1 }
    }

    public func removeDevice(_ deviceId: String, abandonPairDelivery: Bool = false) async throws {
        _ = try await run { $0.removeDevice = .with { $0.deviceID = deviceId; $0.abandonPairDelivery = abandonPairDelivery } }
    }

    public func deleteMachineRecords(_ machineId: String) async throws { _ = try await str(machineId) { $0.deleteMachineRecords = $1 } }

    public func deleteAccount() async throws { _ = try await empty { $0.deleteAccount = $1 } }

    public func prepareRecoveryRotation() async throws -> String {
        try await empty { $0.prepareRecoveryRotation = $1 }.recoveryCode
    }

    public func confirmRecoveryRotation() async throws { _ = try await empty { $0.confirmRecoveryRotation = $1 } }

    public func prepareHandoffCode(storagePersisted: Bool = true) async throws -> String {
        try await bool(storagePersisted) { $0.prepareHandoffCode = $1 }.recoveryCode
    }

    public func publishHandoffOffer() async throws { _ = try await empty { $0.publishHandoffOffer = $1 } }

    public func listHandoffCandidates() async throws -> [PBSecurityHandoffCandidate] {
        try await empty { $0.listHandoffCandidates = $1 }.handoffCandidates
    }

    public func handoffAuthority(targetDeviceId: String) async throws {
        _ = try await str(targetDeviceId) { $0.handoffAuthority = $1 }
    }

    public func completeAccountPublicProfile(server: String, code: String) async throws {
        _ = try await run { $0.completeAccountPublicProfile = .with { $0.server = server; $0.code = code } }
    }

    public func startPairing(webOrigin: String, replacesDeviceId: String = "") async throws -> PBSecurityPairingInfo {
        try await run { $0.startPairing = .with { $0.webOrigin = webOrigin; $0.replacesDeviceID = replacesDeviceId } }.pairing
    }

    public func pollPairing(pairingId: String) async throws -> PBSecurityPairingInfo {
        try await str(pairingId) { $0.pollPairing = $1 }.pairing
    }

    public func approvePairing(pairingId: String, sasConfirmed: Bool) async throws -> PBSecurityPairingInfo {
        try await run { $0.approvePairing = .with { $0.pairingID = pairingId; $0.sasConfirmed = sasConfirmed } }.pairing
    }

    public func finishPairingDelivery(pairingId: String) async throws -> PBSecurityPairingInfo {
        try await str(pairingId) { $0.finishPairingDelivery = $1 }.pairing
    }

    public func cancelPairing(pairingId: String) async throws { _ = try await str(pairingId) { $0.cancelPairing = $1 } }

    public func startJoinPairing(
        server: String = "",
        code: String = "",
        commitHint: Data? = nil,
        existingRepair: Bool = false,
        existingServer: String = "",
        rawLink: String = ""
    ) async throws -> PBSecurityPairingInfo {
        try await run {
            $0.startJoinPairing = .with {
                $0.server = server; $0.code = code
                if let commitHint, !commitHint.isEmpty { $0.commitHint = commitHint }
                $0.existingRepair = existingRepair; $0.existingServer = existingServer; $0.rawLink = rawLink
            }
        }.pairing
    }

    @discardableResult
    public func confirmJoinSas(pairingId: String) async throws -> PBSecurityWorkflowResultEffect {
        try await str(pairingId) { $0.confirmJoinSas = $1 }
    }

    public func cancelJoinPairing(pairingId: String) async throws { _ = try await str(pairingId) { $0.cancelJoinPairing = $1 } }

    public func prepareEmergencyRecovery(server: String, code: String) async throws -> PreparedEmergencyRecoveryResult {
        let res = try await run { $0.prepareEmergencyRecovery = .with { $0.server = server; $0.code = code } }
        return PreparedEmergencyRecoveryResult(recoveryCode: res.recoveryCode, recovery: res.hasRecovery ? res.recovery : nil)
    }

    public func confirmRecoveryCodeSaved() async throws { _ = try await empty { $0.confirmRecoveryCodeSaved = $1 } }

    public func continueRecoverySigning(oldCode: String) async throws -> PBSecurityRecoveryInfo? {
        let res = try await str(oldCode) { $0.continueRecoverySigning = $1 }
        return res.hasRecovery ? res.recovery : nil
    }

    public func resumeRecovery() async throws -> PBSecurityRecoveryInfo? {
        let res = try await empty { $0.resumeRecovery = $1 }
        return res.hasRecovery ? res.recovery : nil
    }

    @discardableResult
    public func completeRecovery() async throws -> PBSecurityWorkflowResultEffect {
        try await empty { $0.completeRecovery = $1 }
    }

    public func discardUnsignedRecovery() async throws { _ = try await empty { $0.discardUnsignedRecovery = $1 } }

    public func discardRejectedRecovery() async throws { _ = try await empty { $0.discardRejectedRecovery = $1 } }

    public func checkExitAdvice() async throws -> PBSecurityExitAdvice {
        try await empty { $0.checkExitAdvice = $1 }.exitAdvice
    }

    public func clearSecurity() async throws { _ = try await empty { $0.clearSecurity_p = $1 } }

    public func checkRetainedDenied() async throws -> Bool {
        try await empty { $0.checkRetainedDenied = $1 }.flag
    }

    public func updateDeviceMeta(
        deviceId: String = "",
        name: String? = nil,
        platform: String? = nil,
        description: String? = nil,
        describeSelfOnly: Bool = false
    ) async throws {
        _ = try await run {
            $0.updateDeviceMeta = .with {
                $0.deviceID = deviceId
                if let name { $0.name = name }
                if let platform { $0.platform = platform }
                if let description { $0.description_p = description }
                $0.describeSelfOnly = describeSelfOnly
            }
        }
    }

    public func republishRecoveryObjects(flag: Bool = false) async throws { _ = try await bool(flag) { $0.republishRecoveryObjects = $1 } }
}

/// Shared Go `CoreClient` + `SecurityExecutor` event-loop driver used by both
/// `StandaloneSecurityHost` (pre-auth / onboarding) and headless `Remote` tests.
final class CoreSecurityDriver: @unchecked Sendable {
    private let core: CoreClient
    private let executor: SecurityExecutor
    private let client: SecurityClient
    private let clock: @Sendable () -> Int64
    private let sleep: Sleeper
    private let onStatusSnapshot: (@Sendable (PBSecurityWorkflowStatus) -> Void)?
    private let onTokenUpdated: (@Sendable (PBSecurityTokenUpdatedEffect) -> Void)?
    private let queue = DispatchQueue(label: "ra.core.security.driver")
    private let lock = NSLock()
    private var closed = false
    private var nextOpID: UInt64 = 1
    private var timers: [String: (id: UInt64, task: Task<Void, Never>)] = [:]
    private var securityOpWaiters: [UInt64: ContinuationWaiter<PBSecurityOpResultEffect>] = [:]

    init(
        executor: SecurityExecutor,
        client: SecurityClient,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        onStatusSnapshot: (@Sendable (PBSecurityWorkflowStatus) -> Void)? = nil,
        onTokenUpdated: (@Sendable (PBSecurityTokenUpdatedEffect) -> Void)? = nil,
        initialConfig: PBSecurityConfigEvent? = nil
    ) throws {
        self.core = try CoreClient()
        self.executor = executor
        self.client = client
        self.clock = clock
        self.sleep = sleep
        self.onStatusSnapshot = onStatusSnapshot
        self.onTokenUpdated = onTokenUpdated
        enqueueEvent(PBCoreEvent.with {
            $0.entropyProvided = Self.buildEntropy(minIds: 16, minFloats: 16, minRandomBytes: 4096)
        })
        if let initialConfig {
            enqueueEvent(PBCoreEvent.with { $0.securityConfig = initialConfig })
        }
    }

    private func isClosed() -> Bool { lock.withLock { closed } }

    func enqueueEvent(_ ev: PBCoreEvent) {
        guard !isClosed() else { return }
        var stamped = ev
        if stamped.nowMs == 0 { stamped.nowMs = clock() }
        if stamped.monoMs == 0 { stamped.monoMs = stamped.nowMs }
        let finalEvent = stamped
        queue.async { [weak self] in
            self?.dispatchOnWorker(finalEvent)
        }
    }

    func runSecurityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        try await awaitWaiter(
            isClosed: { self.isClosed() },
            closedError: { SecurityFailure.storage },
            register: { waiter in
                try lock.withLock {
                    guard !closed else { throw SecurityFailure.storage }
                    try Task.checkCancellation()
                    let id = nextOpID
                    nextOpID &+= 1
                    securityOpWaiters[id] = waiter
                    return id
                }
            },
            unregister: { [self] id in lock.withLock { securityOpWaiters.removeValue(forKey: id) } },
            dispatch: { id in
                var req = PBSecurityOpRequestEvent()
                req.opID = id
                configure(&req)
                enqueueEvent(PBCoreEvent.with { $0.securityOpRequest = req })
            }
        )
    }

    func syncStateView() -> PBCoreSyncStateView? {
        guard !isClosed() else { return nil }
        return queue.sync {
            guard !isClosed() else { return nil }
            return try? core.view(PBCoreScreen.with {
                $0.syncState = PBCoreSyncStateScreenRequest()
            }).syncState
        }
    }

    private func dispatchOnWorker(_ initial: PBCoreEvent) {
        var queueEvents = [initial]
        var idx = 0
        while idx < queueEvents.count && !isClosed() {
            let ev = queueEvents[idx]
            idx += 1
            guard let batch = try? core.dispatch(ev) else { continue }
            for eff in batch.effects {
                if isClosed() { return }
                handleEffect(eff, inlineQueue: &queueEvents)
            }
        }
    }

    private func handleEffect(_ eff: PBCoreEffect, inlineQueue: inout [PBCoreEvent]) {
        switch eff.effect {
        case .requestEntropy(let req):
            let ids = max(Int(req.minIds), 16)
            let floats = max(Int(req.minFloats), 16)
            let rbytes = req.minRandomBytes > 0 ? max(Int(req.minRandomBytes), 4096) : 0
            inlineQueue.append(PBCoreEvent.with {
                $0.nowMs = clock()
                $0.monoMs = clock()
                $0.entropyProvided = Self.buildEntropy(minIds: ids, minFloats: floats, minRandomBytes: rbytes)
            })
        case .scheduleTimer(let st):
            let tk = "\(st.kind.rawValue):\(st.key)"
            lock.withLock { timers.removeValue(forKey: tk)?.task.cancel() }
            let delayMs = max(0, st.delayMs)
            let sleep = self.sleep
            let task = Task { [weak self] in
                if delayMs > 0 { try? await sleep(delayMs) }
                guard let self, !Task.isCancelled, !self.isClosed() else { return }
                let matches = self.lock.withLock { () -> Bool in
                    guard let cur = self.timers[tk], cur.id == st.timerID else { return false }
                    self.timers.removeValue(forKey: tk)
                    return true
                }
                guard matches else { return }
                self.enqueueEvent(PBCoreEvent.with {
                    $0.timerFired = .with {
                        $0.timerID = st.timerID
                        $0.kind = st.kind
                        $0.key = st.key
                    }
                })
            }
            lock.withLock { timers[tk] = (st.timerID, task) }
        case .cancelTimer(let ct):
            let tk = "\(ct.kind.rawValue):\(ct.key)"
            let task = lock.withLock { () -> Task<Void, Never>? in
                guard let cur = timers[tk], ct.timerID == 0 || cur.id == ct.timerID else { return nil }
                return timers.removeValue(forKey: tk)?.task
            }
            task?.cancel()
        case .securityKeyGenerate, .securityKeySign, .securityKeyAgree,
             .securityKeyEncapsulate, .securityKeyDiscard, .securityStorage, .securityRpc:
            _ = executor.handleEffect(eff)
        case .securityWorkflowResult(let r):
            client.handleWorkflowResult(r)
            if r.hasStatus { onStatusSnapshot?(r.status) }
        case .securityStatusChanged(let s):
            if s.hasStatus {
                client.handleStatusChanged(s.status)
                onStatusSnapshot?(s.status)
            }
        case .securityTokenUpdated(let t):
            onTokenUpdated?(t)
        case .securityOpResult(let res):
            let waiter = lock.withLock { securityOpWaiters.removeValue(forKey: res.opID) }
            waiter?.resolve(.success(res))
        default: break
        }
    }

    func close() {
        let (pendingTimers, pendingOps) = lock.withLock { () -> ([Task<Void, Never>], [ContinuationWaiter<PBSecurityOpResultEffect>]) in
            guard !closed else { return ([], []) }
            closed = true
            let ts = timers.values.map(\.task)
            let ops = Array(securityOpWaiters.values)
            timers.removeAll()
            securityOpWaiters.removeAll()
            return (ts, ops)
        }
        for t in pendingTimers { t.cancel() }
        for waiter in pendingOps { waiter.resolve(.failure(SecurityFailure.storage)) }
        queue.sync {
            core.close()
        }
    }

    private static func buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int) -> PBCoreEntropyProvided {
        PBCoreEntropyProvided.with {
            $0.randomIds = (0..<minIds).map { _ in newRequestId() }
            $0.unitFloats = (0..<minFloats).map { _ in Double.random(in: 0..<1) }
            if minRandomBytes > 0 {
                var bytes = Data(count: minRandomBytes)
                _ = bytes.withUnsafeMutableBytes { buf in
                    guard let base = buf.baseAddress else { return errSecParam }
                    return SecRandomCopyBytes(kSecRandomDefault, minRandomBytes, base)
                }
                $0.randomBytes = bytes
            }
        }
    }
}

/// Standalone Go `clientcore` + `SecurityExecutor` host for unauthenticated onboarding/recovery
/// flows and unit tests when no `CoreHost` / `Remote` is attached.
public final class StandaloneSecurityHost: @unchecked Sendable {
    private final class Box: @unchecked Sendable {
        let lock = NSLock()
        var driver: CoreSecurityDriver?
        var closed = false
    }

    private let box = Box()
    private let executor: SecurityExecutor
    private let driver: CoreSecurityDriver
    public nonisolated let client: SecurityClient

    public init(
        store: any SecurityStoreV4,
        http: HTTP? = nil,
        allowPlainHttp: Bool = false,
        clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
        sleep: @escaping Sleeper = realSleep,
        rpcOverride: SecurityExecutor.RpcOverride? = nil
    ) throws {
        let box = self.box
        let executor = SecurityExecutor(
            store: store,
            http: http,
            allowPlainHttp: allowPlainHttp,
            rpcOverride: rpcOverride,
            emit: { ev in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(ev)
            }
        )
        let client = SecurityClient(
            dispatchEvent: { ev in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(ev)
            },
            setForegroundFn: { fg in
                let drv = box.lock.withLock { box.closed ? nil : box.driver }
                drv?.enqueueEvent(PBCoreEvent.with {
                    $0.securityForeground = .with { $0.foreground = fg }
                })
            },
            isClosedFn: { box.lock.withLock { box.closed } },
            onDismissUpgradeNotice: { store.dismissLegacyRepairNotice() }
        )
        let driver = try CoreSecurityDriver(
            executor: executor,
            client: client,
            clock: clock,
            sleep: sleep,
            initialConfig: .with {
                $0.inlineCrypto = true
                $0.foreground = true
            }
        )
        box.lock.withLock { box.driver = driver }
        self.executor = executor
        self.client = client
        self.driver = driver
    }

    public func setForeground(_ active: Bool) {
        client.setForeground(active)
    }

    public func securityOp(_ configure: @Sendable (inout PBSecurityOpRequestEvent) -> Void) async throws -> PBSecurityOpResultEffect {
        try await driver.runSecurityOp(configure)
    }

    public func syncStateView() -> PBCoreSyncStateView? {
        driver.syncStateView()
    }

    public func close() {
        box.lock.withLock {
            box.closed = true
            box.driver = nil
        }
        executor.close()
        client.rejectAll()
        driver.close()
    }
}
