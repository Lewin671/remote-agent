import Foundation
import RAProto
import Security

/// Drives a standalone Go `CoreClient` + `SecurityExecutor` when no host `CoreHost` is attached
/// (e.g. standalone `Remote` tests or `AuthorityCoordinator.resumeHandoffOffer` device-meta sealing).
final class CoreSecurityRunner: @unchecked Sendable {
    private let lock = NSLock()
    private let client: CoreClient
    private let executor: SecurityExecutor
    private var configured = false
    private var closed = false
    private var nextOpID: UInt64 = 1
    private var pushSeq: UInt64 = 1
    private var lastNowMs: Int64 = 0
    private var lastSnapshot: SnapshotKey?

    private struct SnapshotKey: Equatable {
        let account: String
        let headEpoch: UInt64
        let headHash: Data
        let floorMs: UInt64
        let leaseBytes: Data
        let recoveryCount: Int
        let keyCount: Int
    }

    init(executor: SecurityExecutor) throws {
        self.client = try CoreClient()
        self.executor = executor
    }

    func close() {
        lock.lock()
        defer { lock.unlock() }
        guard !closed else { return }
        closed = true
        client.close()
    }

    func configure(account: String, deviceId: String, nowMs: Int64) throws {
        lock.lock()
        defer { lock.unlock() }
        try configureLocked(account: account, deviceId: deviceId, nowMs: nowMs)
    }

    private func configureLocked(account: String, deviceId: String, nowMs: Int64) throws {
        guard !closed else { throw DeviceSecurity.Failure.storage }
        if !configured {
            _ = try stepLocked(.with {
                $0.entropyProvided = Self.buildEntropy(minIds: 64, minFloats: 64, minRandomBytes: 4096)
            })
            _ = try stepLocked(.with {
                $0.securityConfig = .with {
                    $0.account = account
                    $0.deviceID = deviceId
                    $0.inlineCrypto = true
                }
            })
            configured = true
        }
        if nowMs > 0 { lastNowMs = nowMs }
        _ = try stepLocked(.with {
            $0.nowMs = nowMs
            $0.monoMs = nowMs
            $0.clockUpdated = Self.localCalendar(nowMs: nowMs)
        })
    }

    func syncRecord(_ ev: PBSecurityRecordEvent, nowMs: Int64) throws {
        lock.lock()
        defer { lock.unlock() }
        guard !closed else { throw DeviceSecurity.Failure.storage }
        try configureLocked(account: ev.account, deviceId: "", nowMs: nowMs)
        var event = ev
        if event.pushSeq == 0 {
            event.pushSeq = pushSeq
            pushSeq &+= 1
        }
        _ = try stepLocked(.with { $0.securityRecord = event })
    }

    func syncRecord(
        _ record: DeviceSecurity.Record,
        recoveryPending: Bool = false,
        recoveryReadyMs: UInt64 = 0,
        recoveryObjectsPending: Bool = false,
        nowMs: Int64
    ) throws {
        lock.lock()
        defer { lock.unlock() }
        guard !closed else { throw DeviceSecurity.Failure.storage }
        let ev = try Self.makeRecordEvent(
            record: record,
            opId: 0,
            pushSeq: pushSeq,
            networkGeneration: 1,
            recoveryPending: recoveryPending,
            recoveryReadyMs: recoveryReadyMs,
            recoveryObjectsPending: recoveryObjectsPending
        )
        let key = SnapshotKey(
            account: record.pin.account,
            headEpoch: record.headEpoch,
            headHash: try DeviceSecurity.stateHash(record.state),
            floorMs: record.floor,
            leaseBytes: ev.lease,
            recoveryCount: record.recovery.count,
            keyCount: ev.contentKeys.count
        )
        if key == lastSnapshot {
            try configureLocked(account: record.pin.account, deviceId: record.memberID, nowMs: nowMs)
            return
        }
        pushSeq &+= 1
        try configureLocked(account: record.pin.account, deviceId: record.memberID, nowMs: nowMs)
        _ = try stepLocked(.with { $0.securityRecord = ev })
        lastSnapshot = key
    }

