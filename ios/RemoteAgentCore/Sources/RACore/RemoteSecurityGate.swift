// Security Gate helpers for M2: decrypts/verifies sealed relay/cache payloads
// into plaintext Core events, tracks opaque cacheRefs for CacheWriteEffect,
// and executes RPC / CipherCache I/O on behalf of CoreHost.
import Connect
import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    enum CachedCipherObject: Sendable {
        case machine(PBMachine)
        case session(PBRemoteSession)
        case event(PBStoredEvent)
        case prefs(PBAccountPrefsObject)
    }

    struct SealedCommandEntry {
        let stateHash: Data
        let envelope: PBEnvelope
        let keyHash: Data
        let payloadHash: Data
    }

    // MARK: - Bounded in-memory caches (cipher blobs by cacheRef + sealed commands by requestID)

    func trackCacheRef(_ item: CachedCipherObject) -> String {
        let ref = UUID().uuidString
        cacheRefs[ref] = item
        cacheRefOrder.append(ref)
        let cap = 2048
        if cacheRefOrder.count > cap {
            let dropCount = cacheRefOrder.count - cap
            for key in cacheRefOrder.prefix(dropCount) {
                cacheRefs.removeValue(forKey: key)
            }
            cacheRefOrder.removeFirst(dropCount)
        }
        return ref
    }

    func storeSealedCommand(requestID: String, entry: SealedCommandEntry) {
        sealedCommands[requestID] = entry
        sealedOrder.removeAll { $0 == requestID }
        sealedOrder.append(requestID)
        let cap = 64
        if sealedOrder.count > cap {
            let dropCount = sealedOrder.count - cap
            for key in sealedOrder.prefix(dropCount) {
                sealedCommands.removeValue(forKey: key)
            }
            sealedOrder.removeFirst(dropCount)
        }
    }

    // MARK: - Security Snapshot

    public func syncSecuritySnapshot(
        opId: UInt64,
        networkGeneration netGen: UInt64,
        touch: Bool
    ) async -> PBCoreSecuritySnapshotEvent {
        var event = PBCoreSecuritySnapshotEvent()
        event.opID = opId
        event.networkGeneration = netGen
        event.selfDeviceID = account.deviceId
        do {
            if netGen != verifiedNetworkGeneration {
                try await refreshNetworkSecurity()
            }
            let client = try securitySession()
            if touch {
                try await client.touchAfterWake()
            }
            let record = try await client.sync()
            var maintenancePending = false
            if !record.recovery.isEmpty {
                do {
                    try await republishRecoveryObjects(record)
                } catch DeviceSecurity.Failure.lease {
                    maintenancePending = true
                } catch {
                    throw error
                }
            }
            let head = record.state
            let expiration = try await client.leaseExpiration()
            let archive = try await client.historicalMachines()
            let members = Dictionary(uniqueKeysWithValues: head.core.members.map { ($0.id, $0) })

            self.trustedMembers = members
            self.archiveMachines = archive
            self.authorityDeviceId = head.core.authorityID
            self.leaseEndMs = expiration
            self.recoveryPending = await client.recoveryPending
            self.recoveryReadyMs = await client.recoveryReadyMs
            self.recoveryObjectsPending = maintenancePending
            self.recoveryStatusStale = await client.recoveryStatusStale
            self.clockSkewed = false
            if netGen == self.networkGeneration {
                self.verifiedNetworkGeneration = netGen
            }
            for id in archive where members[id]?.kind != 1 {
                await cache?.removeBlob(name: "schedules/" + id)
            }

            event.isMember = record.member
            event.authorityDeviceID = head.core.authorityID
            event.trustedMachineIds = head.core.members.filter { $0.kind == 1 }.map(\.id)
            event.trustedClientIds = head.core.members.filter { $0.kind == 2 }.map(\.id)
            event.archiveMachineIds = Array(archive)
            event.headEpoch = head.core.epoch
            event.leaseEndMs = Int64(expiration)
            event.clockSkewed = false
            event.recoveryPending = recoveryPending
            event.recoveryReadyMs = Int64(recoveryReadyMs)
            event.recoveryObjectsPending = maintenancePending
            return event
        } catch {
            if let secErr = error as? DeviceSecurity.Failure, secErr == .clock {
                self.clockSkewed = true
                event.clockSkewed = true
            }
            report(error)
            event.error = errorText(error)
            event.unresolvedHost = unresolvedHost(error)
            return event
        }
    }

    // MARK: - Envelope Decryption Helpers

    func decodeMachine(_ machine: PBMachine, trackRef: Bool) async -> PBCorePlainMachine {
        var out = PBCorePlainMachine()
        out.machineID = machine.id
        out.online = machine.online
        out.lastSeenMs = machine.lastSeenMs
        if machine.hasMeta {
            do {
                let (h, meta) = try await openSecureMessage(
                    envelope: machine.meta,
                    want: Place(.machineMeta, machineId: machine.id),
                    as: PBMachineMeta.self,
                    history: !trackRef
                )
                out.meta = meta
                out.metaRev = h.seq
            } catch {
                out.metaBroken = true
                report(error)
            }
        }
        if trackRef {
            out.cacheRef = trackCacheRef(.machine(machine))
        }
        return out
    }

    func decodeSession(_ rs: PBRemoteSession, trackRef: Bool) async -> PBCorePlainSession {
        var out = PBCorePlainSession()
        out.sessionID = rs.sessionID
        out.machineID = rs.machineID
        out.lastSeq = rs.lastSeq
        out.updatedMs = rs.updatedMs
        out.listedEndedMs = rs.endedMs
        do {
            try rejectRetiredHistory(rs.key)
            let opened = try await securitySession().openHistory(rs.key)
            let kh = projection(opened.0)
            let raw = opened.1
            try Envelopes.expect(kh, Place(.sessionKey, machineId: rs.machineID, sessionId: rs.sessionID))
            if raw.count != keySize {
                throw OpenError(L10nMessage.errorsIosCryptoBadSessionKey())
            }
            sessionKeys[rs.sessionID] = raw
            if rs.hasMeta {
                let (mh, info) = try await openSecureMessage(
                    envelope: rs.meta,
                    want: Place(.sessionMeta, machineId: rs.machineID, sessionId: rs.sessionID),
                    as: PBSession.self,
                    history: !trackRef
                )
                out.meta = info
                out.metaRev = mh.seq
                machineReplacements[rs.sessionID] = info.machineReplacements
                if !info.currentCheckpoint.isEmpty {
                    if let cp = try? PBLiveSnapshot(serializedBytes: info.currentCheckpoint),
                       cp.session.id == rs.sessionID,
                       cp.session.lastSeq <= info.lastSeq,
                       cp.actions.allSatisfy({ $0.sessionID == rs.sessionID && $0.seq <= info.lastSeq }) {
                        out.checkpoint = cp
                    } else {
                        out.checkpointInvalid = true
                    }
                }
            }
        } catch is UnsupportedHistoryError {
            out.retiredHistory = true
        } catch {
            out.openFailed = true
            report(error)
        }
        if trackRef {
            out.cacheRef = trackCacheRef(.session(rs))
        }
        return out
    }

    func decodeDevice(_ device: PBDevice) async -> PBCorePlainDevice {
        var out = PBCorePlainDevice()
        out.deviceID = device.id
        out.kind = device.kind
        out.createdMs = device.createdMs
        out.lastSeenMs = device.lastSeenMs
        if device.hasMeta, device.kind == .client {
            do {
                let (h, meta) = try await openSecureMessage(
                    envelope: device.meta,
                    want: Place(.deviceMeta, requestId: device.id),
                    as: PBDeviceMeta.self
                )
                try DeviceSecurity.check(h.seq >= device.metaRevision, .stale)
                out.meta = meta
                out.metaRev = h.seq
            } catch {
                out.metaBroken = true
                report(error)
            }
        } else {
            out.metaRev = device.metaRevision
        }
        return out
    }

    func decodeAccountPrefs(_ obj: PBAccountPrefsObject, trackRef: Bool) async throws -> PBCorePlainAccountPrefs {
        var out = PBCorePlainAccountPrefs()
        out.revision = obj.revision
        if obj.hasEnvelope && !obj.envelope.securityHeader.isEmpty && obj.revision > 0 {
            do {
                let (h, prefs) = try await openSecureMessage(
                    envelope: obj.envelope,
                    want: Place(.accountPrefs, requestId: projectPrefsID),
                    as: PBProjectPrefs.self,
                    history: true
                )
                out.revision = max(obj.revision, h.seq)
                out.prefs = prefs
            } catch {
                out.broken = true
                report(error)
            }
        }
        if trackRef {
            out.cacheRef = trackCacheRef(.prefs(obj))
        }
        return out
    }

    func decodeEvents(
        _ stored: [PBStoredEvent],
        machineId: String,
        sessionId: String,
        history: Bool,
        trackRef: Bool
    ) async -> [PBCorePlainEvent] {
        guard !stored.isEmpty else { return [] }
        guard let sessionSeed = sessionKeys[sessionId] else {
            return stored.map { se in
                var ev = PBCorePlainEvent()
                ev.seq = se.seq
                do {
                    try rejectRetiredHistory(se.envelope)
                    ev.broken = true
                } catch is UnsupportedHistoryError {
                    ev.retired = true
                } catch {
                    ev.broken = true
                }
                return ev
            }
        }

        let batchSize = DeviceSecurityClient.historyBatchSize
        var historyResults: [Int: Result<(DeviceSecurity.Header, Data), Error>] = [:]
        var output: [PBCorePlainEvent] = []
        output.reserveCapacity(stored.count)

        for (index, se) in stored.enumerated() {
            if history && index % batchSize == 0 {
                let end = min(index + batchSize, stored.count)
                let indices = (index..<end).filter {
                    (try? PBHeader(serializedBytes: stored[$0].envelope.header).version) != 1
                }
                historyResults.removeAll(keepingCapacity: true)
                if !indices.isEmpty {
                    do {
                        let results = try await securitySession().openHistoryBatch(
                            indices.map { stored[$0].envelope },
                            sessionSeed: sessionSeed
                        )
                        historyResults = Dictionary(uniqueKeysWithValues: zip(indices, results))
                    } catch {
                        historyResults = Dictionary(uniqueKeysWithValues: indices.map { ($0, .failure(error)) })
                    }
                }
            }

            var item = PBCorePlainEvent()
            item.seq = se.seq
            do {
                try rejectRetiredHistory(se.envelope)
                let opened: (DeviceSecurity.Header, Data)
                if history {
                    guard let entry = historyResults[index] else {
                        throw DeviceSecurity.Failure.storage
                    }
                    opened = try entry.get()
                } else {
                    opened = try await securitySession().openCurrent(se.envelope, sessionSeed: sessionSeed)
                }
                let h = projection(opened.0)
                let plain = opened.1
                let place = try await historicalPlace(
                    Place(.event, machineId: machineId, sessionId: sessionId, seq: se.seq),
                    header: h,
                    history: history
                )
                try Envelopes.expect(h, place)
                guard let pb = try? PBEvent(serializedBytes: plain),
                      pb.seq == se.seq,
                      pb.sessionID == sessionId else {
                    item.broken = true
                    output.append(item)
                    continue
                }
                item.event = pb
                item.verified = true
                item.approxBytes = Int64(plain.count) + 256
                if trackRef {
                    item.cacheRef = trackCacheRef(.event(se))
                }
            } catch let unavailable as RecoveryHistoryUnavailable {
                do {
                    let h = projection(unavailable.header)
                    let place = try await historicalPlace(
                        Place(.event, machineId: machineId, sessionId: sessionId, seq: se.seq),
                        header: h,
                        history: history
                    )
                    try Envelopes.expect(h, place)
                    var slot = PBEvent()
                    slot.seq = se.seq
                    slot.sessionID = sessionId
                    item.event = slot
                    item.unavailable = true
                    item.approxBytes = 256
                    if trackRef {
                        item.cacheRef = trackCacheRef(.event(se))
                    }
                } catch is UnsupportedHistoryError {
                    item.retired = true
                } catch {
                    item.broken = true
                }
            } catch is UnsupportedHistoryError {
                item.retired = true
            } catch {
                item.broken = true
            }
            output.append(item)
        }
        return output
    }

    // MARK: - Cache I/O Execution

    public func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            var loaded = PBCoreCacheBootstrapLoadedEvent()
            loaded.opID = req.opID
            do {
                let identity = try securityIdentity()
                let client = try securitySession()
                let members = Dictionary(uniqueKeysWithValues: identity.1.core.members.map { ($0.id, $0) })
                let archive = try await client.historicalMachines()
                self.trustedMembers = members
                self.archiveMachines = archive
                self.authorityDeviceId = identity.1.core.authorityID
                self.leaseEndMs = try await client.leaseExpiration()
                for id in archive where members[id]?.kind != 1 {
                    await cache?.removeBlob(name: "schedules/" + id)
                }
                if let cache {
                    for var m in await cache.machines() {
                        guard members[m.id]?.kind == 1 || archive.contains(m.id) else { continue }
                        m.online = false
                        loaded.machines.append(await decodeMachine(m, trackRef: false))
                    }
                    for s in await cache.sessions() {
                        guard members[s.machineID]?.kind == 1 || archive.contains(s.machineID) else { continue }
                        loaded.sessions.append(await decodeSession(s, trackRef: false))
                    }
                    if let blob = await cache.blob(name: projectPrefsID),
                       let obj = try? PBAccountPrefsObject(serializedBytes: blob) {
                        if let plainPrefs = try? await decodeAccountPrefs(obj, trackRef: false) {
                            loaded.prefs = plainPrefs
                        }
                    }
                }
            } catch {
                loaded.error = errorText(error)
                report(error)
            }
            event.cacheBootstrapLoaded = loaded
        case .events(let req):
            var loaded = PBCoreCacheEventsLoadedEvent()
            loaded.opID = req.opID
            loaded.sessionID = req.sessionID
            if let cache {
                let stored = req.newest
                    ? await cache.newestEvents(sessionId: req.sessionID)
                    : await cache.eventPage(sessionId: req.sessionID, afterSeq: req.afterSeq)
                let machineId = machineIdForSession(req.sessionID)
                let decoded = await decodeEvents(
                    stored,
                    machineId: machineId,
                    sessionId: req.sessionID,
                    history: true,
                    trackRef: false
                )
                loaded.events = decoded
                loaded.retired = decoded.contains(where: \.retired)
                loaded.broken = decoded.contains(where: \.broken)
            }
            event.cacheEventsLoaded = loaded
        case nil:
            break
        }
        return event
    }

    public func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async {
        guard let cache else { return }
        switch effect.kind {
        case .writeMachineRef(let ref):
            if case .machine(let m)? = cacheRefs[ref] {
                await cache.putMachine(m)
            }
        case .writeSessionRef(let ref):
            if case .session(let rs)? = cacheRefs[ref] {
                await cache.putSession(rs)
            }
        case .keepSessionOwnerRef(let ref):
            if case .session(let rs)? = cacheRefs[ref] {
                await cache.keepOwner(rs)
            }
        case .writeEvents(let batch):
            let items: [PBStoredEvent] = batch.cacheRefs.compactMap { ref in
                if case .event(let se)? = cacheRefs[ref] { return se }
                return nil
            }
            if !items.isEmpty {
                await cache.putEvents(sessionId: batch.sessionID, events: items)
            }
        case .writePrefsRef(let ref):
            if case .prefs(let obj)? = cacheRefs[ref],
               let data = try? obj.serializedData() {
                await cache.putBlob(name: projectPrefsID, data: data)
            }
        case .setSessionOpen:
            break
        case nil:
            break
        }
    }

    public func executeCacheDelete(
        _ effect: PBCoreCacheDeleteEffect,
        onSessionRemoved: (@Sendable (String) async -> Void)? = nil
    ) async {
        switch effect.kind {
        case .dropEventsSessionID(let sid):
            await cache?.dropEvents(sessionId: sid)
        case .removeSessionID(let sid):
            sessionKeys.removeValue(forKey: sid)
            machineReplacements.removeValue(forKey: sid)
            await cache?.removeSession(id: sid)
            await onSessionRemoved?(sid)
            await self.sessionRemovalHandler(sid)
        case .unlistSessionID(let sid):
            await cache?.unlistSession(id: sid)
        case .removeMachineID(let mid):
            await cache?.removeMachine(id: mid)
        case nil:
            break
        }
    }

    // MARK: - RPC Execution

    func withAuthRetry<T>(_ call: @Sendable ([String: [String]]) async throws -> T) async throws -> T {
        var used = try await currentToken()
        do {
            return try await call(Api.headers(token: used))
        } catch let error as ConnectError where error.code == .unauthenticated {
            try Task.checkCancellation()
            rejectCredential(used)
            used = try await currentToken()
            do {
                return try await call(Api.headers(token: used))
            } catch let error as ConnectError where error.code == .unauthenticated {
                rejectCredential(used)
                throw error
            }
        }
    }

    public func executeRpc(_ rpc: PBCoreRpcEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch rpc.kind {
        case .fetchLists(let req):
            var res = PBCoreListsFetchedEvent()
            res.opID = rpc.opID
            do {
                let apiClient = api.client
                let limit = req.limit == 0 ? 50 : req.limit
                let (machinesResp, sessionsResp, devicesResp) = try await withAuthRetry { headers in
                    async let m = apiClient.listMachines(request: .init(), headers: headers).value()
                    var ask = PBListRemoteSessionsRequest()
                    ask.limit = limit
                    async let s = apiClient.listRemoteSessions(request: ask, headers: headers).value()
                    async let d = apiClient.listDevices(request: .init(), headers: headers).value()
                    return try await (m, s, d)
                }
                for m in machinesResp.machines {
                    res.machines.append(await decodeMachine(m, trackRef: true))
                }
                for s in sessionsResp.sessions {
                    res.sessions.append(await decodeSession(s, trackRef: true))
                }
                if sessionsResp.hasNext {
                    res.next = sessionsResp.next
                }
                for d in devicesResp.devices {
                    res.devices.append(await decodeDevice(d))
                }
            } catch {
                res.error = errorText(error)
                res.unresolvedHost = unresolvedHost(error)
                report(error)
            }
            event.listsFetched = res

        case .fetchSessionsPage(let req):
            var res = PBCoreSessionsPageFetchedEvent()
            res.opID = rpc.opID
            do {
                let apiClient = api.client
                var askReq = PBListRemoteSessionsRequest()
                askReq.limit = req.limit == 0 ? 50 : req.limit
                if req.hasAfter { askReq.after = req.after }
                let ask = askReq
                let page = try await withAuthRetry { headers in
                    try await apiClient.listRemoteSessions(request: ask, headers: headers).value()
                }
                for s in page.sessions {
                    res.sessions.append(await decodeSession(s, trackRef: true))
                }
                if page.hasNext {
                    res.next = page.next
                }
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.sessionsPageFetched = res

        case .fetchSessionByID(let req):
            var res = PBCoreSessionByIdFetchedEvent()
            res.opID = rpc.opID
            do {
                let apiClient = api.client
                var askReq = PBListRemoteSessionsRequest()
                askReq.sessionIds = [req.sessionID]
                let ask = askReq
                let reply = try await withAuthRetry { headers in
                    try await apiClient.listRemoteSessions(request: ask, headers: headers).value()
                }
                if let rs = reply.sessions.first(where: { $0.sessionID == req.sessionID }) {
                    res.session = await decodeSession(rs, trackRef: true)
                }
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.sessionByIDFetched = res

        case .getEvents(let req):
            var res = PBCoreEventsPageFetchedEvent()
            res.opID = rpc.opID
            res.sessionID = req.sessionID
            do {
                let apiClient = api.client
                var askReq = PBGetEventsRequest()
                askReq.sessionID = req.sessionID
                askReq.afterSeq = req.afterSeq
                askReq.limit = req.limit == 0 ? 500 : req.limit
                let ask = askReq
                let reply = try await withAuthRetry { headers in
                    try await apiClient.getEvents(request: ask, headers: headers).value()
                }
                let machineId = machineIdForSession(req.sessionID)
                res.events = await decodeEvents(
                    reply.events,
                    machineId: machineId,
                    sessionId: req.sessionID,
                    history: true,
                    trackRef: true
                )
                res.more = reply.more
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.eventsPageFetched = res

        case .getAccountPrefs(let req):
            var res = PBCoreAccountPrefsFetchedEvent()
            res.opID = rpc.opID
            do {
                let apiClient = api.client
                var askReq = PBGetAccountPrefsRequest()
                askReq.objectID = req.objectID.isEmpty ? projectPrefsID : req.objectID
                let ask = askReq
                let reply = try await withAuthRetry { headers in
                    try await apiClient.getAccountPrefs(request: ask, headers: headers).value()
                }
                res.prefs = try await decodeAccountPrefs(reply.object, trackRef: true)
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.accountPrefsFetched = res

        case .putAccountPrefs(let req):
            var res = PBCoreAccountPrefsPutResultEvent()
            res.opID = rpc.opID
            do {
                let objectId = req.objectID.isEmpty ? projectPrefsID : req.objectID
                let client = try securitySession()
                let env: PBEnvelope
                do {
                    env = try await client.seal(
                        purpose: .prefs,
                        request: objectId,
                        seq: req.expectedRevision + 1,
                        plaintext: try req.nextPrefs.serializedData()
                    )
                } catch DeviceSecurity.Failure.conflict {
                    res.sealConflict = true
                    res.error = errorText(DeviceSecurity.Failure.conflict)
                    event.accountPrefsPutResult = res
                    return event
                }
                let apiClient = api.client
                var putReq = PBPutAccountPrefsRequest()
                putReq.objectID = objectId
                putReq.expectedRevision = req.expectedRevision
                putReq.envelope = env
                let put = putReq
                let reply = try await withAuthRetry { headers in
                    try await apiClient.putAccountPrefs(request: put, headers: headers).value()
                }
                res.applied = reply.applied
                if reply.hasObject {
                    if reply.applied {
                        var plain = PBCorePlainAccountPrefs()
                        plain.revision = reply.object.revision
                        plain.prefs = req.nextPrefs
                        plain.cacheRef = trackCacheRef(.prefs(reply.object))
                        res.current = plain
                    } else {
                        res.current = try await decodeAccountPrefs(reply.object, trackRef: true)
                    }
                }
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.accountPrefsPutResult = res

        case .fetchDevices:
            var res = PBCoreDevicesFetchedEvent()
            res.opID = rpc.opID
            do {
                let apiClient = api.client
                let reply = try await withAuthRetry { headers in
                    try await apiClient.listDevices(request: .init(), headers: headers).value()
                }
                for d in reply.devices {
                    res.devices.append(await decodeDevice(d))
                }
            } catch {
                res.error = errorText(error)
                report(error)
            }
            event.devicesFetched = res

        case nil:
            break
        }
        return event
    }
}
