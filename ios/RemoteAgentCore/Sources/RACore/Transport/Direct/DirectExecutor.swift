import Foundation
import RAProto
import SwiftProtobuf

private func decodeStrictUTF8(_ bytes: Data) -> String? {
    String(data: bytes, encoding: .utf8)
}

/// Platform executor for Core's direct-path coordinator: owns WebRTC PCs,
/// leaf Ed25519/X25519 operations, and main-WS signaling framing.
/// Bulk AES-GCM and batch signature verification stay outside Core.
final class DirectExecutor: @unchecked Sendable {
    private final class PcEntry: @unchecked Sendable {
        let machineId: String
        let use: Int
        var confirmed = false
        var closedByCore = false
        var channel: DirectChannel?

        init(machineId: String, use: Int) {
            self.machineId = machineId
            self.use = use
        }
    }

    private final class PendingRpc: @unchecked Sendable {
        let pcId: UInt64
        let machineId: String
        private let lock = NSLock()
        private var continuation: CheckedContinuation<PBEnvelope, Error>?
        private var settled: Result<PBEnvelope, Error>?

        init(pcId: UInt64, machineId: String) {
            self.pcId = pcId
            self.machineId = machineId
        }

        func install(_ c: CheckedContinuation<PBEnvelope, Error>) {
            lock.lock()
            if let s = settled {
                lock.unlock()
                c.resume(with: s)
            } else {
                continuation = c
                lock.unlock()
            }
        }

        func resolve(_ res: Result<PBEnvelope, Error>) {
            lock.lock()
            guard settled == nil else { lock.unlock(); return }
            settled = res
            let c = continuation
            continuation = nil
            lock.unlock()
            c?.resume(with: res)
        }
    }

    private let emit: @Sendable (PBCoreEvent) -> Void
    private let onPushedEvent: @Sendable (_ machineId: String, _ pcId: UInt64, _ env: PBEnvelope, _ header: PBHeader, _ size: Int) -> Void
    private let factory: (any RtcFactory)?
    private let clock: @Sendable () -> UInt64
    private let monotonic: @Sendable () -> Double

    let budget = DirectBudget()
    private let lock = NSLock()
    private var pcs: [UInt64: PcEntry] = [:]
    private var pendingRpc: [String: PendingRpc] = [:]
    private var leg: (any FrameStream)?
    private var closed = false

    init(
        emit: @escaping @Sendable (PBCoreEvent) -> Void,
        onPushedEvent: @escaping @Sendable (_ machineId: String, _ pcId: UInt64, _ env: PBEnvelope, _ header: PBHeader, _ size: Int) -> Void,
        factory: (any RtcFactory)? = nil,
        clock: @escaping @Sendable () -> UInt64 = { UInt64(Date().timeIntervalSince1970 * 1000) },
        monotonic: @escaping @Sendable () -> Double = { Double(DispatchTime.now().uptimeNanoseconds) / 1e6 }
    ) {
        self.emit = emit
        self.onPushedEvent = onPushedEvent
        self.factory = factory
        self.clock = clock
        self.monotonic = monotonic
    }

    private func emitEvent(_ build: (inout PBCoreEvent) -> Void) {
        if lock.withLock({ closed }) { return }
        var ev = PBCoreEvent()
        build(&ev)
        emit(ev)
    }

    func attachLeg(_ stream: any FrameStream) {
        lock.withLock { leg = stream }
    }

    func detachLeg(_ stream: any FrameStream) {
        lock.withLock {
            if leg === stream { leg = nil }
        }
    }

    func detachAll() {
        lock.withLock { leg = nil }
    }

    @discardableResult
    func signalSend(_ eff: PBCoreDirectSignalSendEffect) -> Bool {
        let stream: (any FrameStream)? = lock.withLock { closed ? nil : leg }
        guard let stream else { return false }
        var frame = PBPeerFrame()
        frame.directedPeerFrame = .with {
            $0.toDevice = eff.toDevice
            $0.frameClass = PBFrameClass(rawValue: Int(eff.frameClass)) ?? .unspecified
            $0.frameID = eff.frameID
            $0.sealed = eff.sealed
        }
        guard let raw = try? frame.serializedData() else { return false }
        stream.send(raw)
        return true
    }

