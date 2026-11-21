// Transitional Step 4 adapters from Core's SyncStateView
// into the legacy shell model (`RemoteState`) and the
// per-slot unknown-command tracker (`Attempt`) used by unmigrated Step 4 views
// (`SchedulesScreen`, `ScheduleStore`, `Settings`, `MachineStatusScreen`).
//
// Delete this file once Step 4 migrates those screens into Go Core.
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
        s.recoveryStatusStale = false
        s.removed = v.removed
        s.clockWarning = v.accountClockSkewed ? L10n.errorsIosThisDeviceIsOver5MinutesBehindTheSecurity() : ""
        s.displayGrace = v.displayGrace
        s.reachabilityUnknown = v.reachabilityUnknown
        if v.hasConnectionReason {
            let text = renderCoreText(v.connectionReason)
            s.error = text
            s.errorDetail = text
        }
        if v.sessionListFailed, v.hasSessionListError {
            s.sessionsError = renderCoreText(v.sessionListError)
        }
        s.loadingSessions = v.loadingMoreSessions
        s.sessionsListed = v.sessionsListed
        if v.hasMoreSessions_p && v.hasNextCursor {
            s.sessionsNext = SessionPlace(
                endedMs: v.nextCursor.endedMs,
                sessionId: v.nextCursor.sessionID
            )
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

/// Step 4 transitional per-slot command holder for unmigrated UI views
/// (`ScheduleMutation`). Delete when Step 4 migrates Schedules commands into Core.
final class Attempt {
    static var expiredUnknown: LocalizedMessage {
        L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult()
    }

    private(set) var pending: PreparedCommand?
    private(set) var frozen = false
    private(set) var expired = false

    func next(_ make: () throws -> PreparedCommand) throws -> PreparedCommand {
        if expired {
            throw CommandError(CommandError.unknownOutcome, Self.expiredUnknown, outcome: .unknown)
        }
        if frozen, let pending { return pending }
        let prepared = try make()
        pending = prepared
        frozen = false
        return prepared
    }

    func settle() {
        pending = nil
        frozen = false
        expired = false
    }

    @discardableResult
    func settle(_ error: Error) -> Error? {
        guard let ce = error as? CommandError else {
            if frozen { return error }
            pending = nil
            frozen = false
            return error
        }
        if frozen {
            if ce.code == CommandError.expired {
                expired = true
                return CommandError(CommandError.unknownOutcome, Self.expiredUnknown, outcome: .unknown)
            }
            return ce.unknown ? ce : CommandError.unknownBecause(errorText(ce))
        }
        if ce.unknown || pending?.mayHaveRun == true {
            frozen = true
            return ce.unknown ? ce : CommandError.unknownBecause(errorText(ce))
        }
        pending = nil
        frozen = false
        return error
    }

    func abandon() {
        pending = nil
        frozen = false
        expired = false
    }
}