    func runOp(_ configureReq: (inout PBSecurityOpRequestEvent) -> Void) throws -> PBSecurityOpResultEffect {
        lock.lock()
        defer { lock.unlock() }
        guard !closed else { throw DeviceSecurity.Failure.storage }
        let id = nextOpID
        nextOpID &+= 1
        var req = PBSecurityOpRequestEvent()
        req.opID = id
        configureReq(&req)
        let effs = try stepLocked(.with { $0.securityOpRequest = req })
        for eff in effs.reversed() {
            if case .securityOpResult(let res)? = eff.effect, res.opID == id {
                if !res.error.isEmpty {
                    throw Self.mapOpError(res)
                }
                return res
            }
        }
        throw DeviceSecurity.Failure.invalid
    }

    private func stepLocked(_ initial: PBCoreEvent) throws -> [PBCoreEffect] {
        var remaining: [PBCoreEffect] = []
        var queue: [PBCoreEvent] = [initial]
        while !queue.isEmpty {
            var next = queue.removeFirst()
            if next.nowMs == 0 && lastNowMs > 0 { next.nowMs = lastNowMs }
            if next.monoMs == 0 && lastNowMs > 0 { next.monoMs = lastNowMs }
            let batch = try client.dispatch(next)
            for eff in batch.effects {
                switch eff.effect {
                case .requestEntropy(let req):
                    queue.append(.with {
                        $0.entropyProvided = Self.buildEntropy(
                            minIds: max(Int(req.minIds), 32),
                            minFloats: max(Int(req.minFloats), 32),
                            minRandomBytes: max(Int(req.minRandomBytes), 4096)
                        )
                    })
                case .securityKeySign(let s):
                    let signed = executor.keySign(s)
                    queue.append(.with { $0.securityKeySigned = signed })
                case .securityKeyAgree(let a):
                    let agreed = executor.keyAgree(a)
                    queue.append(.with { $0.securityKeyAgreed = agreed })
                default:
                    remaining.append(eff)
                }
            }
        }
        return remaining
    }

    static func mapOpError(_ res: PBSecurityOpResultEffect) -> Error {
        switch res.errorCode {
        case "unsupported_history":
            return UnsupportedHistoryError()
        case "recovery_unavailable":
            return DeviceSecurity.Failure.unavailable
        case "stale":
            return res.error.contains("lease expired") ? DeviceSecurity.Failure.lease : DeviceSecurity.Failure.stale
        default:
            return SecCall.failure(from: res.errorCode.isEmpty ? "invalid" : res.errorCode)
        }
    }

    static func makeRecordEvent(
        record: DeviceSecurity.Record,
        opId: UInt64,
        pushSeq: UInt64,
        networkGeneration: UInt64,
        recoveryPending: Bool,
        recoveryReadyMs: UInt64,
        recoveryObjectsPending: Bool
    ) throws -> PBSecurityRecordEvent {
        let head = record.state
        let headHash = try DeviceSecurity.stateHash(head)
        let ordered = record.states.values.sorted { $0.core.epoch < $1.core.epoch }
        var ev = PBSecurityRecordEvent()
        ev.opID = opId
        if pushSeq != 0 { ev.pushSeq = pushSeq }
        ev.networkGeneration = networkGeneration
        ev.account = record.pin.account
        ev.domain = record.pin.domain
        ev.genesis = record.pin.genesis
        ev.floorMs = Int64(bitPattern: record.floor)
        ev.recoveryPending = recoveryPending
        ev.recoveryReadyMs = Int64(bitPattern: recoveryReadyMs)
        ev.recoveryObjectsPending = recoveryObjectsPending
        ev.states = try ordered.map { try DeviceSecurity.stateBytes($0) }
        ev.recoveryProofs = try ordered.map { try record.recovery[$0.core.epoch]?.bytes() ?? Data() }
        if let lease = record.lease, lease.head == headHash {
            ev.lease = try lease.bytes()
        }
        let headContentEpoch = head.core.contentEpoch
        for (ep, sec) in record.contentKeys.sorted(by: { $0.key < $1.key }) {
            if ep > 0 && ep < headContentEpoch && sec.count == keySize {
                ev.contentKeys.append(.with {
                    $0.contentEpoch = ep
                    $0.secret = sec
                })
            }
        }
        return ev
    }

