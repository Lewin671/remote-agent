// The lists Remote holds: machines, sessions a page at a time, the account's
// project preferences and its devices (architecture §8 「会话列表分页」, §10
// 「客户端同步」).
import Connect
import Foundation
import RAProto
import os

extension Remote {
    func dropMachine(_ id: String) async {
        // However it was found gone, pushed or by a listing: a session of its that is being
        // opened for a page or a lookup asked for before must not be taken after this.
        bump(&removedPushes, id)
        machines[id] = nil
        trustedMetadata["machine:" + id] = nil
        let sids = sessions.values.filter { $0.machineId == id }.map(\.id)
        for sid in sids {
            sessions[sid] = nil
            sessionKeys[sid] = nil
            listed.remove(sid)
        }
        // The cache only lists the first page: the others held here go one by one.
        for sid in sids { await cache?.removeSession(id: sid) }
        await cache?.removeMachine(id: id)
    }

    /// Reloads machines and the first page of sessions: every session that
    /// has not ended and the latest ended ones. Pushes that arrive while the
    /// lists are in flight are newer and win. Returns whether the lists are
    /// in place. When they could not be fetched `RemoteState.sessionsError`
    /// says why, and it is tried again by itself, ever more slowly, while the
    /// connection holds.
    @discardableResult
    public func refresh() async -> Bool {
        refreshRun += 1
        let run = refreshRun
        listRetry?.task.cancel()
        listRetry = nil
        listsInFlight += 1
        defer {
            listsInFlight -= 1
            if listsInFlight == 0 {
                let waiting = listWaiters
                listWaiters = []
                for w in waiting { w.resume() }
            }
        }
        do {
            try await refreshTrustedDirectory()
            let mp = machinePushes
            let sp = sessionPushes
            let rp = removedPushes
            let ms = try await api.client.listMachines(
                request: .init(), headers: Api.headers(token: token)
            ).value()
            var ask = PBListRemoteSessionsRequest()
            ask.limit = UInt32(sessionPage)
            let ss = try await api.client.listRemoteSessions(
                request: ask, headers: Api.headers(token: token)
            ).value()
            // Putting the lists in place awaits the cache: one at a time.
            await acquire(applying)
            defer { release(applying) }
            // Asked for again since: that answer is the newer one, and this one would undo it.
            if run != refreshRun { return false }
            // The listing starts over: a later page still in flight continued the old one.
            listing += 1
            // Forget what the server no longer has, e.g. a machine another
            // device removed while this one was offline.
            let mids = Set(ms.machines.map(\.id))
            let sids = Set(ss.sessions.map(\.sessionID))
            let end =
                ss.hasNext
                ? SessionPlace(endedMs: ss.next.endedMs, sessionId: ss.next.sessionID) : nil
            for id in Array(machines.keys)
            where !mids.contains(id) && !pushed(machinePushes, mp, id) {
                await dropMachine(id)
            }
            // A session held here that this page does not have (architecture §8
            // 「重新连接后」): one that ended and comes after the page's end is
            // a later page's and stays; any other goes. Without a later page
            // the page is the whole listing, and what it lacks does not exist.
            for v in Array(sessions.values) {
                if sids.contains(v.id) || pushed(sessionPushes, sp, v.id) { continue }
                let hasMachine = mids.contains(v.machineId) || machines[v.machineId] != nil
                if let end, hasMachine,
                    afterPlace(endedMs: v.listedEndedMs, sessionId: v.id, place: end)
                {
                    continue
                }
                sessions[v.id] = nil
                sessionKeys[v.id] = nil
                listed.remove(v.id)
                // Its events go with it only when it is known not to exist; else it
                // may be on a later page with what is held here out of date.
                if end == nil || !hasMachine {
                    await cache?.removeSession(id: v.id)
                } else {
                    await cache?.unlistSession(id: v.id)
                }
            }
            // The cache keeps the first page only.
            for id in Array(listed) where !sids.contains(id) && !pushed(sessionPushes, sp, id) {
                listed.remove(id)
                await cache?.unlistSession(id: id)
            }
            for m in ms.machines where !pushed(removedPushes, rp, m.id) {
                await putMachine(m, staleOnline: pushed(machinePushes, mp, m.id))
            }
            for s in ss.sessions where !pushed(removedPushes, rp, s.machineID) {
                await putSession(s, publish: false)
            }
            next = end
            firstEnd = end
            firstPage = true
            synced = true
            pageError = ""
            listBackoff = listRetryMs
            loaded = true
            publish()
            return true
        } catch {
            // Given up on by whoever asked: nothing to say about the lists.
            if Task.isCancelled { return false }
            report(error)
            // What is held stays as it was, with the cursor it had.
            if run == refreshRun {
                pageError = errorText(error)
                retryListing(run)
            }
            publish()
            return false
        }
    }

