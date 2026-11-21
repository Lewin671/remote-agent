import CryptoKit
import Foundation
import RAProto

/// Weak holder so executor callbacks created before actor initialization finish never leak `Remote`.
final class WeakRemote: @unchecked Sendable {
    private let lock = NSLock()
    private weak var stored: Remote?
    var remote: Remote? { get { lock.withLock { stored } } set { lock.withLock { stored = newValue } } }
}

enum UploadKind: Equatable, Sendable {
    case image
    case file
}

struct DirectBulkHint: Sendable {
    var routeHintValue: UInt32 = 0
    var bulkFileKey: String = ""
    var bulkBytes: Int64 = 0
    var bulkUpload: Bool = false
    var uploadTransferID: String = ""
    var uploadHasFilename: Bool = false
    var finishTransferID: String = ""
}

/// Extracts raw bytes for a top-level length-delimited `PBServerFrame` field (`11` = delivery, `12` = result)
/// without re-serializing, so Core's strict peerwire parser sees the exact incoming wire bytes.
func peerFrameBody(_ raw: Data, targetField: UInt64) -> Data? {
    var idx = raw.startIndex
    func readVarint() -> UInt64? {
        var value: UInt64 = 0
        var shift: UInt64 = 0
        for _ in 0..<10 {
            guard idx < raw.endIndex else { return nil }
            let b = raw[idx]
            idx = raw.index(after: idx)
            value |= UInt64(b & 0x7F) << shift
            if b & 0x80 == 0 { return value }
            shift += 7
        }
        return nil
    }
    while idx < raw.endIndex {
        guard let tag = readVarint() else { return nil }
        let field = tag >> 3
        let wire = tag & 7
        guard field > 0 else { return nil }
        switch wire {
        case 0:
            guard readVarint() != nil else { return nil }
        case 1:
            guard raw.distance(from: idx, to: raw.endIndex) >= 8 else { return nil }
            idx = raw.index(idx, offsetBy: 8)
        case 2:
            guard let len = readVarint(), len <= UInt64(raw.distance(from: idx, to: raw.endIndex)) else { return nil }
            let end = raw.index(idx, offsetBy: Int(len))
            if field == targetField {
                return Data(raw[idx..<end])
            }
            idx = end
        case 5:
            guard raw.distance(from: idx, to: raw.endIndex) >= 4 else { return nil }
            idx = raw.index(idx, offsetBy: 4)
        default:
            return nil
        }
    }
    return nil
}

extension Remote {
    func securityObserver() -> SyncObserver {
        SyncObserver(
            synced: { [weak self] record, started in
                await self?.emitDirectSecuritySnapshot(record, started: started)
                await self?.pushObservedSecuritySnapshot(record)
            },
            failed: { [weak self] in
                self?.rejectDirectSecurity()
            },
            installed: { [weak self] record in
                Task {
                    await self?.handleDirectSecurityInstall(record)
                    await self?.pushObservedSecuritySnapshot(record)
                }
            }
        )
    }

    public nonisolated func cachedDirectSecuritySnapshot() -> PBCoreDirectSecuritySnapshotEvent? {
        directSecurityLock.withLock { $0.lastSnapshot }
    }

    public nonisolated func loadDirectPreferences() -> [String: Bool] {
        directPreferences.allEnabled(server: account.server, deviceId: account.deviceId)
    }

    public nonisolated func persistDirectPreference(_ machineId: String, enabled: Bool) {
        try? directPreferences.setEnabled(server: account.server, deviceId: account.deviceId, machineId: machineId, enabled: enabled)
    }

    public func authorizeDirect() {
        guard !closed else { return }
        Task {
            var ok = false
            do {
                let record = try await securitySession().sync()
                ok = record.member
            } catch {
                ok = false
            }
            if !closed {
                var ev = PBCoreEvent()
                ev.directAuthorizeDone = .with { $0.ok = ok }
                await emitCoreEvent(ev)
            }
        }
    }

    public nonisolated func detachDirectStreams() {
        directExecutor.detachAll()
    }

    public nonisolated func handleDirectKeySign(_ eff: PBCoreDirectKeySignEffect) {
        directExecutor.keySign(eff)
    }

    public nonisolated func handleDirectKeyAgree(_ eff: PBCoreDirectKeyAgreeEffect) {
        directExecutor.keyAgree(eff)
    }

    @discardableResult
    public nonisolated func handleDirectSignalSend(_ eff: PBCoreDirectSignalSendEffect) -> Bool {
        directExecutor.signalSend(eff)
    }

