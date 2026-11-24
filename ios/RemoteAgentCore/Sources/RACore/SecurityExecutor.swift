import Connect
import Foundation
import RAProto
import SwiftProtobuf

private final class RpcCallState: @unchecked Sendable {
    private let waiter = ContinuationWaiter<HTTPResponse>()
    private let lock = NSLock()
    private var cancelable: Cancelable?
    private var cancelled = false

    func install(_ cont: CheckedContinuation<HTTPResponse, Error>) -> Bool {
        waiter.install(cont)
    }

    func attach(_ cancelable: Cancelable) {
        let cancelNow = lock.withLock { () -> Bool in
            if cancelled { return true }
            self.cancelable = cancelable
            return false
        }
        if cancelNow { cancelable.cancel() }
    }

    func complete(_ response: HTTPResponse) {
        lock.withLock { cancelable = nil }
        waiter.resolve(.success(response))
    }

    func cancel() {
        let cal = lock.withLock { () -> Cancelable? in
            cancelled = true
            let cal = cancelable
            cancelable = nil
            return cal
        }
        cal?.cancel()
        waiter.resolve(.failure(CancellationError()))
    }
}

/// Executes host-side key generation/agreement/signing, v4 storage transactions, and unary Connect
/// RPC calls requested by Go `clientcore/security`.
public final class SecurityExecutor: @unchecked Sendable {
    public typealias RpcOverride = @Sendable (PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent

    public let store: any SecurityStoreV4
    private let http: HTTP?
    private let emit: @Sendable (PBCoreEvent) async -> Void
    private let rpcOverride: RpcOverride?
    private let allowPlainHttp: Bool
    private let queue = DispatchQueue(label: "ra.security.executor", attributes: .concurrent)
    private let lock = NSLock()
    private var closed = false
    private var inFlightTasks: [UUID: Task<Void, Never>] = [:]

    public init(
        store: any SecurityStoreV4,
        http: HTTP? = nil,
        allowPlainHttp: Bool = false,
        rpcOverride: RpcOverride? = nil,
        emit: @escaping @Sendable (PBCoreEvent) async -> Void
    ) {
        self.store = store
        self.http = http
        self.allowPlainHttp = allowPlainHttp
        self.rpcOverride = rpcOverride
        self.emit = emit
    }

    public func close() {
        let tasks = lock.withLock { () -> [Task<Void, Never>] in
            guard !closed else { return [] }
            closed = true
            let list = Array(inFlightTasks.values)
            inFlightTasks.removeAll()
            return list
        }
        for t in tasks { t.cancel() }
    }

    public func handleEffect(_ eff: PBCoreEffect) -> Bool {
        switch eff.effect {
        case .securityKeyGenerate(let k): keyGenerate(k)
        case .securityKeySign(let s): keySign(s)
        case .securityKeyAgree(let a): keyAgree(a)
        case .securityKeyEncapsulate(let e): keyEncapsulate(e)
        case .securityKeyDiscard(let d): keyDiscard(d)
        case .securityStorage(let s): storage(s)
        case .securityRpc(let r): rpc(r)
        default: return false
        }
        return true
    }

    private func enqueueOp(_ work: @escaping @Sendable () async -> PBCoreEvent) {
        enqueueIO { [self] in await sendEvent(await work()) }
    }

    public func keyGenerate(_ eff: PBSecurityKeyGenerateEffect) {
        enqueueOp { [self] in
            var ev = PBSecurityKeyGeneratedEvent(); ev.opID = eff.opID
            do { ev.publicKey = try store.generateKey(kind: eff.keyKind, slot: eff.slot, persist: eff.persist) }
            catch { ev.error = error.localizedDescription }
            return PBCoreEvent.with { $0.securityKeyGenerated = ev }
        }
    }

    public func keySign(_ eff: PBSecurityKeySignEffect) {
        enqueueOp { [self] in
            var ev = PBSecurityKeySignedEvent(); ev.opID = eff.opID
            if eff.message.isEmpty { ev.error = "empty message to sign" }
            else {
                do { ev.signature = try store.signKey(kind: eff.keyKind == .unspecified ? .deviceEd : eff.keyKind, slot: eff.slot, message: eff.message) }
                catch { ev.error = error.localizedDescription }
            }
            return PBCoreEvent.with { $0.securityKeySigned = ev }
        }
    }

    public func keyAgree(_ eff: PBSecurityKeyAgreeEffect) {
        enqueueOp { [self] in
            var ev = PBSecurityKeyAgreedEvent(); ev.opID = eff.opID
            do { ev.shared = try store.agreeKey(kind: eff.keyKind == .unspecified ? .deviceX : eff.keyKind, slot: eff.slot, peerPublic: eff.peerPublic) }
            catch { ev.error = error.localizedDescription }
            return PBCoreEvent.with { $0.securityKeyAgreed = ev }
        }
    }

    public func keyEncapsulate(_ eff: PBSecurityKeyEncapsulateEffect) {
        enqueueOp { [self] in
            var ev = PBSecurityKeyEncapsulatedEvent(); ev.opID = eff.opID
            do { ev.pairs = try store.encapsulate(recipients: eff.recipients) }
            catch { ev.error = error.localizedDescription }
            return PBCoreEvent.with { $0.securityKeyEncapsulated = ev }
        }
    }

    public func keyDiscard(_ eff: PBSecurityKeyDiscardEffect) {
        enqueueIO { [self] in store.discardKey(kind: eff.keyKind, slot: eff.slot) }
    }

    public func storage(_ eff: PBSecurityStorageEffect) {
        enqueueOp { [self] in PBCoreEvent.with { $0.securityStorageResult = store.executeStorage(eff) } }
    }

    public func rpc(_ eff: PBSecurityRpcEffect) {
        spawnAsync { [self] in
            let res = if let rpcOverride { await rpcOverride(eff) } else { await executeUnaryRpc(eff) }
            await sendEvent(PBCoreEvent.with { $0.securityRpcResult = res })
        }
    }

    private func executeUnaryRpc(_ eff: PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent {
        var out = PBSecurityRpcResultEvent()
        out.opID = eff.opID
        guard let path = Self.rpcPaths[eff.method] else {
            out.connectCode = Int32(Code.unimplemented.rawValue)
            out.error = "unknown rpc method: \(eff.method.rawValue)"
            return out
        }
        guard let http else {
            out.connectCode = Int32(Code.unavailable.rawValue)
            out.error = "http unavailable"
            out.networkError = true
            return out
        }
        do {
            let base = try Self.checkedServerURL(eff.server, allowPlainHttp: allowPlainHttp)
            guard let url = URL(string: base + path) else {
                out.connectCode = Int32(Code.invalidArgument.rawValue)
                out.error = "invalid server url"
                return out
            }
            var headers: Headers = [
                "Content-Type": ["application/proto"],
                "Connect-Protocol-Version": ["1"],
            ]
            if !eff.authToken.isEmpty {
                headers["Authorization"] = ["Bearer \(eff.authToken)"]
            }
            let finalHeaders = headers
            let timeoutMs: Int64 = eff.method == .getSecurityStates ? 5_000 : 15_000
            let call = RpcCallState()
            let response: HTTPResponse = try await withTimeout(timeoutMs, sleep: realSleep) {
                try await withTaskCancellationHandler {
                    try await withCheckedThrowingContinuation { continuation in
                        guard call.install(continuation) else { return }
                        let req = HTTPRequest<Data?>(
                            url: url,
                            headers: finalHeaders,
                            message: eff.requestBytes,
                            method: .post,
                            trailers: [:],
                            idempotencyLevel: .unknown
                        )
                        let cancelable = http.unary(request: req, onMetrics: { _ in }) { resp in
                            call.complete(resp)
                        }
                        call.attach(cancelable)
                    }
                } onCancel: {
                    call.cancel()
                }
            }
            if let err = response.error {
                let ce = err as? ConnectError
                out.connectCode = Int32(response.code.rawValue)
                out.error = ce?.message ?? err.localizedDescription
                out.networkError = ce.map { networkFailure($0.exception) != nil }
                    ?? (response.code == .unavailable || response.code == .deadlineExceeded || response.code == .unknown)
                return out
            }
            if response.code != .ok {
                let parsed = Self.parseConnectErrorBody(response.message, fallbackCode: response.code)
                out.connectCode = Int32(parsed.code.rawValue)
                out.error = parsed.message
                return out
            }
            out.responseBytes = response.message ?? Data()
            return out
        } catch is TimedOut {
            out.connectCode = Int32(Code.deadlineExceeded.rawValue)
            out.error = "connection timed out"
            out.networkError = true
            return out
        } catch let ce as ConnectError {
            out.connectCode = Int32(ce.code.rawValue)
            out.error = ce.message ?? ce.localizedDescription
            out.networkError = networkFailure(ce.exception) != nil
            return out
        } catch {
            out.connectCode = Int32(Code.unavailable.rawValue)
            out.error = error.localizedDescription
            out.networkError = true
            return out
        }
    }

    private func enqueueIO(_ work: @escaping @Sendable () async -> Void) {
        spawnAsync(work)
    }

    private func spawnAsync(_ work: @escaping @Sendable () async -> Void) {
        let isClosed = lock.withLock { closed }
        if isClosed { return }
        let id = UUID()
        let task = Task { [weak self] in
            await work()
            _ = self?.lock.withLock { self?.inFlightTasks.removeValue(forKey: id) }
        }
        lock.withLock {
            if closed {
                task.cancel()
            } else {
                inFlightTasks[id] = task
            }
        }
    }

    private func sendEvent(_ ev: PBCoreEvent) async {
        let isClosed = lock.withLock { closed }
        guard !isClosed else { return }
        await emit(ev)
    }

    static func checkedServerURL(_ raw: String, allowPlainHttp: Bool) throws -> String {
        do {
            return try Secret.checkServer(raw)
        } catch {
            if allowPlainHttp, let u = URLComponents(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
               u.scheme?.lowercased() == "http", let host = u.host?.lowercased(), !host.isEmpty,
               u.user == nil, u.password == nil
            {
                let shown = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
                return "http://\(shown)" + (u.port.map { ":\($0)" } ?? "")
            }
            throw error
        }
    }

    private static let rpcPaths: [PBSecurityRpcMethod: String] = [
        .getSecurityStates: "/remoteagent.v1.ClientService/GetSecurityStates",
        .putSecurityState: "/remoteagent.v1.ClientService/PutSecurityState",
        .pinSecurityDomain: "/remoteagent.v1.ClientService/PinSecurityDomain",
        .createPairing: "/remoteagent.v1.PairingService/CreatePairing",
        .peekPairing: "/remoteagent.v1.PairingService/PeekPairing",
        .joinPairing: "/remoteagent.v1.PairingService/JoinPairing",
        .revealPairing: "/remoteagent.v1.PairingService/RevealPairing",
        .approvePairing: "/remoteagent.v1.PairingService/ApprovePairing",
        .getPairing: "/remoteagent.v1.PairingService/GetPairing",
        .cancelPairing: "/remoteagent.v1.PairingService/CancelPairing",
        .getRecoveryChallenge: "/remoteagent.v1.RecoveryService/GetRecoveryChallenge",
        .getRecoveryContext: "/remoteagent.v1.RecoveryService/GetRecoveryContext",
        .submitRecovery: "/remoteagent.v1.RecoveryService/SubmitRecovery",
        .completeRecovery: "/remoteagent.v1.RecoveryService/CompleteRecovery",
        .deleteMachineRecords: "/remoteagent.v1.ClientService/DeleteMachineRecords",
        .deleteAccount: "/remoteagent.v1.ClientService/DeleteAccount",
        .getChallenge: "/remoteagent.v1.AuthService/GetChallenge",
        .login: "/remoteagent.v1.AuthService/Login",
        .getSignupChallenge: "/remoteagent.v1.AuthService/GetSignupChallenge",
        .registerDevice: "/remoteagent.v1.AuthService/RegisterDevice",
        .putDeviceMeta: "/remoteagent.v1.ClientService/PutDeviceMeta",
        .listDevices: "/remoteagent.v1.ClientService/ListDevices",
        .listMachines: "/remoteagent.v1.ClientService/ListMachines",
        .getAccountPrefs: "/remoteagent.v1.ClientService/GetAccountPrefs",
        .putAccountPrefs: "/remoteagent.v1.ClientService/PutAccountPrefs",
    ]

    private static let connectCodes: [String: Code] = [
        "canceled": .canceled, "unknown": .unknown, "invalid_argument": .invalidArgument,
        "deadline_exceeded": .deadlineExceeded, "not_found": .notFound, "already_exists": .alreadyExists,
        "permission_denied": .permissionDenied, "resource_exhausted": .resourceExhausted,
        "failed_precondition": .failedPrecondition, "aborted": .aborted, "out_of_range": .outOfRange,
        "unimplemented": .unimplemented, "internal": .internalError, "unavailable": .unavailable,
        "data_loss": .dataLoss, "unauthenticated": .unauthenticated,
    ]

    private static func parseConnectErrorBody(_ data: Data?, fallbackCode: Code) -> (code: Code, message: String) {
        let fallback = "HTTP error (\(fallbackCode.name))"
        guard let data, !data.isEmpty,
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else {
            return (fallbackCode, fallback)
        }
        let codeStr = (json["code"] as? String)?.lowercased()
        let mapped = codeStr.flatMap { connectCodes[$0] } ?? fallbackCode
        let msg = (json["message"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? fallback
        return (mapped, msg)
    }
}

/// Decodes, verifies, and projects a push notification `PBEnvelope` using Go `clientcore` (`notify_blob`)
/// in a short-lived sandboxed `CoreClient` without needing unlocked device private keys.
/// Fail-closed: returns `nil` whenever `expectedKind` is unspecified or Core marks `display.valid == false`.
public func openNotifyDisplayViaCore(
    notifyBlob: Data,
    envelope: PBEnvelope,
    expectedKind: PBNotifyKind,
    nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000),
    machineId: String = "",
    sessionId: String = "",
    tag: String = "",
    eventKey: String = "",
    fallbackBody: String? = nil
) throws -> PBSecurityNotifyDisplay? {
    guard expectedKind != .unspecified else { return nil }
    let core = try CoreClient()
    defer { core.close() }
    let batch = try core.dispatch(PBCoreEvent.with {
        $0.nowMs = nowMs
        $0.monoMs = nowMs
        $0.securityConfig = .with { $0.inlineCrypto = true }
        $0.securityOpenNotify = .with {
            $0.opID = 1
            $0.notifyBlob = notifyBlob
            $0.envelope = envelope
            $0.nowMs = nowMs
            $0.expectedKind = expectedKind
            $0.machineID = machineId
            $0.sessionID = sessionId
            $0.tag = tag
            $0.eventKey = eventKey
            if let fallbackBody {
                $0.fallbackBody = fallbackBody
            }
        }
    })
    for eff in batch.effects {
        if case .securityNotifyOpened(let res)? = eff.effect, res.opID == 1 {
            if !res.error.isEmpty {
                throw SecurityWorkflowError(message: res.error, code: .invalid, retryable: false, uncertain: false, errorAction: "")
            }
            guard res.hasDisplay, res.display.valid else { return nil }
            return res.display
        }
    }
    return nil
}