    /// Asks for the lists again after a while, unless they were asked for meanwhile or the connection is gone.
    private func retryListing(_ run: Int) {
        let wait = listBackoff
        listBackoff = min(listBackoff * 2, 30_000)
        listRetry = schedule(wait) { me, id in
            guard me.listRetry?.id == id else { return }
            // Not this timer's to cancel from refresh while it runs.
            me.listRetry = nil
            if me.running && me.conn == .online && run == me.refreshRun { await me.refresh() }
        }
    }

    private func pushed(_ now: [String: Int], _ then: [String: Int], _ id: String) -> Bool {
        (now[id] ?? 0) != (then[id] ?? 0)
    }

    /// Whether the listing's first page, as last fetched, would hold this session: the cache keeps that page.
    func onFirstPage(_ rs: PBRemoteSession) -> Bool {
        guard firstPage else { return false }
        guard let firstEnd else { return true }
        return !afterPlace(endedMs: rs.endedMs, sessionId: rs.sessionID, place: firstEnd)
    }

    /// Fetches the next page of ended sessions, which is the first one while
    /// that has not come (`RemoteState.sessionsListed`); calls made while one
    /// is in flight wait for that one. Returns whether there are more after it.
    /// Throws when the page could not be fetched, which `RemoteState.sessionsError`
    /// then says until a page comes.
    @discardableResult
    public func loadMoreSessions() async throws -> Bool {
        if let pageJob { return try await pageJob.value }
        let job = Task { () throws -> Bool in
            // Whoever waits for it finds it over: nothing is loading any more.
            defer {
                self.pageJob = nil
                self.publish()
            }
            return try await self.fetchPage()
        }
        pageJob = job
        publish()
        return try await job.value
    }

    /// Fetches every page that is left, e.g. to filter by title; calls made meanwhile wait for the same run.
    public func loadAllSessions() async throws {
        if let allJob { return try await allJob.value }
        let job = Task { () throws -> Void in
            defer { self.allJob = nil }
            while !self.firstPage || self.next != nil { try await self.loadMoreSessions() }
        }
        allJob = job
        try await job.value
    }

    private func fetchPage() async throws -> Bool {
        while true {
            // A first page on its way, or being put in place, has the cursor to go on from;
            // where none was held, it is the page that was asked for.
            let listedBefore = firstPage
            while listsInFlight > 0 {
                await withCheckedContinuation { listWaiters.append($0) }
            }
            if !listedBefore && firstPage { return next != nil }
            if conn != .online {
                // Nothing is asked for in the background, or over a connection that is gone.
                pageError = "没有连上服务器"
                throw CommandError(CommandError.unavailable, pageError)
            }
            if !firstPage {
                // The first page never came: it is the one to fetch.
                if await refresh() { return next != nil }
                // Asked for again meanwhile: that one is waited for above.
                if listsInFlight > 0 || firstPage { continue }
                throw CommandError(
                    CommandError.unavailable, pageError.isEmpty ? "没能读取会话列表" : pageError)
            }
            guard let after = next else { return false }
            let epoch = listing
            let rp = removedPushes
            var ask = PBListRemoteSessionsRequest()
            ask.after.endedMs = after.endedMs
            ask.after.sessionID = after.sessionId
            ask.limit = UInt32(sessionPage)
            let res: PBListRemoteSessionsResponse
            do {
                let tok = try await currentToken()
                res = try await api.client.listRemoteSessions(
                    request: ask, headers: Api.headers(token: tok)
                ).value()
            } catch {
                // The first page was fetched again meanwhile: what failed continued the listing before it.
                if epoch != listing { continue }
                log("remote-agent: " + errorText(error))
                pageError = errorText(error)
                throw error
            }
            // The first page was fetched again meanwhile: this page continued the
            // listing before it. The caller still wants a page: the one after the new cursor.
            if epoch != listing { continue }
            // Putting a session awaits the cache: the listing may start over meanwhile too.
            for s in res.sessions where epoch == listing && accepts(s, rp) {
                await putSession(s, save: false, publish: false)
            }
            if epoch != listing { continue }
            next =
                res.hasNext
                ? SessionPlace(endedMs: res.next.endedMs, sessionId: res.next.sessionID) : nil
            pageError = ""
            return next != nil
        }
    }