    public nonisolated func handleDirectPcCreate(_ eff: PBCoreDirectPcCreateEffect) {
        directExecutor.pcCreate(eff)
    }

    public func handleDirectPcDescribe(_ eff: PBCoreDirectPcDescribeEffect) async {
        await directExecutor.pcDescribe(eff)
    }

    public func handleDirectPcSetRemote(_ eff: PBCoreDirectPcSetRemoteEffect) async {
        await directExecutor.pcSetRemote(eff)
    }

    public func handleDirectPcAddCandidates(_ eff: PBCoreDirectPcAddCandidatesEffect) async {
        await directExecutor.pcAddCandidates(eff)
    }

    public nonisolated func handleDirectPcConfirm(_ eff: PBCoreDirectPcConfirmEffect) {
        directExecutor.pcConfirm(eff)
    }

    public nonisolated func handleDirectPcClose(_ eff: PBCoreDirectPcCloseEffect) {
        directExecutor.pcClose(eff)
    }

    public func handleDirectDcReadStats(_ eff: PBCoreDirectDcReadStatsEffect) async {
        await directExecutor.dcReadStats(eff)
    }

    nonisolated func rejectDirectSecurity() {
        let rejected = PBCoreDirectSecuritySnapshotEvent.with { $0.rejected = true }
        directSecurityLock.withLock { state in
            state.rev &+= 1
            state.xPrivateRaw = nil
            state.lastSnapshot = rejected
        }
        var ev = PBCoreEvent()
        ev.directSecuritySnapshot = rejected
        Task { await self.emitCoreEvent(ev) }
    }

    func handleDirectSecurityInstall(_ record: DeviceSecurity.Record) async {
        let headHex = (try? DeviceSecurity.stateHash(record.state).hex) ?? ""
        directSecurityLock.withLock { state in
            let prev = state.lastSnapshot
            let changed = !record.member || (prev != nil && !(prev?.rejected ?? true) && (record.pin.account != prev?.account || headHex != prev?.head))
            if changed {
                state.rev &+= 1
                state.xPrivateRaw = nil
                state.lastSnapshot = PBCoreDirectSecuritySnapshotEvent.with { $0.rejected = true }
            } else if record.member, let xKey = try? record.xKey {
                state.xPrivateRaw = xKey.rawRepresentation
            }
        }
        guard !closed else { return }
        var ev = PBCoreEvent()
        ev.directSecurityInstalled = .with {
            $0.member = record.member
            $0.account = record.pin.account
            $0.head = headHex
        }
        await emitCoreEvent(ev)
    }

    func emitDirectSecuritySnapshot(_ record: DeviceSecurity.Record, started: UInt64) async {
        let rev = directSecurityLock.withLock { state -> UInt64 in
            state.rev &+= 1
            return state.rev
        }
        let nowMs = clock()
        let builtSnapshot = Self.buildDirectSecuritySnapshot(
            record: record,
            expectedDeviceID: account.deviceId,
            devicePublicKey: device.publicKey,
            started: started,
            nowMs: nowMs
        )
        let out = builtSnapshot?.snapshot ?? PBCoreDirectSecuritySnapshotEvent.with { $0.rejected = true }
        let committed = directSecurityLock.withLock { state -> Bool in
            guard state.rev == rev else { return false }
            state.xPrivateRaw = builtSnapshot?.xPrivateRaw
            state.lastSnapshot = out
            return true
        }
        guard committed, !closed else { return }
        var ev = PBCoreEvent()
        ev.directSecuritySnapshot = out
        await hostHooks?.onCoreEvent(ev)
    }

