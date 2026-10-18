import Connect
import Foundation
import RAProto

extension Remote {
    /// Reads the current signed state and the devices the relay lists, then
    /// keeps only the members the state names. Device metadata is self-signed
    /// and is the only source of a client's name.
    func refreshTrustedDirectory() async throws {
        directoryVersion += 1
        let version = directoryVersion
        let head = try await securitySession().sync().state
        clockWarning = ""
        let hash = try DeviceSecurity.stateHash(head)
        let hints = try await api.client.listDevices(
            request: .init(), headers: Api.headers(token: try await currentToken())
        ).value()
        var members: [String: DeviceSecurity.Member] = [:]
        var metadata: [String: PBEnvelope] = [:]
        var nextDevices: [String: DeviceView] = [:]
        let token = try await currentToken()
        var prefsRequest = PBGetAccountPrefsRequest()
        prefsRequest.objectID = projectPrefsID
        let prefs = try await api.client.getAccountPrefs(
            request: prefsRequest, headers: Api.headers(token: token)
        ).value()
        if prefs.object.hasEnvelope { metadata["prefs"] = prefs.object.envelope }
        var nextMachines = machines
        for member in head.core.members {
            members[member.id] = member
            let hint = hints.devices.first(where: { $0.id == member.id })
            var view = DeviceView(
                id: member.id, kind: PBDeviceKind(rawValue: Int(member.kind))!,
                createdMs: hint?.createdMs ?? 0, lastSeenMs: hint?.lastSeenMs ?? 0)
            if let hint, hint.hasMeta {
                let env = hint.meta
                metadata[(member.kind == 1 ? "machine:" : "device:") + member.id] = env
                if member.kind == 2 {
                    let (h, meta) = try await openSecureMessage(
                        envelope: env, want: Place(.deviceMeta, requestId: member.id),
                        as: PBDeviceMeta.self)
                    try DeviceSecurity.check(h.seq >= hint.metaRevision, .stale)
                    view.meta = meta
                    view.metaRev = h.seq
                } else {
                    let (h, meta) = try await openSecureMessage(
                        envelope: env, want: Place(.machineMeta, machineId: member.id),
                        as: PBMachineMeta.self)
                    var machine =
                        machines[member.id]
                        ?? MachineView(id: member.id, online: false, lastSeenMs: 0)
                    machine.meta = meta
                    machine.metaRev = h.seq
                    nextMachines[member.id] = machine
                }
            } else if member.kind == 1 && nextMachines[member.id] == nil {
                nextMachines[member.id] = MachineView(
                    id: member.id, online: false, lastSeenMs: 0)
            }
            nextDevices[member.id] = view
        }
        guard !closed, version == directoryVersion else { return }
        let installed = try await securitySession().identity().1
        guard try DeviceSecurity.stateHash(installed) == hash else {
            throw DeviceSecurity.Failure.stale
        }
        for (id, device) in nextDevices where device.kind == .client {
            if let old = devices[id], old.metaRev > device.metaRev {
                throw DeviceSecurity.Failure.stale
            }
        }
        for old in Array(trustedMembers.values) where old.kind == 1 && members[old.id] == nil {
            await dropMachine(old.id)
        }
        trustedMembers = members
        trustedMetadata = metadata
        devices = nextDevices
        authorityDeviceId = head.core.authorityID
        machines = nextMachines.filter { members[$0.key]?.kind == 1 }
        for session in Array(sessions.values) where members[session.machineId]?.kind != 1 {
            sessions[session.id] = nil
            sessionKeys[session.id] = nil
            listed.remove(session.id)
        }
        publish()
    }

    /// A new head arrives within a minute; an unknown state is fetched at once.
    func startSecurityPolling() {
        guard let client = try? securitySession() else { return }
        Task { await client.startPolling() }
    }
}