    /// Whether a session the server answered a request with is taken: not
    /// one of a machine removed since the request was made (`removalsThen`),
    /// nor of one the listing of machines does not have.
    private func accepts(_ rs: PBRemoteSession, _ removalsThen: [String: Int]) -> Bool {
        !pushed(removedPushes, removalsThen, rs.machineID)
            && (!firstPage || machines[rs.machineID] != nil)
    }

    /// Fetches one session by its ID when it is not held here: one opened
    /// from a notification or a link may be on a page not fetched yet.
    /// Returns whether it is held now; false means the server does not have
    /// it. Throws when the server could not be asked.
    public func fetchSession(_ id: String) async throws -> Bool {
        if sessions[id] != nil { return true }
        if let job = byId[id] { return try await job.value }
        let job = Task { () throws -> Bool in
            defer { self.byId[id] = nil }
            let rp = self.removedPushes
            var ask = PBListRemoteSessionsRequest()
            ask.sessionIds = [id]
            let tok = try await self.currentToken()
            let res = try await self.api.client.listRemoteSessions(
                request: ask, headers: Api.headers(token: tok)
            ).value()
            // A session of a machine removed meanwhile, or one the listing of
            // machines no longer has, is as good as gone.
            if let rs = res.sessions.first(where: { $0.sessionID == id }), self.accepts(rs, rp) {
                await self.putSession(rs, save: self.onFirstPage(rs))
            }
            return self.sessions[id] != nil
        }
        byId[id] = job
        return try await job.value
    }

    func machineRemoved() async {
        invalidateDirectoryRead()
        _ = await refresh()
    }

    func machineUpdated(_ machine: PBMachine) async {
        bump(&machinePushes, machine.id)
        if machine.hasMeta { invalidateDirectoryRead() }
        do { try await checkDirectory(machine.id, envelopes: machine.hasMeta ? [machine.meta] : []) }
        catch { report(error) }
        await putMachine(machine)
    }

    func putMachine(_ incoming: PBMachine, staleOnline: Bool = false, save: Bool = true) async {
        guard trustedMembers[incoming.id]?.kind == 1 || archiveMachines.contains(incoming.id) else { return }
        let m = incoming
        let old = machines[m.id]
        var view = old ?? MachineView(id: m.id, online: m.online, lastSeenMs: m.lastSeenMs)
        if !staleOnline { view.online = m.online }
        if m.lastSeenMs > view.lastSeenMs { view.lastSeenMs = m.lastSeenMs }
        var store = old == nil
        var verifiedMeta = false
        if m.hasMeta {
            do {
                let (h, meta) = try await openSecureMessage(
                    envelope: m.meta, want: Place(.machineMeta, machineId: m.id),
                    as: PBMachineMeta.self)
                if h.seq >= view.metaRev {
                    view.meta = meta
                    view.metaRev = h.seq
                    verifiedMeta = true
                    store = true
                }
            } catch {
                report(error)
            }
        }
        guard (trustedMembers[m.id]?.kind == 1 || archiveMachines.contains(m.id)), (machines[m.id]?.metaRev ?? 0) <= view.metaRev else {
            return
        }
        machines[m.id] = view
        if verifiedMeta { trustedMetadata["machine:" + m.id] = m.meta }
        if save && store { await cache?.putMachine(m) }
        publish()
    }