    static func openDeviceMeta(
        record: DeviceSecurity.Record,
        envelope: PBEnvelope,
        deviceId: String,
        clock: @Sendable () -> Int64
    ) throws -> (header: PBSecurityOpenEnvelopeResult, meta: PBDeviceMeta) {
        let runner = try CoreSecurityRunner(executor: SecurityExecutor(signer: { nil }, xPrivate: { record.xPrivateRaw }))
        defer { runner.close() }
        try runner.syncRecord(record, nowMs: clock())
        let res = try runner.runOp {
            $0.openEnvelope = .with {
                $0.envelope = envelope
                $0.expectedPurpose = DeviceSecurity.Purpose.deviceMeta.rawValue
                $0.requestID = deviceId
                $0.history = true
            }
        }
        let meta = try PBDeviceMeta(serializedBytes: res.opened.plaintext)
        return (res.opened, meta)
    }

    static func sealDeviceMeta(
        record: DeviceSecurity.Record,
        account: Account,
        seq: UInt64,
        plaintext: Data,
        gate: AuthorityGate,
        ticket: UInt64,
        clock: @Sendable () -> Int64
    ) throws -> PBEnvelope {
        let exec = SecurityExecutor(
            signer: {
                try? gate.withPermit(ticket) {
                    try SigningKey(seed: account.deviceSeed)
                }
            },
            xPrivate: { record.xPrivateRaw }
        )
        let runner = try CoreSecurityRunner(executor: exec)
        defer { runner.close() }
        try runner.syncRecord(record, nowMs: clock())
        let res = try runner.runOp {
            $0.sealEnvelope = .with {
                $0.purpose = DeviceSecurity.Purpose.deviceMeta.rawValue
                $0.requestID = account.deviceId
                $0.seq = seq
                $0.plaintext = plaintext
            }
        }
        return res.sealed.envelope
    }

    private static func buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int) -> PBCoreEntropyProvided {
        var ev = PBCoreEntropyProvided()
        if minIds > 0 {
            ev.randomIds = (0..<minIds).map { _ in newRequestId() }
        }
        if minFloats > 0 {
            ev.unitFloats = (0..<minFloats).map { _ in Double.random(in: 0..<1) }
        }
        if minRandomBytes > 0 {
            var data = Data(count: minRandomBytes)
            let rc = data.withUnsafeMutableBytes { buf in
                SecRandomCopyBytes(kSecRandomDefault, minRandomBytes, buf.baseAddress!)
            }
            if rc == errSecSuccess {
                ev.randomBytes = data
            } else {
                ev.randomBytes = randomBytes(minRandomBytes)
            }
        }
        return ev
    }

    private static func localCalendar(nowMs: Int64) -> PBCoreLocalCalendar {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .current
        cal.firstWeekday = 2
        let now = Date(timeIntervalSince1970: Double(nowMs) / 1000)
        let today = cal.startOfDay(for: now)
        let yesterday = cal.date(byAdding: .day, value: -1, to: today) ?? today
        let weekComponents = cal.dateComponents([.yearForWeekOfYear, .weekOfYear], from: today)
        let weekStart = cal.date(from: weekComponents) ?? today
        return PBCoreLocalCalendar.with {
            $0.nowMs = nowMs
            $0.todayStartMs = Int64(today.timeIntervalSince1970 * 1000)
            $0.yesterdayStartMs = Int64(yesterday.timeIntervalSince1970 * 1000)
            $0.weekStartMs = Int64(weekStart.timeIntervalSince1970 * 1000)
            $0.nowYear = Int32(cal.component(.year, from: now))
        }
    }
}