    func pcCreate(_ eff: PBCoreDirectPcCreateEffect) {
        guard let rtc = factory, !lock.withLock({ closed }) else {
            emitEvent {
                $0.directPcCreated = .with { $0.pcID = eff.pcID; $0.ok = false }
            }
            return
        }
        let prev = lock.withLock { () -> PcEntry? in
            let p = pcs.removeValue(forKey: eff.pcID)
            p?.closedByCore = true
            return p
        }
        if prev != nil {
            rejectPending(for: eff.pcID)
            prev?.channel?.close()
        }
        let entry = PcEntry(machineId: eff.machineID, use: Int(eff.use))
        // Register before constructing DirectChannel so synchronous SDK callbacks never miss the entry.
        lock.withLock { pcs[eff.pcID] = entry }
        do {
            let pcID = eff.pcID
            let ch = try DirectChannel(DirectChannelOptions(
                peer: eff.machineID,
                roundId: eff.roundID,
                use: Int(eff.use),
                generation: eff.generation,
                budget: budget,
                valid: { [weak self, weak entry] in
                    guard let self, let entry else { return false }
                    return self.lock.withLock { !self.closed && self.pcs[pcID] === entry }
                },
                stun: eff.stunServers,
                factory: rtc,
                candidate: { [weak self, weak entry] cand in
                    guard let self, let entry, self.lock.withLock({ !self.closed && self.pcs[pcID] === entry }) else { return }
                    self.emitEvent {
                        $0.directPcLocalCandidate = .with {
                            $0.pcID = pcID
                            if let cand {
                                $0.candidate = cand.candidate
                            } else {
                                $0.endOfCandidates = true
                            }
                        }
                    }
                },
                state: { [weak self, weak entry] kind, value in
                    guard let self, let entry, self.lock.withLock({ !self.closed && self.pcs[pcID] === entry }) else { return }
                    self.emitEvent {
                        $0.directPcState = .with {
                            $0.pcID = pcID
                            $0.kind = kind
                            $0.value = value
                        }
                    }
                },
                dcState: { [weak self, weak entry] open in
                    guard let self, let entry, self.lock.withLock({ !self.closed && self.pcs[pcID] === entry }) else { return }
                    self.emitEvent {
                        $0.directDcState = .with {
                            $0.pcID = pcID
                            $0.open = open
                        }
                    }
                },
                stunError: { [weak self, weak entry] code, address in
                    guard let self, let entry, self.lock.withLock({ !self.closed && self.pcs[pcID] === entry }) else { return }
                    self.emitEvent {
                        $0.directPcStunError = .with {
                            $0.pcID = pcID
                            $0.code = Int32(code)
                            $0.address = address
                        }
                    }
                },
                frame: { [weak self, weak entry] raw in
                    guard let entry else { return }
                    self?.handleDcFrame(pcId: pcID, entry: entry, raw: raw)
                },
                closed: { [weak self, weak entry] in
                    guard let self, let entry else { return }
                    let closedByCore = self.lock.withLock { () -> Bool in
                        if self.pcs[pcID] === entry { self.pcs.removeValue(forKey: pcID) }
                        return entry.closedByCore
                    }
                    self.rejectPending(for: pcID)
                    if !closedByCore {
                        self.emitEvent {
                            $0.directPcClosed = .with { $0.pcID = pcID }
                        }
                    }
                },
                clock: clock,
                monotonic: monotonic
            ))
            let stillActive = lock.withLock { () -> Bool in
                guard !closed, pcs[eff.pcID] === entry else { return false }
                entry.channel = ch
                return true
            }
            guard stillActive else {
                entry.closedByCore = true
                ch.close()
                emitEvent { $0.directPcCreated = .with { $0.pcID = eff.pcID; $0.ok = false } }
                return
            }
            emitEvent {
                $0.directPcCreated = .with { $0.pcID = eff.pcID; $0.ok = true }
            }
        } catch {
            lock.withLock {
                if pcs[eff.pcID] === entry { pcs.removeValue(forKey: eff.pcID) }
            }
            emitEvent {
                $0.directPcCreated = .with { $0.pcID = eff.pcID; $0.ok = false }
            }
        }
    }