    func putSession(_ incoming: PBRemoteSession, save: Bool = true, publish announce: Bool = true)
        async
    {
        guard trustedMembers[incoming.machineID]?.kind == 1 || archiveMachines.contains(incoming.machineID) else { return }
        let rs = incoming
        let sid = rs.sessionID
        let removal = removedPushes[rs.machineID] ?? 0
        do {
            var view: SessionView
            if let held = sessions[sid], held.machineId == rs.machineID {
                view = held
            } else {
                if let held = sessions[sid] {
                    let (_, info) = try await openSecureMessage(envelope:rs.meta,want:Place(.sessionMeta,machineId:rs.machineID,sessionId:sid),as:PBSession.self)
                    let origins = try await securitySession().replacementOrigins(target:rs.machineID,proofs:info.machineReplacements)
                    guard origins.contains(held.machineId) else {throw OpenError("会话记录的机器被改动")}
                }
                let route = try PBHeader(serializedBytes: rs.key.header)
                let h: PBHeader
                let raw: Data
                if route.version == 1 {
                    guard let keys else {
                        throw OpenError("这份旧历史需要账户根密钥，本机没有")
                    }
                    (h, raw) = try Envelopes.open(key: keys.wrap, envelope: rs.key)
                } else {
                    let opened = try await securitySession().openHistory(rs.key)
                    h = projection(opened.0)
                    raw = opened.1
                }
                try Envelopes.expect(h, Place(.sessionKey, machineId: rs.machineID, sessionId: sid))
                if raw.count != keySize { throw OpenError("bad session key") }
                guard (removedPushes[rs.machineID] ?? 0) == removal,
                    (trustedMembers[rs.machineID]?.kind == 1 || archiveMachines.contains(rs.machineID))
                else { return }
                sessionKeys[sid] = raw
                view = SessionView(id: sid, machineId: rs.machineID)
            }
            if view.machineId != rs.machineID { throw OpenError("会话记录的机器被改动") }
            view.lastSeq = max(view.lastSeq, rs.lastSeq)
            view.updatedMs = max(view.updatedMs, rs.updatedMs)
            var store = sessions[sid] == nil
            if store { view.listedEndedMs = rs.endedMs }
            if rs.hasMeta {
                guard sessionKeys[sid] != nil else { throw OpenError("bad session seed") }
                let (h, info) = try await openSecureMessage(
                    envelope: rs.meta,
                    want: Place(.sessionMeta, machineId: rs.machineID, sessionId: sid),
                    as: PBSession.self)
                if h.seq >= view.metaRev {
                    // The server stores when it ended with the metadata.
                    view.info = info
                    if !info.currentCheckpoint.isEmpty {
                        let cp=try PBLiveSnapshot(serializedBytes:info.currentCheckpoint)
                        guard cp.session.id == sid, cp.session.lastSeq <= info.lastSeq, cp.actions.allSatisfy({$0.sessionID==sid && $0.seq<=info.lastSeq}) else {throw DeviceSecurity.Failure.invalid}
                        view.checkpoint=cp;view.checkpointStatus="ready"
                    } else {view.checkpointStatus="waiting"}
                    view.metaRev = h.seq
                    view.listedEndedMs = rs.endedMs
                    store = true
                }
            }
            guard (removedPushes[rs.machineID] ?? 0) == removal,
                (trustedMembers[rs.machineID]?.kind == 1 || archiveMachines.contains(rs.machineID)),
                (sessions[sid]?.metaRev ?? 0) <= view.metaRev
            else { return }
            sessions[sid] = view
            clearEndedLiveState(sid)
            if eventFeeds.withLock({ $0[sid]?.followers ?? 0 }) > 0 {
                startLivePolling(sid)
                Task { await self.loadEvents(sid) }
            }
            if save && store {
                listed.insert(sid)
                await cache?.putSession(rs)
            } else if !save {
                await cache?.keepOwner(rs)
            }
        } catch {
            report(error)
            report(error)
        }
        if announce { publish() }
    }

    // --- account preferences and devices ---

