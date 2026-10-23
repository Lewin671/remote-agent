import Connect
import Foundation
import RAProto

extension Remote {
    /// Reads the current signed state and the devices the relay lists, then
    /// keeps only the members the state names. Device metadata is self-signed
    /// and is the only source of a client's name.
    func refreshTrustedDirectory() async throws {
        try await refreshTrustedDirectory(updateMachine: nil, envelopes: [])
    }

    func checkDirectory(_ machine: String, envelopes: [PBEnvelope]) async throws {
        try await refreshTrustedDirectory(updateMachine: machine, envelopes: envelopes)
    }

    private func refreshTrustedDirectory(updateMachine: String?, envelopes: [PBEnvelope]) async throws {
        let client = try securitySession()
        while !closed {
            // Even a reusable directory must first verify the latest security chain.
            let existing = directoryLoad
            let record = try await client.sync()
            let head = try DeviceSecurity.stateHash(record.state)
            guard try await client.headStateHash() == head else { continue }
            if let job = existing ?? directoryLoad {
                do { try await job.task.value }
                catch {
                    if directoryLoad?.id == job.id { directoryLoad = nil }
                    if try await client.headStateHash() != job.head { continue }
                    throw error
                }
                if directoryLoad?.id == job.id { directoryLoad = nil }
                if try await client.headStateHash() != directoryHead { continue }
                return
            }
            let recoveryPending = await client.recoveryPending
            guard try await client.headStateHash() == head else { continue }
            let operationPending = try (securityStorage as? any AuthorityStorage)?.readOperation() != nil
            let known = envelopes.allSatisfy { env in
                guard let h = try? DeviceSecurity.parseHeader(env.securityHeader) else { return false }
                return h.sender == updateMachine && record.states[h.epoch] != nil
            }
            if let machine = updateMachine, known, directoryHead == head,
                record.state.core.member(account.deviceId) != nil,
                trustedMembers[machine]?.kind == 1, record.state.core.member(machine)?.kind == 1,
                !recoveryObjectsPending, !recoveryPending, !operationPending {
                clockWarning = ""
                return
            }
            directoryVersion += 1
            let version = directoryVersion
            let job = DirectoryLoad(head: head, task: Task { try await self.readTrustedDirectory(record, version: version) })
            directoryLoad = job
            do { try await job.task.value }
            catch {
                if directoryLoad?.id == job.id { directoryLoad = nil }
                if try await client.headStateHash() != head { continue }
                throw error
            }
            // Metadata publication may reload the directory; it runs outside this job.
            if directoryLoad?.id == job.id { directoryLoad = nil }
            if try await client.headStateHash() == directoryHead { return }
        }
    }

    private func readTrustedDirectory(_ record: DeviceSecurity.Record, version: Int) async throws {
        recoveryObjectsPending = true
        var maintenancePending = false
        do {
            try await republishRecoveryObjects(record)
        } catch DeviceSecurity.Failure.lease {
            maintenancePending = true
            // Leave maintenance pending until renewal; stored reads still work.
        }
        let head = record.state
        clockWarning = ""
        let hash = try DeviceSecurity.stateHash(head)
        let hints = try await api.client.listDevices(
            request: .init(), headers: Api.headers(token: try await currentToken())
        ).value()
        let machineRows = try await api.client.listMachines(
            request: .init(), headers: Api.headers(token: try await currentToken())
        ).value()
        var members: [String: DeviceSecurity.Member] = [:]
        var metadata: [String: PBEnvelope] = [:]
        for device in hints.devices where device.hasMeta { metadata["device:" + device.id] = device.meta }
        for machine in machineRows.machines where machine.hasMeta { metadata["machine:" + machine.id] = machine.meta }
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
            if let env = metadata[(member.kind == 1 ? "machine:" : "device:") + member.id] {
                do {
                if member.kind == 2 {
                    let (h, meta) = try await openSecureMessage(
                        envelope: env, want: Place(.deviceMeta, requestId: member.id),
                        as: PBDeviceMeta.self)
                    try DeviceSecurity.check(h.seq >= (hint?.metaRevision ?? 0), .stale)
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
                } catch {
                    if error is CancellationError { throw error }
                    if let old=devices[member.id] { view.meta=old.meta;view.metaRev=old.metaRev }
                    if member.kind == 1 && nextMachines[member.id] == nil {nextMachines[member.id]=MachineView(id:member.id,online:false,lastSeenMs:0)}
                }
            } else if member.kind == 1 && nextMachines[member.id] == nil {
                nextMachines[member.id] = MachineView(
                    id: member.id, online: false, lastSeenMs: 0)
            }
            nextDevices[member.id] = view
        }
        guard !closed, version == directoryVersion else { return }
        let expiration = try await securitySession().leaseExpiration()
        let installed = try await securitySession().identity().1
        guard try DeviceSecurity.stateHash(installed) == hash else {
            throw DeviceSecurity.Failure.stale
        }
        for (id, device) in nextDevices where device.kind == .client {
            if let old = devices[id], old.metaRev > device.metaRev {
                throw DeviceSecurity.Failure.stale
            }
        }
        for (id, current) in machines {
            var next = nextMachines[id] ?? current
            next.online = current.online
            next.lastSeenMs = max(next.lastSeenMs, current.lastSeenMs)
            if current.metaRev >= next.metaRev {
                next.meta = current.meta
                next.metaRev = current.metaRev
                if let env = trustedMetadata["machine:" + id] { metadata["machine:" + id] = env }
            }
            nextMachines[id] = next
        }
        archiveMachines=Set(record.states.values.flatMap{$0.core.members}.filter{$0.kind==1}.map{$0.id})
        for (id,var machine) in nextMachines where members[id] == nil {machine.online=false;nextMachines[id]=machine}
        trustedMembers = members
        trustedMetadata = metadata
        directoryHead = hash
        recoveryObjectsPending = maintenancePending
        devices = nextDevices
        leaseEndMs = expiration
        authorityDeviceId = head.core.authorityID
        machines = nextMachines.filter { members[$0.key]?.kind == 1 || archiveMachines.contains($0.key) }
        for session in Array(sessions.values) where members[session.machineId]?.kind != 1 && !archiveMachines.contains(session.machineId) {
            sessions[session.id] = nil
            sessionKeys[session.id] = nil
            listed.remove(session.id)
        }
        publish()
    }

