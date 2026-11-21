// Security Gate executor: syncs verified DeviceSecurity records into Go
// `clientcore/security`, executes wire RPC / CipherCache I/O, and forwards raw
// envelopes to Go without client-side protobuf decryption in Swift.
import Connect
import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    // MARK: - Security Record Sync

    func pushObservedSecurityRecord(_ record: DeviceSecurity.Record) async {
        guard coreSyncDepth == 0, !closed, let client = deviceSecurity else { return }
        await ensureSecurityConfigured()
        let head = record.state
        let members = Dictionary(uniqueKeysWithValues: head.core.members.map { ($0.id, $0) })
        let archive = Set(record.states.values.flatMap(\.core.members).filter { $0.kind == 1 }.map(\.id))
        for id in archive where members[id]?.kind != 1 {
            await cache?.removeBlob(name: "schedules/" + id)
        }
        guard let ev = try? buildSecurityRecordEvent(
            record: record,
            opId: 0,
            pushSeq: nextSecurityPushSeq(),
            networkGeneration: networkGeneration,
            recoveryPending: await client.recoveryPending,
            recoveryReadyMs: await client.recoveryReadyMs,
            recoveryObjectsPending: recoveryObjectsPending
        ) else { return }
        if let onRecord = hostHooks?.onSecurityRecord {
            await onRecord(ev)
        } else {
            try? ensureFallbackRunner().syncRecord(ev, nowMs: clock())
        }
        securityRecordLoaded = true
    }

    public func syncSecurityRecord(
        opId: UInt64,
        networkGeneration netGen: UInt64,
        touch: Bool
    ) async -> PBSecurityRecordEvent {
        var event = PBSecurityRecordEvent()
        event.opID = opId
        event.networkGeneration = netGen
        event.account = account.accountId
        coreSyncDepth += 1
        defer { coreSyncDepth -= 1 }
        do {
            if netGen != verifiedNetworkGeneration {
                try await refreshNetworkSecurity()
            }
            let client = try securitySession()
            if touch {
                try await client.touchAfterWake()
            }
            let synced = try await client.sync()
            var maintenancePending = false
            if !synced.recovery.isEmpty {
                await ensureSecurityConfigured()
                let preEv = try buildSecurityRecordEvent(
                    record: synced,
                    opId: 0,
                    pushSeq: nextSecurityPushSeq(),
                    networkGeneration: netGen,
                    recoveryPending: await client.recoveryPending,
                    recoveryReadyMs: await client.recoveryReadyMs,
                    recoveryObjectsPending: recoveryObjectsPending
                )
                if let onWait = hostHooks?.onSecurityRecordAndWait {
                    await onWait(preEv)
                } else {
                    try ensureFallbackRunner().syncRecord(preEv, nowMs: clock())
                }
                securityRecordLoaded = true
                recoveryObjectsPending = true
                do {
                    try await republishRecoveryObjects(synced)
                    recoveryObjectsPending = false
                } catch DeviceSecurity.Failure.lease {
                    recoveryObjectsPending = false
                    maintenancePending = true
                } catch {
                    recoveryObjectsPending = false
                    throw error
                }
            }
            let latest = (try? await client.record()) ?? synced
            let head = latest.state
            let members = Dictionary(uniqueKeysWithValues: head.core.members.map { ($0.id, $0) })
            let archive = Set(latest.states.values.flatMap(\.core.members).filter { $0.kind == 1 }.map(\.id))
            for id in archive where members[id]?.kind != 1 {
                await cache?.removeBlob(name: "schedules/" + id)
            }
            await ensureSecurityConfigured()
            let built = try buildSecurityRecordEvent(
                record: latest,
                opId: opId,
                pushSeq: nextSecurityPushSeq(),
                networkGeneration: netGen,
                recoveryPending: await client.recoveryPending,
                recoveryReadyMs: await client.recoveryReadyMs,
                recoveryObjectsPending: maintenancePending
            )
            if hostHooks?.onSecurityOp == nil {
                try ensureFallbackRunner().syncRecord(built, nowMs: clock())
            }
            securityRecordLoaded = true
            self.clockSkewed = false
            if netGen == self.networkGeneration {
                self.verifiedNetworkGeneration = netGen
            }
            return built
        } catch {
            if let secErr = error as? DeviceSecurity.Failure, secErr == .clock {
                self.clockSkewed = true
            }
            report(error)
            event.error = errorText(error)
            return event
        }
    }

    // MARK: - Cache I/O Execution

    public func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            var loaded = PBCoreCacheBootstrapLoadedEvent()
            loaded.opID = req.opID
            do {
                _ = try securityIdentity()
                _ = try securitySession()
                guard let securityStorage else { throw DeviceSecurity.Failure.storage }
                let record = try securityStorage.readSecurity()
                let members = Dictionary(uniqueKeysWithValues: record.state.core.members.map { ($0.id, $0) })
                let archive = Set(record.states.values.flatMap { $0.core.members }.filter { $0.kind == 1 }.map { $0.id })
                for id in archive where members[id]?.kind != 1 {
                    await cache?.removeBlob(name: "schedules/" + id)
                }
                await ensureSecurityConfigured()
                let recEv = try buildSecurityRecordEvent(
                    record: record,
                    opId: 0,
                    pushSeq: nextSecurityPushSeq(),
                    networkGeneration: networkGeneration,
                    recoveryPending: recoveryPending,
                    recoveryReadyMs: recoveryReadyMs,
                    recoveryObjectsPending: recoveryObjectsPending
                )
                if let onWait = hostHooks?.onSecurityRecordAndWait {
                    await onWait(recEv)
                } else {
                    try? ensureFallbackRunner().syncRecord(recEv, nowMs: clock())
                }
                securityRecordLoaded = true
                if let cache {
                    for var m in await cache.machines() {
                        guard members[m.id]?.kind == 1 || archive.contains(m.id) else { continue }
                        m.online = false
                        loaded.wireMachines.append(m)
                    }
                    for s in await cache.sessions() {
                        guard members[s.machineID]?.kind == 1 || archive.contains(s.machineID) else { continue }
                        loaded.wireSessions.append(s)
                    }
                    if let blob = await cache.blob(name: projectPrefsID),
                       let obj = try? PBAccountPrefsObject(serializedBytes: blob) {
                        loaded.wirePrefs = obj
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
                loaded.wireEvents = req.newest
                    ? await cache.newestEvents(sessionId: req.sessionID)
                    : await cache.eventPage(sessionId: req.sessionID, afterSeq: req.afterSeq)
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
        case .writeMachine(let m):
            await cache.putMachine(m)
        case .writeSession(let rs):
            await cache.putSession(rs)
        case .keepSessionOwner(let rs):
            await cache.keepOwner(rs)
        case .writeStoredEvents(let batch):
            if !batch.events.isEmpty {
                await cache.putEvents(sessionId: batch.sessionID, events: batch.events)
            }
        case .writePrefs(let obj):
            if let data = try? obj.serializedData() {
                await cache.putBlob(name: projectPrefsID, data: data)
            }
        case .writeMachineRef, .writeSessionRef, .keepSessionOwnerRef, .writeEvents, .writePrefsRef, .setSessionOpen, nil:
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
                res.wireMachines = machinesResp.machines
                res.wireSessions = sessionsResp.sessions
                if sessionsResp.hasNext {
                    res.next = sessionsResp.next
                }
                res.wireDevices = devicesResp.devices
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
                res.wireSessions = page.sessions
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
                    res.wireSession = rs
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
                res.wireEvents = reply.events
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
                res.wirePrefs = reply.object
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
                let env: PBEnvelope
                if req.hasSealedEnvelope {
                    env = req.sealedEnvelope
                } else {
                    do {
                        env = try await sealEnvelopeOp(
                            purpose: .prefs,
                            requestId: objectId,
                            seq: req.expectedRevision + 1,
                            plaintext: try req.nextPrefs.serializedData()
                        ).envelope
                    } catch DeviceSecurity.Failure.conflict {
                        res.sealConflict = true
                        res.error = errorText(DeviceSecurity.Failure.conflict)
                        event.accountPrefsPutResult = res
                        return event
                    }
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
                    res.wireCurrent = reply.object
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
                res.wireDevices = reply.devices
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