    /// Reads the preferences. With no copy of them here nothing of them can
    /// be changed, so a read that fails then is tried again by itself, ever
    /// more slowly, while the connection holds.
    func loadPrefs() async {
        prefsRetry?.task.cancel()
        prefsRetry = nil
        do {
            try await refreshTrustedDirectory()
            try await readTrustedPrefs()
            prefsBackoff = listRetryMs
        } catch {
            report(error)
            let wait = prefsBackoff
            prefsBackoff = min(prefsBackoff * 2, 30_000)
            prefsRetry = schedule(wait) { me, id in
                guard me.prefsRetry?.id == id else { return }
                me.prefsRetry = nil
                if me.running && me.conn == .online { await me.loadPrefs() }
            }
        }
    }
    func readTrustedPrefs() async throws {
        guard let env = trustedMetadata["prefs"] else {
            if prefs == nil { prefs = .empty }
            publish()
            return
        }
        let (h, value) = try await openSecureMessage(
            envelope: env, want: Place(.accountPrefs, requestId: projectPrefsID),
            as: PBProjectPrefs.self)
        if prefs == nil || h.seq >= prefs!.revision {
            prefs = PrefsObject(revision: h.seq, prefs: value)
            publish()
        }
    }
    func putPrefs(_ object: PBAccountPrefsObject, save: Bool = true) async {
        if save {
            invalidateDirectoryRead()
            await loadPrefs()
            return
        }
        guard object.hasEnvelope, !object.envelope.securityHeader.isEmpty else { return }
        do {
            let (h, value) = try await openSecureMessage(
                envelope: object.envelope, want: Place(.accountPrefs, requestId: projectPrefsID),
                as: PBProjectPrefs.self)
            if prefs == nil { prefs = PrefsObject(revision: h.seq, prefs: value) }
        } catch { report(error) }
    }
    public func changePrefs(_ op: PrefsOp) async throws {
        guard conn == .online else { throw CommandError("unavailable", "没有连上服务器，连上后才能修改") }
        let operation = newRequestId()
        for _ in 0..<5 {
            try await refreshTrustedDirectory()
            try await readTrustedPrefs()
            let base = prefs ?? .empty
            let next = applyPrefsOp(base: base.prefs, op: op, opId: operation, now: clock())
            do {
                let env = try await securitySession().seal(
                    purpose: .prefs, request: projectPrefsID, seq: base.revision + 1,
                    plaintext: next.serializedData())
                var request = PBPutAccountPrefsRequest()
                request.objectID = projectPrefsID
                request.expectedRevision = base.revision
                request.envelope = env
                let result = try await api.client.putAccountPrefs(
                    request: request, headers: Api.headers(token: try await currentToken())
                ).value()
                if !result.applied { continue }
                if prefs == nil || prefs!.revision <= base.revision + 1 {
                    prefs = PrefsObject(revision: base.revision + 1, prefs: next)
                    publish()
                }
                try await refreshTrustedDirectory()
                try await readTrustedPrefs()
                return
            } catch DeviceSecurity.Failure.conflict { continue }
        }
        throw PrefsConflictError()
    }
    public func refreshDevices() async { await loadDevices() }
    func loadDevices() async {
        do {
            try await refreshTrustedDirectory()
        } catch {
            report(error)
        }
        publish()
    }
    func deviceRemoved(_ id: String) async {
        bump(&devicePushes, id)
        invalidateDirectoryRead()
        await loadDevices()
    }

    func putDevice(_ ignored: PBDevice) async {
        invalidateDirectoryRead()
        await loadDevices()
    }

    /// Names this client. Device metadata is self-only in protocol 10, so a
    /// name for another member can only be set on that member's own device.
    public func renameDevice(_ id: String, name: String) async throws {
        guard id == account.deviceId else {
            throw CommandError("failed_precondition", "设备名称只能由该设备自己设置")
        }
        try await changeDeviceMeta(id) { $0.name = name }
    }

    /// Says what this client is and runs on, keeping the name it was given.
    public func describeSelf(platform: String, description: String) async throws {
        try await changeDeviceMeta(account.deviceId) {
            $0.platform = platform
            $0.description_p = description
        }
    }

    func changeDeviceMeta(_ id: String, _ change: (inout PBDeviceMeta) -> Void) async throws
    {
        for _ in 0..<5 {
            try await refreshTrustedDirectory()
            guard let base = devices[id] else { throw CommandError("not_found", "没有这台设备") }
            var meta = base.meta ?? PBDeviceMeta()
            change(&meta)
            do {
                let env = try await securitySession().seal(
                    purpose: .deviceMeta, request: id, seq: base.metaRev + 1,
                    plaintext: meta.serializedData())
                var request = PBPutDeviceMetaRequest()
                request.deviceID = id
                request.expectedRevision = base.metaRev
                request.meta = env
                let result = try await api.client.putDeviceMeta(
                    request: request, headers: Api.headers(token: try await currentToken())
                ).value()
                if !result.applied { continue }
                try await refreshTrustedDirectory()
                return
            } catch DeviceSecurity.Failure.conflict { continue }
        }
        throw CommandError("aborted", "设备信息正在变化，请重试")
    }
}
