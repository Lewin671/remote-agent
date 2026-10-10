// Session histories: read from the cache, then from the server after the
// last contiguous event, with pushed events that leave a gap held back
// until the gap is fetched (architecture §10 「客户端同步」). Complete
// histories are only kept for whoever follows them (「后台和内存」).
import Foundation
import RAProto

extension Remote {
    /// A session's events; loads them from the cache and the server on first
    /// use. The same feed is returned for a session each time. While someone
    /// follows it (`updates()`), the history is kept whole; left alone it is
    /// reclaimed after a while and the feed goes back to empty, to load again
    /// when it is next followed or asked for here.
    public nonisolated func events(_ sid: String) -> Feed<SessionEvents> {
        let feed = eventFeed(sid)
        post(.touch(sid))
        return feed
    }

    /// Asks for missing events again, e.g. after one failed to open.
    public nonisolated func retryEvents(_ sid: String) { post(.retryEvents(sid)) }

    nonisolated func eventFeed(_ sid: String) -> Feed<SessionEvents> {
        eventFeeds.withLock { feeds in
            if let f = feeds[sid] { return f }
            let f = Feed(SessionEvents()) { [weak self] in self?.post(.followers(sid)) }
            feeds[sid] = f
            return f
        }
    }

    private func followers(_ sid: String) -> Int {
        eventFeeds.withLock { $0[sid]?.followers ?? 0 }
    }

    /// A history that was handed out and that no one follows now.
    private func isIdle(_ sid: String) -> Bool {
        eventFeeds.withLock { $0[sid]?.followers } == 0
    }

    func touch(_ sid: String) {
        if historySweep == nil {
            let sleep = self.sleep
            let every = max(historyIdleMs, 1)
            historySweep = Task { [weak self] in
                while (try? await sleep(every)) != nil, !Task.isCancelled {
                    guard let self else { return }
                    await self.trimHistory()
                }
            }
        }
        if let b = buffers[sid] { used(b) }
        Task { await self.loadEvents(sid) }
    }

    func followersChanged(_ sid: String) {
        if followers(sid) > 0 {
            Task { await self.loadEvents(sid) }
        } else {
            if let b = buffers[sid] { used(b) }
            trimHistory()
        }
    }

    /// Notes that a history was just used. The clock says how long ago for the
    /// idle limit; the order of uses decides which goes first, since two uses
    /// in the same millisecond are still one after the other.
    private func used(_ b: Buffer) {
        b.usedAt = clock()
        useSeq += 1
        b.useOrder = useSeq
    }

    func trimHistory() {
        let idle = buffers.filter { isIdle($0.key) }.sorted { $0.value.useOrder < $1.value.useOrder }
        var bytes = idle.reduce(Int64(0)) { $0 + $1.value.bytes }
        var count = idle.count
        for (sid, b) in idle {
            if sessions[sid] == nil || count > historyIdleCount || bytes > historyIdleBytes
                || clock() - b.usedAt >= historyIdleMs {
                buffers[sid] = nil
                // The feed stays the same one for consumers about to follow
                // it; following it reloads a reclaimed history.
                eventFeed(sid).set(SessionEvents())
                count -= 1
                bytes -= b.bytes
            }
        }
    }

    func loadEvents(_ sid: String) async {
        if buffers[sid] != nil || sessions[sid] == nil { return }
        let b = Buffer()
        used(b)
        buffers[sid] = b
        let cached = await cache?.events(sessionId: sid) ?? []
        if buffers[sid] !== b { return }
        if !cached.isEmpty {
            await ingest(sid, cached, fillGaps: false, save: false)
            if buffers[sid] !== b { return }
            // A cached copy that does not open is dropped and the history
            // fetched whole (architecture §10 「后台和内存」): the cache keeps
            // an event as first stored, so the bad one would stay for good.
            if b.error != nil {
                await cache?.dropEvents(sessionId: sid)
                if buffers[sid] !== b { return }
                b.events = PagedList()
                b.pending = [:]
                b.bytes = 0
                b.error = nil
                publishEvents(sid, b)
            }
        }
        await fetchEvents(sid)
        trimHistory()
    }

    /// Fetches everything after the last contiguous event.
    func fetchEvents(_ sid: String) async {
        guard let b = buffers[sid] else { return }
        if sessions[sid] == nil || !running { return }
        if b.fetching {
            b.refetch = true
            return
        }
        b.fetching = true
        let gen = generation
        defer {
            b.fetching = false
            if b.refetch && running && gen != generation { Task { await self.fetchEvents(sid) } }
            publishEvents(sid, b)
        }
        do {
            repeat {
                b.refetch = false
                while true {
                    if !running || gen != generation || buffers[sid] !== b { return }
                    let before = b.events.count
                    var ask = PBGetEventsRequest()
                    ask.sessionID = sid
                    ask.afterSeq = UInt64(before)
                    ask.limit = 500
                    let res = try await api.client.getEvents(request: ask, headers: Api.headers(token: token)).value()
                    if !running || gen != generation || buffers[sid] !== b { return }
                    await ingest(sid, res.events, fillGaps: false, save: true)
                    // An event that does not open stops progress; asking for the
                    // same page again would not help.
                    if !res.more || b.events.count == before { break }
                }
            } while b.refetch
            b.loaded = true
            b.fetchError = nil
        } catch {
            if !running || gen != generation { return }
            report(error)
            // Shown on the session with a retry; the next Ready fetches again too.
            b.fetchError = "没能读取历史：" + errorText(error)
        }
    }

    func ingest(_ sid: String, _ stored: [PBStoredEvent], fillGaps: Bool, save: Bool) async {
        guard let b = buffers[sid], let s = sessions[sid], let key = sessionKeys[sid] else { return }
        var opened: [PBStoredEvent] = []
        for se in stored {
            if se.seq <= UInt64(b.events.count) || b.pending[se.seq] != nil { continue }
            do {
                let (h, plain) = try Envelopes.open(key: key, envelope: se.envelope)
                try Envelopes.expect(h, Place(.event, machineId: s.machineId, sessionId: sid, seq: se.seq))
                guard let ev = try? PBEvent(serializedBytes: plain) else { throw OpenError("malformed content") }
                if ev.seq != se.seq || ev.sessionID != sid { throw OpenError("事件的位置被改动") }
                b.pending[se.seq] = ev
                opened.append(se)
                // What is held is the event as opened; its ciphertext may be packed.
                b.bytes += Int64(plain.count) + 256
            } catch {
                report(error)
                b.error = "有一条记录读不出来，历史停在这里，后面的内容和状态可能不全"
                break
            }
        }
        var added: [PBEvent] = []
        while let ev = b.pending.removeValue(forKey: UInt64(b.events.count + added.count) + 1) {
            added.append(ev)
        }
        b.events = b.events.appending(added)
        if save && !opened.isEmpty { await cache?.putEvents(sessionId: sid, events: opened) }
        if buffers[sid] !== b { return }
        // The cache write awaited: newer metadata may have arrived, or the
        // session gone. Only the event progress is ours to update.
        if var cur = sessions[sid] {
            cur.lastSeq = max(cur.lastSeq, UInt64(b.events.count))
            sessions[sid] = cur
        }
        if fillGaps && !b.pending.isEmpty { Task { await self.fetchEvents(sid) } }
        publishEvents(sid, b)
        publish()
        trimHistory()
    }

    private func publishEvents(_ sid: String, _ b: Buffer) {
        if buffers[sid] !== b { return }
        eventFeed(sid).set(SessionEvents(events: b.events, loaded: b.loaded, error: b.error, fetchError: b.fetchError))
    }
}
