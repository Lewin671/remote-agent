// Transitional adapter from Core's SyncStateView into the legacy shell model (`RemoteState`).
//
// Delete this file once remaining shell views migrate off `RemoteState`.
import Foundation
import RACore
import RAProto

enum SyncStateAdapter {
    static func toRemoteState(_ v: PBCoreSyncStateView) -> RemoteState {
        var s = RemoteState()
        switch v.connection {
        case .connected:
            s.conn = .online
            s.link = .online
        case .connecting, .reconnecting:
            s.conn = .connecting
            s.link = .waiting
        case .noNetwork:
            s.conn = .offline
            s.link = .noNetwork
        case .offline:
            s.conn = .offline
            s.link = .noServer
        case .unspecified, .UNRECOGNIZED:
            s.conn = .connecting
            s.link = .waiting
        }
        s.offlineSinceMs = v.disconnectedAtMs
        s.synced = v.machinesListed && v.sessionsListed
        s.loaded = v.sessionsListed || !v.sessions.isEmpty || !v.machines.isEmpty
        s.authorityDeviceId = v.authorityDeviceID
        s.leaseEndMs = UInt64(max(0, v.leaseEndMs))
        s.recoveryPending = v.recoveryPending
        s.recoveryReadyMs = UInt64(max(0, v.recoveryReadyMs))
        s.recoveryStatusStale = v.recoveryStatusStale
        s.removed = v.removed
        s.clockWarning = v.accountClockSkewed ? L10n.errorsIosThisDeviceIsOver5MinutesBehindTheSecurity() : ""
        s.displayGrace = v.displayGrace
        s.reachabilityUnknown = v.reachabilityUnknown
        if v.hasConnectionReason {
            let text = renderCoreText(v.connectionReason)
            s.error = text
            s.errorDetail = text
        }
        if v.hasPrefs || v.prefsLoaded {
            s.prefs = PrefsObject(
                revision: v.prefsRevision,
                prefs: v.hasPrefs ? v.prefs : PBProjectPrefs()
            )
        }

        var machines: [String: MachineView] = [:]
        var currentMachines = Set<String>()
        for m in v.machines {
            let meta = m.hasMeta ? m.meta : nil
            var ps = PeerState()
            ps.reachable = m.online
            ps.ready = (v.connection == .connected)
            ps.authorized = !v.removed
            ps.unlinked = m.unlinked || v.unlinked
            ps.protocolVersion = meta?.protocolVersion
            ps.capabilities = meta.flatMap { $0.hasCapabilities ? $0.capabilities : nil }
            ps.relayRequired = !m.directReady
            let mv = MachineView(
                id: m.machineID,
                online: m.online,
                lastSeenMs: m.lastSeenMs,
                meta: meta,
                metaRev: m.metaRev,
                linkState: ps
            )
            machines[m.machineID] = mv
            if !m.unlinked {
                currentMachines.insert(m.machineID)
            }
        }
        s.machines = machines
        s.currentMachines = currentMachines

        var sessions: [String: SessionView] = [:]
        for item in v.sessions {
            var sv = SessionView(
                id: item.sessionID,
                machineId: item.machineID,
                info: item.hasInfo ? item.info : nil,
                metaRev: item.metaRev,
                lastSeq: item.lastSeq,
                updatedMs: item.updatedMs,
                listedEndedMs: item.listedEndedMs
            )
            if item.hasCheckpoint {
                sv.checkpoint = item.checkpoint
                sv.checkpointStatus = "ready"
            } else {
                sv.checkpointStatus = "waiting"
            }
            if item.retiredHistory {
                sv.historyError = L10n.errorsThisHistoryUsesAnOldEncryptionFormatAndCan()
            } else if item.openFailed {
                sv.historyError = L10n.sessionHistoryUnreadable()
            }
            sessions[item.sessionID] = sv
        }
        s.sessions = sessions

        var devices: [String: DeviceView] = [:]
        for d in v.devices where d.trusted {
            devices[d.deviceID] = DeviceView(
                id: d.deviceID,
                kind: d.kind,
                createdMs: d.createdMs,
                meta: d.hasMeta ? d.meta : nil,
                metaRev: d.metaRev,
                lastSeenMs: d.lastSeenMs
            )
        }
        s.devices = devices
        return s
    }
}
