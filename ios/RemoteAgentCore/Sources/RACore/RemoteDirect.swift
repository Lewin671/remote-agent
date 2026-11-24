import CryptoKit
import Foundation
import RAProto

/// Weak holder so executor callbacks created before actor initialization finish never leak `Remote`.
final class WeakRemote: @unchecked Sendable {
    private let lock = NSLock()
    private weak var stored: Remote?
    var remote: Remote? { get { lock.withLock { stored } } set { lock.withLock { stored = newValue } } }
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
    public nonisolated func loadDirectPreferences() -> [String: Bool] {
        directPreferences.allEnabled(server: account.server, deviceId: account.deviceId)
    }

    public nonisolated func persistDirectPreference(_ machineId: String, enabled: Bool) {
        try? directPreferences.setEnabled(server: account.server, deviceId: account.deviceId, machineId: machineId, enabled: enabled)
    }

    public nonisolated func detachDirectStreams() {
        directExecutor.detachAll()
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

    public nonisolated func handleDirectPcSendProbe(_ eff: PBCoreDirectPcSendProbeEffect) {
        directExecutor.pcSendProbe(eff)
    }

    public nonisolated func handleTerminalDirectSend(_ eff: PBCoreTerminalDirectSendEffect) {
        if eff.sendRelay {
            activeTerminals.sendRelay(streamId: eff.streamID, raw: eff.sealedEnvelope)
        }
        if eff.directPcID > 0 {
            directExecutor.sendTerminalFrame(pcId: eff.directPcID, raw: eff.sealedEnvelope)
        }
    }

    public nonisolated func handleTerminalDirectDeliver(_ eff: PBCoreTerminalDirectDeliverEffect) {
        activeTerminals.deliver(streamId: eff.streamID, payloads: eff.payloads)
    }

    public nonisolated func handleTerminalDirectAttachRelay(_ eff: PBCoreTerminalDirectAttachRelayEffect) {
        activeTerminals.attachRelay(streamId: eff.streamID, legId: eff.legID)
    }

    public nonisolated func handleTerminalDirectState(_ eff: PBCoreTerminalDirectStateEffect) {
        activeTerminals.onState(streamId: eff.streamID, state: eff.state, reason: eff.reason, needsConfirmation: eff.needsConfirmation)
    }

    public func handleDirectDcReadStats(_ eff: PBCoreDirectDcReadStatsEffect) async {
        await directExecutor.dcReadStats(eff)
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
        var event = PBCoreEvent()
        event.directEventsReceived = .with {
            $0.machineID = machineId
            $0.sessionID = sid
            $0.pcID = pcId
            $0.sender = machineId
            $0.wireEnvelopes = [envelope]
        }
        await emitCoreEvent(event)
    }
}
