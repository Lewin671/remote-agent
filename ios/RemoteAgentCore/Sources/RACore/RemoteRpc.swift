import Connect
import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    // MARK: - Cache I/O Execution

    public func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            var loaded = PBCoreCacheBootstrapLoadedEvent()
            loaded.opID = req.opID
            do {
                try await ensureSecurityBootstrapped()
                await pruneUntrustedCaches()
                if let cache {
                    let allowed = await cachedAllowedMachines()
                    for var m in await cache.machines() {
                        if let allowed, !allowed.contains(m.id) { continue }
                        m.online = false
                        loaded.wireMachines.append(m)
                    }
                    for s in await cache.sessions() {
                        if let allowed, !allowed.contains(s.machineID) { continue }
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
        case .setSessionOpen, nil:
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

    private func withRpcError(_ onError: (String, Error) -> Void, _ work: () async throws -> Void) async {
        do {
            try await work()
        } catch {
            onError(errorText(error), error)
            report(error)
        }
    }

    public func executeRpc(_ rpc: PBCoreRpcEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        let apiClient = api.client
        switch rpc.kind {
        case .fetchLists(let req):
            var res = PBCoreListsFetchedEvent(); res.opID = rpc.opID
            await withRpcError({ msg, err in res.error = msg; res.unresolvedHost = unresolvedHost(err) }) {
                var ask = PBListRemoteSessionsRequest(); ask.limit = req.limit == 0 ? 50 : req.limit
                let sAsk = ask
                let (mResp, sResp, dResp) = try await withAuthRetry { headers in
                    async let m = apiClient.listMachines(request: .init(), headers: headers).value()
                    async let s = apiClient.listRemoteSessions(request: sAsk, headers: headers).value()
                    async let d = apiClient.listDevices(request: .init(), headers: headers).value()
                    return try await (m, s, d)
                }
                res.wireMachines = mResp.machines; res.wireSessions = sResp.sessions; res.wireDevices = dResp.devices
                if sResp.hasNext { res.next = sResp.next }
            }
            event.listsFetched = res

        case .fetchSessionsPage(let req):
            var res = PBCoreSessionsPageFetchedEvent(); res.opID = rpc.opID
            await withRpcError({ msg, _ in res.error = msg }) {
                var ask = PBListRemoteSessionsRequest(); ask.limit = req.limit == 0 ? 50 : req.limit
                if req.hasAfter { ask.after = req.after }
                let sAsk = ask
                let page = try await withAuthRetry { try await apiClient.listRemoteSessions(request: sAsk, headers: $0).value() }
                res.wireSessions = page.sessions
                if page.hasNext { res.next = page.next }
            }
            event.sessionsPageFetched = res

        case .fetchSessionByID(let req):
            var res = PBCoreSessionByIdFetchedEvent(); res.opID = rpc.opID
            await withRpcError({ msg, _ in res.error = msg }) {
                let ask = PBListRemoteSessionsRequest.with { $0.sessionIds = [req.sessionID] }
                let reply = try await withAuthRetry { try await apiClient.listRemoteSessions(request: ask, headers: $0).value() }
                if let rs = reply.sessions.first(where: { $0.sessionID == req.sessionID }) { res.wireSession = rs }
            }
            event.sessionByIDFetched = res

        case .getEvents(let req):
            var res = PBCoreEventsPageFetchedEvent(); res.opID = rpc.opID; res.sessionID = req.sessionID
            await withRpcError({ msg, _ in res.error = msg }) {
                let ask = PBGetEventsRequest.with {
                    $0.sessionID = req.sessionID; $0.afterSeq = req.afterSeq; $0.limit = req.limit == 0 ? 500 : req.limit
                }
                let reply = try await withAuthRetry { try await apiClient.getEvents(request: ask, headers: $0).value() }
                res.wireEvents = reply.events; res.more = reply.more
            }
            event.eventsPageFetched = res

        case .getAccountPrefs(let req):
            var res = PBCoreAccountPrefsFetchedEvent(); res.opID = rpc.opID
            await withRpcError({ msg, _ in res.error = msg }) {
                let ask = PBGetAccountPrefsRequest.with { $0.objectID = req.objectID.isEmpty ? projectPrefsID : req.objectID }
                res.wirePrefs = try await withAuthRetry { try await apiClient.getAccountPrefs(request: ask, headers: $0).value() }.object
            }
            event.accountPrefsFetched = res

        case .putAccountPrefs(let req):
            var res = PBCoreAccountPrefsPutResultEvent(); res.opID = rpc.opID
            do {
                let objectId = req.objectID.isEmpty ? projectPrefsID : req.objectID
                let env: PBEnvelope = if req.hasSealedEnvelope {
                    req.sealedEnvelope
                } else {
                    try await sealEnvelopeOp(
                        purpose: .prefs, requestId: objectId, seq: req.expectedRevision + 1, plaintext: try req.nextPrefs.serializedData()
                    ).envelope
                }
                let put = PBPutAccountPrefsRequest.with { $0.objectID = objectId; $0.expectedRevision = req.expectedRevision; $0.envelope = env }
                let reply = try await withAuthRetry { try await apiClient.putAccountPrefs(request: put, headers: $0).value() }
                res.applied = reply.applied
                if reply.hasObject { res.wireCurrent = reply.object }
            } catch SecurityFailure.conflict where !req.hasSealedEnvelope {
                res.sealConflict = true; res.error = errorText(SecurityFailure.conflict)
            } catch {
                res.error = errorText(error); report(error)
            }
            event.accountPrefsPutResult = res

        case .fetchDevices:
            var res = PBCoreDevicesFetchedEvent(); res.opID = rpc.opID
            await withRpcError({ msg, _ in res.error = msg }) {
                res.wireDevices = try await withAuthRetry { try await apiClient.listDevices(request: .init(), headers: $0).value() }.devices
            }
            event.devicesFetched = res

        case nil:
            break
        }
        return event
    }
}