    static func buildDirectSecuritySnapshot(
        record: DeviceSecurity.Record,
        expectedDeviceID: String,
        devicePublicKey: Data,
        started: UInt64,
        nowMs: Int64
    ) -> (snapshot: PBCoreDirectSecuritySnapshotEvent, xPrivateRaw: Data)? {
        let startedMs = Int64(min(started, UInt64(Int64.max)))
        do {
            let head = record.state
            let at = max(UInt64(max(0, nowMs)), record.floor)
            guard record.member,
                  record.memberID == expectedDeviceID,
                  let lease = record.lease,
                  nowMs < startedMs + 20_000,
                  let selfMember = head.core.member(record.memberID) else {
                return nil
            }
            try lease.valid(head, now: at)
            let xKey = try record.xKey
            let xPub = xKey.publicKey.rawRepresentation
            guard devicePublicKey == selfMember.ed, xPub == selfMember.x else {
                return nil
            }
            let stateHash = try DeviceSecurity.stateHash(head)
            let snap = PBCoreDirectSecuritySnapshotEvent.with {
                $0.member = true
                $0.account = record.pin.account
                $0.domain = record.pin.domain
                $0.head = stateHash.hex
                $0.headHash = stateHash
                $0.epoch = head.core.epoch
                $0.startedAtMs = startedMs
                $0.leaseNotAfterMs = Int64(min(lease.notAfter, UInt64(Int64.max)))
                $0.selfID = selfMember.id
                $0.selfEdPublic = selfMember.ed
                $0.selfXPublic = selfMember.x
                $0.members = head.core.members.map { m in
                    PBCoreDirectSecurityMember.with {
                        $0.deviceID = m.id
                        $0.edPublic = m.ed
                        $0.xPublic = m.x
                        $0.isSelf = (m.id == record.memberID)
                        $0.kind = UInt32(m.kind)
                    }
                }
            }
            return (snap, xKey.rawRepresentation)
        } catch {
            return nil
        }
    }

    func handleDirectEvent(
        machineId: String,
        pcId: UInt64,
        envelope: PBEnvelope,
        header: PBHeader,
        size: Int
    ) async {
        guard !closed else { return }
        let sid = header.sessionID
        guard !sid.isEmpty, header.seq > 0, watchedSessionIds.contains(sid) else { return }
        guard sessionMachines[sid] == machineId, header.sender == machineId else { return }
        guard let seed = sessionKeys[sid], !seed.isEmpty else { return }
        do {
            let (h, ev) = try await openSecureMessage(
                envelope: envelope,
                want: Place(.event, machineId: machineId, sessionId: sid, seq: header.seq),
                as: PBEvent.self,
                history: false
            )
            guard !closed, h.sender == machineId, ev.seq == header.seq, ev.sessionID == sid else { return }
            let plain = PBCorePlainEvent.with {
                $0.seq = header.seq
                $0.event = ev
                $0.verified = true
                $0.approxBytes = Int64(max(size, 64))
            }
            var event = PBCoreEvent()
            event.directEventsReceived = .with {
                $0.machineID = machineId
                $0.sessionID = sid
                $0.pcID = pcId
                $0.sender = machineId
                $0.events = [plain]
            }
            if let hooks = hostHooks {
                await hooks.onCoreEvent(event)
            }
        } catch {
            // Direct push is best-effort; drop errors silently.
        }
    }

    func directBulkHint(_ machineId: String, _ cmd: PBCommand) -> DirectBulkHint {
        var hint = DirectBulkHint()
        switch cmd.kind {
        case .beginImageUpload(let c)?:
            hint.uploadTransferID = c.transferID
            hint.uploadHasFilename = !c.fileName.isEmpty
        case .putImageChunk(let c)?:
            if directTransfers["\(machineId)/\(c.transferID)"] != nil {
                hint.routeHintValue = UInt32(PBCoreCommandRouteHint.bulk.rawValue)
                hint.bulkFileKey = c.transferID
                hint.bulkBytes = Int64(c.data.count)
                hint.bulkUpload = true
                hint.uploadTransferID = c.transferID
                hint.uploadHasFilename = (directTransfers["\(machineId)/\(c.transferID)"] == .file)
            }
        case .getSharedFileChunk(let c)?:
            hint.routeHintValue = UInt32(PBCoreCommandRouteHint.bulk.rawValue)
            hint.bulkFileKey = "\(c.sessionID)/\(c.fileID)"
        case .completeImageUpload(let c)?:
            hint.finishTransferID = c.transferID
        case .abortImageUpload(let c)?:
            hint.finishTransferID = c.transferID
        case .getImageUploadStatus?:
            break
        default:
            if cmd.isReadOnlyCommand {
                hint.routeHintValue = UInt32(PBCoreCommandRouteHint.read.rawValue)
            }
        }
        return hint
    }

    func trackTransfer(machineId: String, command cmd: PBCommand) {
        switch cmd.kind {
        case .beginImageUpload(let c)?:
            if directTransfers.count >= 128, let first = directTransfers.keys.first {
                directTransfers.removeValue(forKey: first)
            }
            directTransfers["\(machineId)/\(c.transferID)"] = c.fileName.isEmpty ? .image : .file
        case .completeImageUpload(let c)?:
            directTransfers.removeValue(forKey: "\(machineId)/\(c.transferID)")
        case .abortImageUpload(let c)?:
            directTransfers.removeValue(forKey: "\(machineId)/\(c.transferID)")
        default:
            break
        }
    }
}