    func pcDescribe(_ eff: PBCoreDirectPcDescribeEffect) async {
        let ch = lock.withLock { pcs[eff.pcID]?.channel }
        guard let ch else {
            emitEvent {
                $0.directPcDescription = .with { $0.pcID = eff.pcID; $0.ok = false }
            }
            return
        }
        do {
            let sdp = try await ch.description(offer: eff.offer, restart: eff.iceRestart)
            emitEvent {
                $0.directPcDescription = .with {
                    $0.pcID = eff.pcID
                    $0.sdp = sdp
                    $0.ok = true
                }
            }
        } catch is CancellationError {
        } catch {
            emitEvent {
                $0.directPcDescription = .with { $0.pcID = eff.pcID; $0.ok = false }
            }
        }
    }

    func pcSetRemote(_ eff: PBCoreDirectPcSetRemoteEffect) async {
        let ch = lock.withLock { pcs[eff.pcID]?.channel }
        guard let ch else {
            emitEvent {
                $0.directPcRemoteSet = .with { $0.pcID = eff.pcID; $0.ok = false; $0.error = "closed" }
            }
            return
        }
        do {
            try await ch.remote(eff.sdp, offer: eff.offer)
            emitEvent {
                $0.directPcRemoteSet = .with { $0.pcID = eff.pcID; $0.ok = true }
            }
        } catch is CancellationError {
        } catch {
            emitEvent {
                $0.directPcRemoteSet = .with {
                    $0.pcID = eff.pcID
                    $0.ok = false
                    $0.error = String(describing: type(of: error))
                }
            }
        }
    }

    func pcAddCandidates(_ eff: PBCoreDirectPcAddCandidatesEffect) async {
        let ch = lock.withLock { pcs[eff.pcID]?.channel }
        guard let ch else {
            emitEvent {
                $0.directPcCandidatesAdded = .with { $0.pcID = eff.pcID; $0.ok = false; $0.error = "closed" }
            }
            return
        }
        do {
            for c in eff.candidates {
                guard let line = decodeStrictUTF8(c.line),
                      let ufrag = c.ufrag.isEmpty ? "" : decodeStrictUTF8(c.ufrag),
                      let mid = c.mid.isEmpty ? "" : decodeStrictUTF8(c.mid) else {
                    throw PathChanged()
                }
                try await ch.addCandidateLine(line, mid: mid, mlineIndex: Int(c.mlineIndex), ufrag: ufrag)
            }
            emitEvent {
                $0.directPcCandidatesAdded = .with { $0.pcID = eff.pcID; $0.ok = true }
            }
        } catch is CancellationError {
        } catch {
            emitEvent {
                $0.directPcCandidatesAdded = .with {
                    $0.pcID = eff.pcID
                    $0.ok = false
                    $0.error = String(describing: type(of: error))
                }
            }
        }
    }

    func pcConfirm(_ eff: PBCoreDirectPcConfirmEffect) {
        let ch: DirectChannel? = lock.withLock {
            guard let entry = pcs[eff.pcID],
                  entry.machineId == eff.machineID,
                  entry.use == Int(eff.use) else { return nil }
            entry.confirmed = true
            return entry.channel
        }
        ch?.confirm()
    }

    func pcClose(_ eff: PBCoreDirectPcCloseEffect) {
        let entry = lock.withLock { () -> PcEntry? in
            let removed = pcs.removeValue(forKey: eff.pcID)
            removed?.closedByCore = true
            return removed
        }
        rejectPending(for: eff.pcID)
        entry?.channel?.close()
    }

