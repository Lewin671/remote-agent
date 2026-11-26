import Connect
import Foundation
import RAProto
import SwiftProtobuf

/// Executes host-side key generation/agreement/signing, v4 storage transactions, and universal
/// unary Connect/HTTP byte-pipe RPC calls requested by Go `clientcore`.
public final class SecurityExecutor: @unchecked Sendable {
    public typealias RpcOverride = @Sendable (PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent
    public typealias RelayRpcOverride = @Sendable (PBRelayRpcEffect) async -> PBRelayRpcResultEvent?

    public let store: any SecurityStoreV4
    private let http: HTTP?
    private let emit: @Sendable (PBCoreEvent) async -> Void
    private let rpcOverride: RpcOverride?
    private let relayRpcOverride: RelayRpcOverride?
    private let allowPlainHttp: Bool
    private let lock = NSLock()
    private var defaultServer: String
    private var defaultDeviceId: String
    private var closed = false
    private var inFlightTasks: [UUID: Task<Void, Never>] = [:]
    private var activeRelayTasks: [UInt64: (id: UUID, task: Task<Void, Never>)] = [:]

    public init(
        store: any SecurityStoreV4,
        http: HTTP? = nil,
        allowPlainHttp: Bool = false,
        defaultServer: String = "",
        defaultDeviceId: String = "",
        rpcOverride: RpcOverride? = nil,
        relayRpcOverride: RelayRpcOverride? = nil,
        emit: @escaping @Sendable (PBCoreEvent) async -> Void
    ) {
        self.store = store
        self.http = http
        self.allowPlainHttp = allowPlainHttp
        self.defaultServer = defaultServer
        self.defaultDeviceId = defaultDeviceId
        self.rpcOverride = rpcOverride
        self.relayRpcOverride = relayRpcOverride
        self.emit = emit
    }

    public func setDefaultAccount(server: String, deviceId: String) {
        lock.withLock {
            defaultServer = server
            defaultDeviceId = deviceId
        }
    }

    public func close() {
        let tasks = lock.withLock { () -> [Task<Void, Never>] in
            guard !closed else { return [] }
            closed = true
            let list = Array(inFlightTasks.values) + activeRelayTasks.values.map(\.task)
            inFlightTasks.removeAll()
            activeRelayTasks.removeAll()
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
        case .relayRpc(let r): relayRpc(r)
        case .relayRpcCancel(let c): cancelRelayRpc(c)
        default: return false
        }
        return true
    }

    private func enqueueOp(_ work: @escaping @Sendable () async -> PBCoreEvent) {
        spawnAsync { [self] in await sendEvent(await work()) }
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
        spawnAsync { [self] in store.discardKey(kind: eff.keyKind, slot: eff.slot) }
    }

    public func storage(_ eff: PBSecurityStorageEffect) {
        enqueueOp { [self] in
            var res = store.executeStorage(eff)
            if eff.op == .readBootstrap && res.loginToken.isEmpty {
                let (server, deviceId) = lock.withLock { (defaultServer, defaultDeviceId) }
                if !server.isEmpty && !deviceId.isEmpty,
                   let saved = store.loadLoginToken(server: server, deviceId: deviceId),
                   !saved.token.isEmpty
                {
                    res.loginToken = saved.token
                    res.loginTokenExpiresMs = saved.expiresMs
                }
            }
            return PBCoreEvent.with { $0.securityStorageResult = res }
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

    public func relayRpc(_ eff: PBRelayRpcEffect) {
        let opID = eff.opID
        let token = UUID()
        let oldTask: Task<Void, Never>? = lock.withLock {
            guard !closed else { return nil }
            let prev = activeRelayTasks[opID]?.task
            let task = Task { [weak self] in
                guard let self else { return }
                defer {
                    self.lock.withLock {
                        if self.activeRelayTasks[opID]?.id == token {
                            self.activeRelayTasks.removeValue(forKey: opID)
                        }
                    }
                }
                let result = await self.executeRelayUnary(eff)
                guard let result, !Task.isCancelled else { return }
                await self.sendEvent(PBCoreEvent.with { $0.relayRpcResult = result })
            }
            activeRelayTasks[opID] = (token, task)
            return prev
        }
        oldTask?.cancel()
    }

    public func cancelRelayRpc(_ eff: PBRelayRpcCancelEffect) {
        let task = lock.withLock { activeRelayTasks.removeValue(forKey: eff.opID)?.task }
        task?.cancel()
    }

    private func executeUnaryRpc(_ eff: PBSecurityRpcEffect) async -> PBSecurityRpcResultEvent {
        guard let method = PBRelayRpcMethod(rawValue: eff.method.rawValue), method != .unspecified else {
            return PBSecurityRpcResultEvent.with {
                $0.opID = eff.opID
                $0.connectCode = Int32(Code.unimplemented.rawValue)
                $0.error = "unknown rpc method: \(eff.method.rawValue)"
            }
        }
        let relayEff = PBRelayRpcEffect.with {
            $0.opID = eff.opID
            $0.method = method
            $0.server = eff.server
            $0.authToken = eff.authToken
            $0.requestBytes = eff.requestBytes
        }
        guard let res = await executeRelayUnary(relayEff) else {
            return PBSecurityRpcResultEvent.with {
                $0.opID = eff.opID
                $0.connectCode = Int32(Code.canceled.rawValue)
                $0.error = "canceled"
            }
        }
        if res.connectCode == 0 && !res.networkError && !res.unresolvedHost && res.diagnosticMessage.isEmpty {
            return PBSecurityRpcResultEvent.with {
                $0.opID = eff.opID
                $0.responseBytes = res.responseBytes
            }
        }
        return PBSecurityRpcResultEvent.with {
            $0.opID = eff.opID
            $0.connectCode = res.connectCode
            $0.error = res.diagnosticMessage
            $0.networkError = res.networkError
            $0.errorDetails = res.errorDetails
        }
    }

    private func executeRelayUnary(_ eff: PBRelayRpcEffect) async -> PBRelayRpcResultEvent? {
        if let relayRpcOverride {
            return await relayRpcOverride(eff)
        }
        let fallbackServer = lock.withLock { defaultServer }
        let server = eff.server.isEmpty ? fallbackServer : eff.server
        return await Self.executeRelayRpcUnary(
            http: http,
            method: eff.method,
            server: server,
            authToken: eff.authToken,
            timeoutMs: eff.timeoutMs,
            attemptHeader: eff.attemptHeader,
            requestBytes: eff.requestBytes,
            allowPlainHttp: allowPlainHttp,
            opID: eff.opID
        )
    }

    /// Executes a single unary ConnectRPC/HTTP byte-pipe call. Returns `nil` when cancelled via Swift Task cancellation.
    public static func executeRelayRpcUnary(
        http: HTTP?,
        method: PBRelayRpcMethod,
        server: String,
        authToken: String = "",
        timeoutMs: Int64 = 0,
        attemptHeader: String = "",
        requestBytes: Data = Data(),
        allowPlainHttp: Bool = false,
        opID: UInt64 = 0
    ) async -> PBRelayRpcResultEvent? {
        var out = PBRelayRpcResultEvent()
        out.opID = opID
        guard let path = relayRpcPaths[method] else {
            out.connectCode = 12 // unimplemented
            out.diagnosticMessage = "unknown rpc method: \(method.rawValue)"
            return out
        }
        guard let http else {
            out.connectCode = 14 // unavailable
            out.diagnosticMessage = "http unavailable"
            out.networkError = true
            return out
        }
        guard !server.isEmpty else {
            out.connectCode = 3 // invalid_argument
            out.diagnosticMessage = "missing relay server"
            return out
        }
        do {
            let base = try checkedServerURL(server, allowPlainHttp: allowPlainHttp)
            guard let url = URL(string: base + path) else {
                out.connectCode = 3 // invalid_argument
                out.diagnosticMessage = "invalid server url"
                return out
            }
            var req = URLRequest(url: url)
            if method == .healthz {
                req.httpMethod = "GET"
                req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
            } else {
                req.httpMethod = "POST"
                req.httpBody = requestBytes
                req.setValue("application/proto", forHTTPHeaderField: "Content-Type")
                req.setValue("1", forHTTPHeaderField: "Connect-Protocol-Version")
                if !authToken.isEmpty {
                    req.setValue("Bearer \(authToken)", forHTTPHeaderField: "Authorization")
                }
                if !attemptHeader.isEmpty {
                    req.setValue(attemptHeader, forHTTPHeaderField: "Remote-Agent-Attempt")
                }
            }
            let effectiveTimeout: Int64 = timeoutMs > 0
                ? timeoutMs
                : (method == .getSecurityStates ? 5_000 : 15_000)
            let finalReq = req
            let (data, response) = try await withTimeout(effectiveTimeout, sleep: realSleep) {
                try await http.data(for: finalReq)
            }
            if let epoch = response.value(forHTTPHeaderField: "Remote-Agent-Relay-Epoch"), !epoch.isEmpty {
                out.relayEpoch = epoch
            }
            if (200..<300).contains(response.statusCode) {
                out.responseBytes = data
                return out
            }
            let parsed = parseConnectErrorBody(data, fallbackCode: connectCodeFromHttpStatus(response.statusCode))
            out.connectCode = Int32(parsed.code.rawValue)
            out.diagnosticMessage = parsed.message
            out.errorDetails = parsed.details
            return out
        } catch is CancellationError {
            return nil
        } catch is TimedOut {
            out.connectCode = 4 // deadline_exceeded
            out.diagnosticMessage = "connection timed out"
            out.networkError = true
            return out
        } catch let e as URLError where e.code == .cancelled && Task.isCancelled {
            return nil
        } catch let e as URLError where e.code == .timedOut {
            out.connectCode = 4 // deadline_exceeded
            out.diagnosticMessage = "connection timed out"
            out.networkError = true
            return out
        } catch {
            if Task.isCancelled { return nil }
            out.connectCode = 14 // unavailable
            out.diagnosticMessage = errorText(error)
            out.networkError = true
            out.unresolvedHost = unresolvedHost(error)
            return out
        }
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

    static func connectCodeFromHttpStatus(_ status: Int) -> Code {
        switch status {
        case 400: .invalidArgument
        case 401: .unauthenticated
        case 403: .permissionDenied
        case 404: .notFound
        case 408, 504: .deadlineExceeded
        case 409: .aborted
        case 412: .failedPrecondition
        case 429, 502, 503: .unavailable
        case 500...599: .internalError
        default: .unknown
        }
    }

    private static let relayRpcPaths: [PBRelayRpcMethod: String] = [
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
        .listRemoteSessions: "/remoteagent.v1.ClientService/ListRemoteSessions",
        .getEvents: "/remoteagent.v1.ClientService/GetEvents",
        .forward: "/remoteagent.v1.ClientService/Forward",
        .getImage: "/remoteagent.v1.ClientService/GetImage",
        .getPushKey: "/remoteagent.v1.ClientService/GetPushKey",
        .setPushSubscription: "/remoteagent.v1.ClientService/SetPushSubscription",
        .setPushTarget: "/remoteagent.v1.ClientService/SetPushTarget",
        .healthz: "/healthz",
    ]

    private static let connectCodes: [String: Code] = [
        "canceled": .canceled, "unknown": .unknown, "invalid_argument": .invalidArgument,
        "deadline_exceeded": .deadlineExceeded, "not_found": .notFound, "already_exists": .alreadyExists,
        "permission_denied": .permissionDenied, "resource_exhausted": .resourceExhausted,
        "failed_precondition": .failedPrecondition, "aborted": .aborted, "out_of_range": .outOfRange,
        "unimplemented": .unimplemented, "internal": .internalError, "unavailable": .unavailable,
        "data_loss": .dataLoss, "unauthenticated": .unauthenticated,
    ]

    private static func decodeConnectDetailBase64(_ raw: String) -> Data? {
        let cleaned = raw
            .filter { !$0.isWhitespace }
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        guard !cleaned.isEmpty else { return nil }
        let rem = cleaned.count % 4
        let padded = rem == 0 ? cleaned : cleaned + String(repeating: "=", count: 4 - rem)
        return Data(base64Encoded: padded)
    }

    static func parseConnectErrorBody(_ data: Data?, fallbackCode: Code) -> (code: Code, message: String, details: [PBRpcErrorDetailBytes]) {
        let fallback = "HTTP error (\(fallbackCode.name))"
        guard let data, !data.isEmpty,
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else {
            return (fallbackCode, fallback, [])
        }
        let codeStr = (json["code"] as? String)?.lowercased()
        let mapped = codeStr.flatMap { connectCodes[$0] } ?? fallbackCode
        let msg = (json["message"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? fallback
        var details: [PBRpcErrorDetailBytes] = []
        if let rawList = json["details"] as? [[String: Any]] {
            for item in rawList {
                guard let typeName = item["type"] as? String, !typeName.isEmpty,
                      let rawVal = item["value"] as? String,
                      let decoded = decodeConnectDetailBase64(rawVal), !decoded.isEmpty
                else { continue }
                details.append(PBRpcErrorDetailBytes.with {
                    $0.typeName = typeName
                    $0.value = decoded
                })
            }
        }
        return (mapped, msg, details)
    }
}

/// Decodes, verifies, and projects a push notification `PBEnvelope` using Go `clientcore` (`notify_blob`)
/// in a short-lived sandboxed `CoreClient` without needing unlocked device private keys.
/// Fail-closed: returns `nil` whenever `expectedKind` is unspecified or Core marks `display.valid == false`.
public func openNotifyDisplayViaCore(
    notifyBlob: Data,
    rawEnvelope: Data,
    expectedKind: PBNotifyKind,
    nowMs: Int64 = Int64(Date().timeIntervalSince1970 * 1000),
    machineId: String = "",
    sessionId: String = "",
    tag: String = "",
    eventKey: String = "",
    fallbackBody: String? = nil
) throws -> PBSecurityNotifyDisplay? {
    guard expectedKind != .unspecified, !rawEnvelope.isEmpty else { return nil }
    let core = try CoreClient()
    defer { core.close() }
    let batch = try core.dispatch(PBCoreEvent.with {
        $0.nowMs = nowMs
        $0.monoMs = nowMs
        $0.securityConfig = .with { $0.inlineCrypto = true }
        $0.securityOpenNotify = .with {
            $0.opID = 1
            $0.notifyBlob = notifyBlob
            $0.rawEnvelope = rawEnvelope
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
