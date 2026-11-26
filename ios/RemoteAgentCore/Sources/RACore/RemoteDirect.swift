import CryptoKit
import Foundation
import RAProto

/// Weak holder so executor callbacks created before actor initialization finish never leak `Remote`.
final class WeakRemote: @unchecked Sendable {
    private let lock = NSLock()
    private weak var stored: Remote?
    var remote: Remote? { get { lock.withLock { stored } } set { lock.withLock { stored = newValue } } }
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