    func dcReadStats(_ eff: PBCoreDirectDcReadStatsEffect) async {
        let ch = lock.withLock { pcs[eff.pcID]?.channel }
        let report = if let ch { await ch.readStats() } else { PBCoreDirectStatsReport() }
        emitEvent {
            $0.directDcStats = .with {
                $0.pcID = eff.pcID
                $0.report = report
            }
        }
    }

    func sendRpc(
        pcId: UInt64,
        requestId: String,
        sealedBytes: Data,
        timeoutMs: Int64,
        handoff: @escaping @Sendable () throws -> Void
    ) async throws -> PBEnvelope {
        let (entry, ch) = lock.withLock { () -> (PcEntry?, DirectChannel?) in
            let e = pcs[pcId]
            return (e, e?.channel)
        }
        guard let entry, let ch,
              lock.withLock({ entry.confirmed }) && ch.ready else {
            throw PathChanged()
        }
        let rpcKey = "\(pcId):\(requestId)"
        let pending = PendingRpc(pcId: pcId, machineId: entry.machineId)
        let inserted = lock.withLock { () -> Bool in
            guard !closed, pendingRpc.count < 128, pendingRpc[rpcKey] == nil else { return false }
            pendingRpc[rpcKey] = pending
            return true
        }
        guard inserted else { throw PathChanged() }

        let boundedMs = timeoutMs > 0 ? min(max(timeoutMs, 1), 30_000) : 3_000
        let timeoutTask = Task {
            try? await Task.sleep(nanoseconds: UInt64(boundedMs) * 1_000_000)
            pending.resolve(.failure(PathChanged()))
        }
        defer {
            timeoutTask.cancel()
            lock.withLock {
                if pendingRpc[rpcKey] === pending { pendingRpc.removeValue(forKey: rpcKey) }
            }
        }

        async let responseWait: PBEnvelope = withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { c in
                pending.install(c)
            }
        } onCancel: {
            pending.resolve(.failure(CancellationError()))
        }

        do {
            try await ch.write(sealedBytes, handoff: handoff)
            return try await responseWait
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            pending.resolve(.failure(PathChanged()))
            _ = try? await responseWait
            throw PathChanged()
        }
    }

    private func handleDcFrame(pcId: UInt64, entry: PcEntry, raw: Data) {
        let confirmed = lock.withLock { () -> Bool in
            guard !closed, pcs[pcId] === entry else { return false }
            return entry.confirmed
        }
        guard let env = try? PBEnvelope(serializedBytes: raw),
              let header = try? PBHeader(serializedBytes: env.header) else { return }
        if entry.use == 1 && header.kind == .event {
            if confirmed && header.machineID == entry.machineId && header.sender == entry.machineId {
                onPushedEvent(entry.machineId, pcId, env, header, raw.count)
            }
            return
        }
        let rpcKey = "\(pcId):\(header.requestID)"
        let pending = lock.withLock { () -> PendingRpc? in
            guard let p = pendingRpc[rpcKey], p.machineId == entry.machineId, header.machineID == entry.machineId else { return nil }
            pendingRpc.removeValue(forKey: rpcKey)
            return p
        }
        pending?.resolve(.success(env))
    }

    private func rejectPending(for pcId: UInt64) {
        let list = lock.withLock { () -> [PendingRpc] in
            var matched: [PendingRpc] = []
            for (k, v) in pendingRpc where v.pcId == pcId {
                matched.append(v)
                pendingRpc.removeValue(forKey: k)
            }
            return matched
        }
        for p in list { p.resolve(.failure(PathChanged())) }
    }

    func close() {
        let (channels, pendings) = lock.withLock { () -> ([PcEntry], [PendingRpc]) in
            if closed { return ([], []) }
            closed = true
            leg = nil
            let c = Array(pcs.values)
            pcs.removeAll()
            for entry in c { entry.closedByCore = true }
            let p = Array(pendingRpc.values)
            pendingRpc.removeAll()
            return (c, p)
        }
        for entry in channels { entry.channel?.close() }
        for p in pendings { p.resolve(.failure(PathChanged())) }
    }
}
