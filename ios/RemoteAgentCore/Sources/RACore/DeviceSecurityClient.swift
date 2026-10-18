import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

public actor DeviceSecurityClient {
    public typealias Exchange =
        @Sendable (DeviceSecurity.Capsule) async throws -> DeviceSecurity.Capsule
    private let store: any DeviceSecurity.Storage
    private let memberID: String
    private let signingKey: SigningKey
    private let exchange: Exchange
    private var syncJob: Task<DeviceSecurity.Record, Error>?
    private var historical: [UInt64: (DeviceSecurity.State, Data)] = [:]
    private var closed = false
    public init(
        store: any DeviceSecurity.Storage, memberID: String, signingKey: SigningKey,
        exchange: @escaping Exchange
    ) {
        self.store = store
        self.memberID = memberID
        self.signingKey = signingKey
        self.exchange = exchange
    }
    private func read() throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!closed, .storage)
        let r = try store.readSecurity()
        try DeviceSecurity.check(
            r.memberID == memberID && deviceId(publicKey: signingKey.publicKey) == memberID,
            .untrusted)
        return r
    }
    public func identity() throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        let r = try read()
        return (r.pin, r.state)
    }
    public func close() {
        closed = true
        syncJob?.cancel()
        syncJob = nil
        historical.removeAll()
    }
    public func rpc(_ method: DeviceSecurity.Method, jsonBody: Data) async throws -> Data {
        try Task.checkCancellation()
        let r = try read()
        let (q, w) = try DeviceSecurity.request(
            pin: r.pin, sender: memberID, signingKey: signingKey, method: method, jsonBody: jsonBody
        )
        let response = try await exchange(q)
        try Task.checkCancellation()
        _ = try read()
        return try r.openReply(w, response)
    }
    public func sync() async throws -> DeviceSecurity.Record {
        if let syncJob { return try await syncJob.value }
        let task = Task { try await self.syncLoop() }
        syncJob = task
        defer { syncJob = nil }
        return try await task.value
    }
    private func syncLoop() async throws -> DeviceSecurity.Record {
        for _ in 0..<32768 {
            let r = try read()
            let body = try JSONSerialization.data(withJSONObject: ["After": r.state.core.epoch])
            let raw = try await rpc(.states, jsonBody: body)
            let reply = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(raw))
            let head = try DeviceSecurity.jsonHash(reply["Current"])
            let states =
                (reply["States"] is NSNull || reply["States"] == nil)
                ? [] : reply["States"] as? [Any]
            guard let states else { throw DeviceSecurity.Failure.invalid }
            if head == (try DeviceSecurity.stateHash(r.state)) {
                try DeviceSecurity.check(states.isEmpty)
                return r
            }
            try DeviceSecurity.check(!states.isEmpty && states.count <= 2, .stale)
            var current = r
            for value in states {
                let next = try DeviceSecurity.stateFromJSON(value)
                do {
                    current = try store.advanceSecurity(
                        expectedState: DeviceSecurity.stateHash(current.state), next: next)
                } catch DeviceSecurity.Failure.stale { break }
            }
            try Task.checkCancellation()
        }
        throw DeviceSecurity.Failure.capacity
    }
    /// Counter reservation precedes forming C. C is never published before its
    /// exact authorization; mutable C travels only inside authority HPKE.
    public func seal(
        purpose: DeviceSecurity.Purpose, machine: String = "", session: String = "",
        request: String = "", seq: UInt64 = 0, deadline: UInt64 = 0, plaintext: Data,
        sessionSeed: Data? = nil
    ) async throws -> PBEnvelope {
        for _ in 0..<3 {
            try Task.checkCancellation()
            let old = try read()
            let r = try store.reserveSecurity(expectedState: DeviceSecurity.stateHash(old.state))
            let h = DeviceSecurity.Header(
                account: r.pin.account, domain: r.pin.domain, epoch: r.state.core.epoch,
                state: try DeviceSecurity.stateHash(r.state),
                contentEpoch: r.state.core.contentEpoch,
                sender: memberID, kind: purpose.kind, purpose: purpose, machine: machine,
                session: session,
                request: request, seq: seq, deadline: deadline, compression: 0, counter: r.counter)
            let formed = try DeviceSecurity.form(
                secret: r.secret(), sessionSeed: sessionSeed, header: h, plaintext: plaintext,
                signingKey: signingKey)
            do {
                let response = try await rpc(
                    .authorize, jsonBody: DeviceSecurity.authorizationRequest(formed))
                let w = try DeviceSecurity.attach(
                    formed, jsonReply: response, pin: r.pin, state: r.state)
                return try DeviceSecurity.envelope(w)
            } catch DeviceSecurity.Failure.stale { _ = try await sync() }
        }
        throw DeviceSecurity.Failure.stale
    }
    public struct Catalog: Sendable {
        public let state: DeviceSecurity.State
        public let entries: [DeviceSecurity.Wire]
    }
    public func catalog() async throws -> Catalog {
        let r = try await sync()
        let expected = try DeviceSecurity.stateHash(r.state)
        var cursor = Data()
        var entries: [DeviceSecurity.Wire] = []
        for _ in 0..<4096 {
            let response = try await rpc(
                .catalog,
                jsonBody: JSONSerialization.data(withJSONObject: [
                    "After": cursor.base64EncodedString(), "AtState": [UInt8](expected),
                ]))
            let reply = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(response))
            let state = try DeviceSecurity.stateFromJSON(reply["State"] as Any)
            try DeviceSecurity.verifyState(r.pin, state)
            try DeviceSecurity.check(DeviceSecurity.stateHash(state) == expected, .stale)
            let rows = reply["Entries"] as? [[String: Any]] ?? []
            for row in rows {
                entries.append(try DeviceSecurity.wireFromJSON(row["Wire"] as Any))
                try DeviceSecurity.check(entries.count <= 4096, .capacity)
            }
            if reply["Next"] == nil || reply["Next"] is NSNull || reply["Next"] as? String == "" {
                return Catalog(state: state, entries: entries)
            }
            let next = try DeviceSecurity.jsonBytes(reply, "Next")
            try DeviceSecurity.check(cursor.lexicographicallyPrecedes(next))
            cursor = next
        }
        throw DeviceSecurity.Failure.capacity
    }
    public struct Removal: Codable, Equatable, Sendable {
        public let request: Data
        public var phase: String
        public var waiting: [String]
        public var error: String
        public var completion: Data?
        public var chain: [Data] = []
        public var serverCleaned = false
        public var operation: String {
            (try? DeviceSecurity.object(DeviceSecurity.safeJSONObject(request))["Operation"]
                as? String) ?? ""
        }
        public var targets: [String] {
            ((try? DeviceSecurity.object(DeviceSecurity.safeJSONObject(request))["Targets"]
                as? [[String: Any]]) ?? []).compactMap { $0["ID"] as? String }
        }
    }
    public func removalOperations() throws -> [Removal] {
        guard let data = try store.readRemovalOperations() else { return [] }
        let all: [Removal]
        do { all = try JSONDecoder().decode([Removal].self, from: data) } catch {
            throw DeviceSecurity.Failure.storage
        }
        try DeviceSecurity.check(all.count <= 128, .storage)
        let record = try read()
        for op in all {
            let request = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(op.request))
            _ = try DeviceSecurity.deleteSignBytes(request)
            let account = try DeviceSecurity.jsonString(request, "Account")
            let domain = try DeviceSecurity.jsonHash(request["Domain"])
            let requester = try DeviceSecurity.jsonString(request, "Requester")
            try DeviceSecurity.check(
                account == record.pin.account && domain == record.pin.domain
                    && requester == memberID
                    && ["unconfirmed", "pending", "complete"].contains(op.phase), .storage)
            try DeviceSecurity.check(
                verifyEd25519(
                    publicKey: signingKey.publicKey,
                    message: DeviceSecurity.deleteSignBytes(request),
                    signature: DeviceSecurity.jsonBytes(request, "Signature")), .storage)
        }
        return all
    }
    private func saveRemoval(_ op: Removal) throws {
        var all = try removalOperations().filter { $0.operation != op.operation }
        all.append(op)
        while all.count > 128 {
            guard let index = all.firstIndex(where: { $0.phase == "complete" && $0.serverCleaned })
            else { throw DeviceSecurity.Failure.capacity }
            all.remove(at: index)
        }
        try store.writeRemovalOperations(JSONEncoder().encode(all))
    }
    public func remove(target: String, commit: Data, expected: Data) async throws -> Removal {
        let r = try read()
        let request = try DeviceSecurity.prepareDelete(
            pin: r.pin, requester: memberID, target: target,
            expected: expected, metadataCommit: commit, signingKey: signingKey)
        let op = Removal(request: request, phase: "unconfirmed", waiting: [], error: "")
        try saveRemoval(op)
        return try await retryRemoval(op)
    }
    private func waiting(_ group: [String: Any]) throws -> [String] {
        guard let required = group["Required"] as? [String], required.count <= 128 else {
            throw DeviceSecurity.Failure.invalid
        }
        let received = Set(
            (group["Receipts"] as? [[String: Any]] ?? []).compactMap { $0["Executor"] as? String })
        return required.filter { !received.contains($0) }
    }
    private func hasOperation(_ group: [String: Any], _ op: Removal) throws -> Bool {
        guard let operations = group["Operations"] as? [[String: Any]], operations.count <= 128
        else { throw DeviceSecurity.Failure.invalid }
        return operations.contains {
            (($0["Request"] as? [String: Any])?["Operation"] as? String) == op.operation
        }
    }
    public func retryRemoval(_ op: Removal) async throws -> Removal {
        do {
            let response = try await rpc(.delete, jsonBody: op.request)
            let group = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(response))
            try DeviceSecurity.check(hasOperation(group, op), .untrusted)
            var pending = op
            pending.phase = "pending"
            pending.waiting = try waiting(group)
            pending.error = ""
            try saveRemoval(pending)
            return try await pollRemoval(pending)
        } catch {
            if error is CancellationError { throw error }
            var failed = op
            failed.error = error is DeviceSecurity.Failure ? String(describing: error) : "offline"
            try saveRemoval(failed)
            return failed
        }
    }
    public func markServerCleaned(_ op: Removal) throws -> Removal {
        var done = op
        done.serverCleaned = true
        done.completion = nil
        done.chain = []
        try saveRemoval(done)
        return done
    }
    public func pollRemoval(_ op: Removal) async throws -> Removal {
        if op.phase == "complete" { return op }
        let completion: Data
        do {
            completion = try await rpc(
                .getCompletion,
                jsonBody: JSONSerialization.data(withJSONObject: ["Operation": op.operation]))
        } catch DeviceSecurity.Failure.pending {
            let status = try await rpc(
                .completionStates,
                jsonBody: JSONSerialization.data(withJSONObject: [
                    "Operation": op.operation, "After": 0,
                ]))
            let reply = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(status))
            let group = try DeviceSecurity.object(reply["Group"] as Any)
            try DeviceSecurity.check(hasOperation(group, op), .untrusted)
            var pending = op
            pending.phase = "pending"
            pending.waiting = try waiting(group)
            pending.error = ""
            try saveRemoval(pending)
            return pending
        }
        let reply = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(completion))
        let group = try DeviceSecurity.object(reply["Group"] as Any)
        let base = try DeviceSecurity.stateFromJSON(group["Base"] as Any)
        let head = try DeviceSecurity.stateFromJSON(group["Head"] as Any)
        var after = base.core.epoch
        var chain: [DeviceSecurity.State] = []
        for _ in 0..<128 {
            if after >= head.core.epoch { break }
            let raw = try await rpc(
                .completionStates,
                jsonBody: JSONSerialization.data(withJSONObject: [
                    "Operation": op.operation, "After": after,
                ]))
            let page = try DeviceSecurity.object(DeviceSecurity.safeJSONObject(raw))
            guard let states = page["States"] as? [Any], (1...2).contains(states.count) else {
                throw DeviceSecurity.Failure.untrusted
            }
            for value in states {
                let state = try DeviceSecurity.stateFromJSON(value)
                try DeviceSecurity.check(
                    state.core.epoch > after && state.core.epoch <= head.core.epoch, .untrusted)
                chain.append(state)
                after = state.core.epoch
            }
        }
        try DeviceSecurity.verifyCompletion(
            pin: read().pin, completionJSON: completion, originalJSON: op.request, chain: chain)
        var done = op
        done.phase = "complete"
        done.waiting = []
        done.error = ""
        done.completion = completion
        done.chain = try chain.map { try DeviceSecurity.stateBytes($0) }
        try saveRemoval(done)
        return done
    }

    public func openCurrent(_ envelope: PBEnvelope, sessionSeed: Data? = nil) async throws -> (
        DeviceSecurity.Header, Data
    ) {
        let wire = try DeviceSecurity.fromEnvelope(envelope)
        let r = try await sync()
        let (h, p) = try DeviceSecurity.open(
            r.pin, r.state, wire, secret: r.secret(), sessionSeed: sessionSeed)
        return (h, try unpackSecurity(h, p))
    }
    /// This path never grants live action authority or lowers the persisted head.
    public func openHistory(_ envelope: PBEnvelope, sessionSeed: Data? = nil) async throws -> (
        DeviceSecurity.Header, Data
    ) {
        let w = try DeviceSecurity.fromEnvelope(envelope)
        let h = try DeviceSecurity.parseHeader(w.formed.header)
        try DeviceSecurity.check(!h.purpose.control)
        var r = try read()
        if h.epoch > r.state.core.epoch { r = try await sync() }
        if h.epoch == r.state.core.epoch {
            let (h, p) = try DeviceSecurity.open(
                r.pin, r.state, w, secret: r.secret(), sessionSeed: sessionSeed)
            return (h, try unpackSecurity(h, p))
        }
        try DeviceSecurity.check(h.epoch < r.state.core.epoch, .stale)
        if historical[h.epoch] == nil, let cached = try store.readSecurityHistory(epoch: h.epoch) {
            historical[h.epoch] = (cached.state, cached.secret)
        }
        if let (s, key) = historical[h.epoch] {
            let (h, p) = try DeviceSecurity.open(r.pin, s, w, secret: key, sessionSeed: sessionSeed)
            return (h, try unpackSecurity(h, p))
        }
        let sr = try DeviceSecurity.object(
            DeviceSecurity.safeJSONObject(
                await rpc(
                    .states,
                    jsonBody: JSONSerialization.data(withJSONObject: ["After": h.epoch - 1]))
            ))
        guard let states = sr["States"] as? [Any], let first = states.first else {
            throw DeviceSecurity.Failure.untrusted
        }
        let state = try DeviceSecurity.stateFromJSON(first)
        try DeviceSecurity.verifyState(r.pin, state)
        try DeviceSecurity.check(state.core.epoch == h.epoch, .untrusted)
        let kr = try DeviceSecurity.object(
            DeviceSecurity.safeJSONObject(
                await rpc(
                    .historyKeys,
                    jsonBody: JSONSerialization.data(withJSONObject: ["After": h.contentEpoch - 1]))
            ))
        guard let keys = kr["Keys"] as? [[String: Any]], let firstKey = keys.first else {
            throw DeviceSecurity.Failure.untrusted
        }
        try DeviceSecurity.check(
            DeviceSecurity.jsonUInt(firstKey, "Epoch") == h.contentEpoch, .untrusted)
        let key = try DeviceSecurity.jsonBytes(firstKey, "Secret")
        try DeviceSecurity.check(key.count == 32, .untrusted)
        if historical.count >= 64, let oldest = historical.keys.min() {
            historical.removeValue(forKey: oldest)
        }
        try store.cacheSecurityHistory(DeviceSecurity.Historical(state: state, secret: key))
        historical[h.epoch] = (state, key)
        let (openedHeader, plain) = try DeviceSecurity.open(
            r.pin, state, w, secret: key, sessionSeed: sessionSeed)
        return (openedHeader, try unpackSecurity(openedHeader, plain))
    }
    public func openMessage<M: SwiftProtobuf.Message>(
        _ envelope: PBEnvelope, as type: M.Type, history: Bool, sessionSeed: Data? = nil
    ) async throws -> (DeviceSecurity.Header, M) {
        let (h, p) =
            try await
            (history
            ? openHistory(envelope, sessionSeed: sessionSeed)
            : openCurrent(envelope, sessionSeed: sessionSeed))
        do {
            let m = try M(serializedBytes: p)
            try validateSecurityBody(h, m)
            return (h, m)
        } catch { throw DeviceSecurity.Failure.invalid }
    }
}
func unpackSecurity(_ h: DeviceSecurity.Header, _ p: Data) throws -> Data {
    let limit = h.purpose.mutable ? 64 << 10 : 4 << 20
    if h.compression == 0 {
        try DeviceSecurity.check(p.count <= limit, .capacity)
        return p
    }
    try DeviceSecurity.check(h.compression == 1)
    return try unpack(p, max: limit)
}
func validateSecurityBody<M: SwiftProtobuf.Message>(_ h: DeviceSecurity.Header, _ m: M) throws {
    switch h.purpose {
    case .event:
        guard let e = m as? PBEvent, e.sessionID == h.session, e.seq == h.seq, e.kind != nil else {
            throw DeviceSecurity.Failure.invalid
        }
    case .sessionMeta:
        guard let s = m as? PBSession, s.id == h.session else {
            throw DeviceSecurity.Failure.invalid
        }
    case .machineMeta: try DeviceSecurity.check(m is PBMachineMeta)
    case .sessionKey: throw DeviceSecurity.Failure.invalid
    case .deviceMeta: try DeviceSecurity.check(m is PBDeviceMeta)
    case .prefs:
        guard let p = m as? PBProjectPrefs, p.schema == 1 else {
            throw DeviceSecurity.Failure.invalid
        }
    case .command:
        guard let c = m as? PBCommand, c.kind != nil else { throw DeviceSecurity.Failure.invalid }
    case .result: try DeviceSecurity.check(m is PBCommandResult)
    case .image: try DeviceSecurity.check(m is PBImagePayload)
    case .notify:
        guard let n = m as? PBNotifyContent, n.machineID == h.machine, n.sessionID == h.session,
            n.kind == .attention || n.kind == .turnEnded
        else { throw DeviceSecurity.Failure.invalid }
    case .terminalInput, .terminalOutput, .terminalResize, .terminalHistory:
        guard let f = m as? PBTerminalFrame else { throw DeviceSecurity.Failure.invalid }
        switch (h.purpose, f.kind) {
        case (.terminalInput, .input), (.terminalResize, .resize),
            (.terminalHistory, .historyRequest),
            (.terminalOutput, .snapshot), (.terminalOutput, .output),
            (.terminalOutput, .historyPage),
            (.terminalOutput, .closed):
            break
        default: throw DeviceSecurity.Failure.invalid
        }
    }
}