    /// The stored envelope's epoch is the resumable republish marker. Only a
    /// retained device that can verify and read it may publish the snapshot.
    private func republishRecoveryObjects(_ record: DeviceSecurity.Record) async throws {
        guard let epoch = record.recovery.keys.max(),
            let boundary = record.states[epoch]?.core.contentEpoch else { return }
        let security = try securitySession()
        let headers = Api.headers(token: try await currentToken())
        try await retryRecoveryPublication {
            var get = PBGetAccountPrefsRequest(); get.objectID = projectPrefsID
            let object = try await api.client.getAccountPrefs(request:get,headers:headers).value().object
            if !object.hasEnvelope || object.revision == 0 { return true }
            let h = try DeviceSecurity.parseHeader(object.envelope.securityHeader)
            if h.contentEpoch >= boundary { return true }
            let snapshot: (DeviceSecurity.Header, PBProjectPrefs)
            do { snapshot = try await security.openMessage(object.envelope, as:PBProjectPrefs.self, history:true) }
            catch DeviceSecurity.Failure.unavailable { return true }
            try DeviceSecurity.check(snapshot.0.purpose == .prefs && snapshot.0.seq == object.revision && snapshot.0.request == projectPrefsID, .untrusted)
            var put = PBPutAccountPrefsRequest(); put.objectID=projectPrefsID;put.expectedRevision=object.revision
            put.envelope = try await security.seal(purpose:.prefs,request:projectPrefsID,seq:object.revision+1,plaintext:snapshot.1.serializedData())
            return try await api.client.putAccountPrefs(request:put,headers:headers).value().applied
        }
        try await retryRecoveryPublication {
            let listed = try await api.client.listDevices(request:.init(),headers:headers).value()
            guard let own=listed.devices.first(where:{$0.id==account.deviceId}),own.hasMeta else { return true }
            let h = try DeviceSecurity.parseHeader(own.meta.securityHeader)
            if h.contentEpoch >= boundary { return true }
            let snapshot = try await security.openMessage(own.meta,as:PBDeviceMeta.self,history:true)
            try DeviceSecurity.check(snapshot.0.purpose == .deviceMeta && snapshot.0.sender == account.deviceId && snapshot.0.seq == own.metaRevision,.untrusted)
            var put = PBPutDeviceMetaRequest();put.deviceID=account.deviceId;put.expectedRevision=own.metaRevision
            put.meta=try await security.seal(purpose:.deviceMeta,request:account.deviceId,seq:own.metaRevision+1,plaintext:snapshot.1.serializedData())
            return try await api.client.putDeviceMeta(request:put,headers:headers).value().applied
        }
    }

    func updateLeaseExpiration(_ expiration: UInt64) {
        if leaseEndMs != expiration { leaseEndMs = expiration; publish() }
    }
    func needsDirectoryRefresh(_ head: Data) -> Bool {
        directoryHead != head || recoveryObjectsPending
    }

    /// A new head arrives within a minute; an unknown state is fetched at once.
    /// A moved head also rebuilds the member directory, so a withheld removal
    /// push cannot leave a removed machine trusted.
    func startSecurityPolling(everyMs: UInt64 = 60_000) {
        guard let client = try? securitySession() else { return }
        let onChange: @Sendable () async -> Void = { [weak self] in
            guard let self else { return }
            guard let client = try? await self.securitySession(),
                let head = try? await client.headStateHash() else { return }
            await self.authorityForegroundTick()
            await self.updateRecoveryStatus()
            if let expiration = try? await client.leaseExpiration() { await self.updateLeaseExpiration(expiration) }
            if await self.needsDirectoryRefresh(head) { try? await self.refreshTrustedDirectory() }
        }
        Task {
            await client.startPolling(everyMs: everyMs, onChange: onChange)
        }
    }
}
