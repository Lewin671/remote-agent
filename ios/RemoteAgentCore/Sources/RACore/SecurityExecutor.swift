import Connect
import Foundation
import RAProto
import SwiftProtobuf

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

    public func keyGenerate(_ eff: PBSecurityKeyGenerateEffect) {
        enqueueIO { [self] in
            var ev = PBSecurityKeyGeneratedEvent()
            ev.opID = eff.opID
            do {
                ev.publicKey = try store.generateKey(kind: eff.keyKind, slot: eff.slot, persist: eff.persist)
            } catch {
                ev.error = error.localizedDescription
            }
            await sendEvent(PBCoreEvent.with { $0.securityKeyGenerated = ev })
        }
    }

    public func keySign(_ eff: PBSecurityKeySignEffect) {
        enqueueIO { [self] in
            var ev = PBSecurityKeySignedEvent()
            ev.opID = eff.opID
            if eff.message.isEmpty {
                ev.error = "empty message to sign"
            } else {
                do {
                    let kind = eff.keyKind == .unspecified ? .deviceEd : eff.keyKind
                    ev.signature = try store.signKey(kind: kind, slot: eff.slot, message: eff.message)
                } catch {
                    ev.error = error.localizedDescription
                }
            }
            await sendEvent(PBCoreEvent.with { $0.securityKeySigned = ev })
        }
    }

    public func keyAgree(_ eff: PBSecurityKeyAgreeEffect) {
        enqueueIO { [self] in
            var ev = PBSecurityKeyAgreedEvent()
            ev.opID = eff.opID
            do {
                let kind = eff.keyKind == .unspecified ? .deviceX : eff.keyKind
                ev.shared = try store.agreeKey(kind: kind, slot: eff.slot, peerPublic: eff.peerPublic)
            } catch {
                ev.error = error.localizedDescription
            }
            await sendEvent(PBCoreEvent.with { $0.securityKeyAgreed = ev })
        }
    }

    public func keyEncapsulate(_ eff: PBSecurityKeyEncapsulateEffect) {
        enqueueIO { [self] in
            var ev = PBSecurityKeyEncapsulatedEvent()
            ev.opID = eff.opID
            do {
                ev.pairs = try store.encapsulate(recipients: eff.recipients)
            } catch {
                ev.error = error.localizedDescription
            }
            await sendEvent(PBCoreEvent.with { $0.securityKeyEncapsulated = ev })
        }
    }

    public func keyDiscard(_ eff: PBSecurityKeyDiscardEffect) {
        enqueueIO { [self] in
            store.discardKey(kind: eff.keyKind, slot: eff.slot)
        }
    }

    public func storage(_ eff: PBSecurityStorageEffect) {
        enqueueIO { [self] in
            let res = store.executeStorage(eff)
            await sendEvent(PBCoreEvent.with { $0.securityStorageResult = res })
        }
    }

    public func rpc(_ eff: PBSecurityRpcEffect) {
        spawnAsync { [self] in
            let res: PBSecurityRpcResultEvent
            if let rpcOverride {
                res = await rpcOverride(eff)
            } else {
                res = await executeUnaryRpc(eff)
            }
            await sendEvent(PBCoreEvent.with { $0.securityRpcResult = res })
        }
    }

    private func executeUnaryRpc(_ eff: PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent {
        var out = PBSecurityRpcResultEvent()
        out.opID = eff.opID
        guard let path = Self.rpcPath(for: eff.method) else {
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
            let response: HTTPResponse = try await withTimeout(timeoutMs, sleep: realSleep) {
                try await withCheckedThrowingContinuation { continuation in
                    let req = HTTPRequest<Data?>(
                        url: url,
                        headers: finalHeaders,
                        message: eff.requestBytes,
                        method: .post,
                        trailers: [:],
                        idempotencyLevel: .unknown
                    )
                    _ = http.unary(request: req, onMetrics: { _ in }) { resp in
                        continuation.resume(returning: resp)
                    }
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

    private static func rpcPath(for method: PBSecurityRpcMethod) -> String? {
        switch method {
        case .getSecurityStates: return "/remoteagent.v1.ClientService/GetSecurityStates"
        case .putSecurityState: return "/remoteagent.v1.ClientService/PutSecurityState"
        case .pinSecurityDomain: return "/remoteagent.v1.ClientService/PinSecurityDomain"
        case .createPairing: return "/remoteagent.v1.PairingService/CreatePairing"
        case .peekPairing: return "/remoteagent.v1.PairingService/PeekPairing"
        case .joinPairing: return "/remoteagent.v1.PairingService/JoinPairing"
        case .revealPairing: return "/remoteagent.v1.PairingService/RevealPairing"
        case .approvePairing: return "/remoteagent.v1.PairingService/ApprovePairing"
        case .getPairing: return "/remoteagent.v1.PairingService/GetPairing"
        case .cancelPairing: return "/remoteagent.v1.PairingService/CancelPairing"
        case .getRecoveryChallenge: return "/remoteagent.v1.RecoveryService/GetRecoveryChallenge"
        case .getRecoveryContext: return "/remoteagent.v1.RecoveryService/GetRecoveryContext"
        case .submitRecovery: return "/remoteagent.v1.RecoveryService/SubmitRecovery"
        case .completeRecovery: return "/remoteagent.v1.RecoveryService/CompleteRecovery"
        case .deleteMachineRecords: return "/remoteagent.v1.ClientService/DeleteMachineRecords"
        case .deleteAccount: return "/remoteagent.v1.ClientService/DeleteAccount"
        case .getChallenge: return "/remoteagent.v1.AuthService/GetChallenge"
        case .login: return "/remoteagent.v1.AuthService/Login"
        case .getSignupChallenge: return "/remoteagent.v1.AuthService/GetSignupChallenge"
        case .registerDevice: return "/remoteagent.v1.AuthService/RegisterDevice"
        case .putDeviceMeta: return "/remoteagent.v1.ClientService/PutDeviceMeta"
        case .listDevices: return "/remoteagent.v1.ClientService/ListDevices"
        case .listMachines: return "/remoteagent.v1.ClientService/ListMachines"
        case .getAccountPrefs: return "/remoteagent.v1.ClientService/GetAccountPrefs"
        case .putAccountPrefs: return "/remoteagent.v1.ClientService/PutAccountPrefs"
        default: return nil
        }
    }

    private static func parseConnectErrorBody(_ data: Data?, fallbackCode: Code) -> (code: Code, message: String) {
        let fallback = "HTTP error (\(fallbackCode.name))"
        guard let data, !data.isEmpty,
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else {
            return (fallbackCode, fallback)
        }
        let codeStr = (json["code"] as? String)?.lowercased()
        let mapped = codeStr.flatMap(connectCode(named:)) ?? fallbackCode
        let msg = (json["message"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? fallback
        return (mapped, msg)
    }

    private static func connectCode(named name: String) -> Code? {
        switch name {
        case "canceled": return .canceled
        case "unknown": return .unknown
        case "invalid_argument": return .invalidArgument
        case "deadline_exceeded": return .deadlineExceeded
        case "not_found": return .notFound
        case "already_exists": return .alreadyExists
        case "permission_denied": return .permissionDenied
        case "resource_exhausted": return .resourceExhausted
        case "failed_precondition": return .failedPrecondition
        case "aborted": return .aborted
        case "out_of_range": return .outOfRange
        case "unimplemented": return .unimplemented
        case "internal": return .internalError
        case "unavailable": return .unavailable
        case "data_loss": return .dataLoss
        case "unauthenticated": return .unauthenticated
        default: return nil
        }
    }
}

/// Decodes and verifies a push notification `PBEnvelope` using Go `clientcore` (`notify_blob`)
/// in a short-lived sandboxed `CoreClient` without needing unlocked device private keys.
public func openNotifyEnvelopeViaCore(
    notifyBlob: Data,
    envelope: PBEnvelope,
    nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000),
    sessionId: String = "",
    tag: String = ""
) throws -> PBNotifyContent? {
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
        }
    })
    for eff in batch.effects {
        if case .securityNotifyOpened(let res)? = eff.effect, res.opID == 1 {
            if !res.error.isEmpty {
                throw SecurityWorkflowError(message: translateSecurityKey(res.error), code: "invalid", retryable: false, uncertain: false, errorAction: "")
            }
            return res.hasContent ? res.content : nil
        }
    }
    return nil
}
