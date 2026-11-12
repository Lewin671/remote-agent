// Histories show a verified newest segment while the older prefix backfills.
// Only the contiguous prefix from sequence 1 establishes historical state;
// live authorization still comes from the current signed checkpoint.
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
        startLivePolling(sid)
        Task { await self.loadEvents(sid) }
    }

    func followersChanged(_ sid: String) {
        if followers(sid) > 0 {
            startLivePolling(sid)
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
        let idle = buffers.filter { isIdle($0.key) }.sorted {
            $0.value.useOrder < $1.value.useOrder
        }
        var bytes = idle.reduce(Int64(0)) { $0 + $1.value.bytes }
        var count = idle.count
        for (sid, b) in idle {
            if sessions[sid] == nil || count > historyIdleCount || bytes > historyIdleBytes
                || clock() - b.usedAt >= historyIdleMs
            {
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
        if let error = sessions[sid]?.historyError { eventFeed(sid).set(SessionEvents(error: error)); return }
        if buffers[sid] != nil || sessions[sid] == nil { return }
        let b = Buffer()
        used(b)
        buffers[sid] = b
        b.restoring = true
        // Restore only a bounded newest window before reading any older rows.
        // lastSeq and cache cursors are routing hints; ingest authenticates each event.
        let newest = await cache?.newestEvents(sessionId: sid) ?? []
        if buffers[sid] !== b { return }
        var broken = false
        if let first = newest.first?.seq, let last = newest.last?.seq {
            b.tailRequested = first > 1
            broken = !(await ingest(sid, newest, fillGaps: false, save: false, history: true))
            if buffers[sid] !== b { return }
            b.cachedThrough = last
            if !broken && first > 1 {
                // What the relay has after the cache need not wait for the
                // cached prefix: ask for it now, and verify the prefix meanwhile.
                Task { await self.fetchEvents(sid) }
                var after: UInt64 = 0
                while after + 1 < first {
                    let page = await cache?.eventPage(sessionId: sid, afterSeq: after) ?? []
                    if buffers[sid] !== b { return }
                    guard let row = page.last, row.seq > after else { break }
                    // Only an event of the cache's own decides that the cache is
                    // bad; one from the relay failing meanwhile stops only its tail.
                    if !(await ingest(sid, page, fillGaps: false, save: false, history: true)) {
                        broken = true
                        break
                    }
                    if buffers[sid] !== b { return }
                    after = row.seq
                    if page.count < cipherEventPageLimit { break }
                }
            }
        }
        if b.error == retiredHistoryError { b.restoring = false; publishEvents(sid, b); return }
        if broken {
            // As before, a cached envelope that fails authentication is discarded
            // so the cache's insert-once policy cannot preserve it indefinitely.
            await cache?.dropEvents(sessionId: sid)
            if buffers[sid] !== b { return }
            // A history of its own: a fetch of the newer events still in
            // flight for the discarded one ends without touching this one.
            let fresh = Buffer()
            used(fresh)
            buffers[sid] = fresh
            publishEvents(sid, fresh)
        } else {
            b.restoring = false
        }
        await fetchEvents(sid)
        trimHistory()
    }

    /// Show the newest network page first, then fill every gap from sequence 1.
    /// While the disk cache is still restoring, only what is newer than it is asked for.
    func fetchEvents(_ sid: String) async {
        guard let b = buffers[sid], sessions[sid] != nil, running else { return }
        if b.fetching || (b.restoring && b.cachedThrough == nil) {
            b.refetch = true
            return
        }
        b.fetching = true
        let gen = generation
        var newerOnly = false
        defer {
            b.fetching = false
            // A pass for the newer events that failed leaves the whole read asked for after it.
            if b.refetch && running && (gen != generation || newerOnly) && buffers[sid] === b {
                Task { await self.fetchEvents(sid) }
            }
            publishEvents(sid, b)
        }
        do {
            repeat {
                b.refetch = false
                newerOnly = b.restoring
                var end: UInt64?
                let latest = sessions[sid]?.lastSeq ?? 0
                var tail: UInt64?
                if newerOnly {
                    // From the end of the cache, or the server's newest page when
                    // that is further on; asked once even when the metadata says
                    // there is nothing newer, as it may be old.
                    tail = max(b.cachedThrough ?? 0, latest > 500 ? latest - 500 : 0)
                } else if latest > UInt64(b.events.count) + 500 && b.error == nil && heldThrough(latest - 500, b) < latest {
                    tail = latest - 500
                }
                if let start = tail {
                    b.tailRequested = true
                    var cursor = heldThrough(start, b)
                    while true {
                        guard let page = try await historyPage(sid, after: cursor, limit: pageLimit(after: cursor, b), buffer: b, generation: gen) else { return }
                        let next = page.events.last?.seq ?? cursor
                        if !page.more { end = next }
                        if !page.more || b.error != nil || next <= cursor { break }
                        cursor = heldThrough(next, b)
                    }
                }
                if newerOnly {
                    // The rest waits for the cached prefix (`loadEvents` asks again).
                    b.fetchError = nil
                    continue
                }
                while true {
                    let before = UInt64(b.events.count)
                    // The tail already reached the server's end. Once the prefix
                    // joins it, avoid downloading the same tail a second time.
                    if let end, before >= end && before >= latest && b.pending.isEmpty { break }
                    guard let page = try await historyPage(sid, after: before, limit: pageLimit(after: before, b), buffer: b, generation: gen) else { return }
                    if !page.more { end = page.events.last?.seq ?? before }
                    // An unreadable event in the tail stops only the tail: the prefix still
                    // advances to it, as far as it makes progress.
                    if !page.more || UInt64(b.events.count) == before { break }
                }
                if !running || gen != generation || buffers[sid] !== b { return }
                let missing = !b.pending.isEmpty || UInt64(b.events.count) < latest
                b.loaded = !missing && b.error == nil
                b.fetchError = missing && b.error == nil ? L10n.errorsIosHistoryIsDiscontinuousRetryLoading() : nil
            } while b.refetch
        } catch {
            if !running || gen != generation || buffers[sid] !== b { return }
            report(error)
            b.fetchError = L10n.errorsIosCouldNotLoadHistory(value: String(describing: errorText(error)))
        }
    }

    /// The end of the run of events held from `seq` on, in the prefix or pending.
    private func heldThrough(_ seq: UInt64, _ b: Buffer) -> UInt64 {
        var last = max(seq, UInt64(b.events.count))
        while b.pending[last + 1] != nil { last += 1 }
        return last
    }

    /// A page after `after` that stops before the next event already held (from
    /// the cache, the newest page or a live event), so none of it comes twice.
    private func pageLimit(after: UInt64, _ b: Buffer) -> UInt64 {
        b.pending.keys.filter { $0 > after + 1 }.min().map { min(500, $0 - after - 1) } ?? 500
    }

    private func historyPage(
        _ sid: String, after: UInt64, limit: UInt64 = 500, buffer b: Buffer, generation gen: Int
    ) async throws -> PBGetEventsResponse? {
        if !running || gen != generation || buffers[sid] !== b { return nil }
        var ask = PBGetEventsRequest()
        ask.sessionID = sid
        ask.afterSeq = after
        ask.limit = UInt32(limit)
        let response = try await api.client.getEvents(request: ask, headers: Api.headers(token: token)).value()
        if !running || gen != generation || buffers[sid] !== b { return nil }
        await ingest(sid, response.events, fillGaps: false, save: true, history: true)
        if !running || gen != generation || buffers[sid] !== b { return nil }
        return response
    }

    /// Whether every event given opened: false when one did not and the history stops before it.
    @discardableResult
    func ingest(
        _ sid: String, _ stored: [PBStoredEvent], fillGaps: Bool, save: Bool, history: Bool = true
    ) async -> Bool {
        guard let b = buffers[sid], let s = sessions[sid], let key = sessionKeys[sid] else {
            return true
        }
        var opens = true
        var opened: [PBStoredEvent] = []
        let batchSize = DeviceSecurityClient.historyBatchSize
        var historyResults: [Int: Result<(DeviceSecurity.Header, Data), Error>] = [:]
        for (index, se) in stored.enumerated() {
            if history && index % batchSize == 0 {
                let end = min(index + batchSize, stored.count)
                let indices = (index..<end).filter {
                    let event = stored[$0]
                    return event.seq > UInt64(b.events.count) && b.pending[event.seq] == nil
                        && (try? PBHeader(serializedBytes: event.envelope.header).version) != 1
                }
                historyResults.removeAll(keepingCapacity: true)
                if !indices.isEmpty {
                    do {
                        let results = try await securitySession().openHistoryBatch(
                            indices.map { stored[$0].envelope }, sessionSeed: key)
                        historyResults = Dictionary(uniqueKeysWithValues: zip(indices, results))
                    } catch {
                        historyResults = Dictionary(uniqueKeysWithValues: indices.map { ($0, .failure(error)) })
                    }
                    // The actor yielded while opening: logout, eviction or a
                    // concurrent ingest may have replaced or advanced this buffer.
                    if buffers[sid] !== b { return opens }
                }
            }
            if se.seq <= UInt64(b.events.count) || b.pending[se.seq] != nil { continue }
            do {
                try rejectRetiredHistory(se.envelope)
                let result: (DeviceSecurity.Header, Data)
                if history {
                    guard let value = historyResults[index] else { throw DeviceSecurity.Failure.storage }
                    result = try value.get()
                } else {
                    result = try await securitySession().openCurrent(se.envelope, sessionSeed: key)
                }
                let h = projection(result.0), plain = result.1
                try Envelopes.expect(
                    h, try await historicalPlace(Place(.event, machineId: s.machineId, sessionId: sid, seq: se.seq),header:h,history:history))
                guard let ev = try? PBEvent(serializedBytes: plain) else {
                    throw OpenError(L10nMessage.errorsIosCryptoMalformedContent())
                }
                if ev.seq != se.seq || ev.sessionID != sid { throw OpenError(L10n.errorsTheEventSPositionWasChanged()) }
                if buffers[sid] !== b { return opens }
                if se.seq <= UInt64(b.events.count) || b.pending[se.seq] != nil { continue }
                b.verifiedSeqs.insert(se.seq)
                b.pending[se.seq] = ev
                opened.append(se)
                // What is held is the event as opened; its ciphertext may be packed.
                b.bytes += Int64(plain.count) + 256
            } catch let unavailable as RecoveryHistoryUnavailable {
                do {
                    try Envelopes.expect(
                        projection(unavailable.header),
                        try await historicalPlace(Place(.event, machineId: s.machineId, sessionId: sid, seq: se.seq),header:projection(unavailable.header),history:history))
                    if buffers[sid] !== b { return opens }
                    if se.seq <= UInt64(b.events.count) || b.pending[se.seq] != nil { continue }
                    var slot = PBEvent()
                    slot.seq = se.seq
                    slot.sessionID = sid
                    b.pending[se.seq] = slot
                    b.unavailableSeqs.insert(se.seq)
                    b.bytes += 256
                    opened.append(se)
                } catch {
                    report(error)
                    b.error = error is UnsupportedHistoryError ? errorText(error) : L10n.sessionHistoryUnreadable()
                    opens = false
                    break
                }
            } catch {
                report(error)
                b.error = error is UnsupportedHistoryError ? errorText(error) : L10n.sessionHistoryUnreadable()
                opens = false
                break
            }
        }
        var added: [PBEvent] = []
        while let ev = b.pending.removeValue(forKey: UInt64(b.events.count + added.count) + 1) {
            added.append(ev)
        }
        b.events = b.events.appending(added)
        b.verifiedEvents = b.verifiedEvents.appending(
            added.filter { b.verifiedSeqs.contains($0.seq) })
        if save && !opened.isEmpty { await cache?.putEvents(sessionId: sid, events: opened) }
        if buffers[sid] !== b { return opens }
        // The cache write awaited: newer metadata may have arrived, or the
        // session gone. Only the event progress is ours to update.
        if var cur = sessions[sid] {
            cur.lastSeq = max(cur.lastSeq, UInt64(b.events.count))
            sessions[sid] = cur
        }
        if !b.pending.isEmpty { b.loaded = false }
        // Once the prefix reaches the newest segment there is one list again;
        // a later live gap must not shrink the chat to what follows it.
        if b.pending.isEmpty { b.tailRequested = false }
        if fillGaps && !b.pending.isEmpty { Task { await self.fetchEvents(sid) } }
        publishEvents(sid, b)
        publish()
        trimHistory()
        return opens
    }

    func publishEvents(_ sid: String, _ b: Buffer) {
        if buffers[sid] !== b { return }
        var shown = b.events
        if b.tailRequested && !b.pending.isEmpty {
            // Like the web client, show one contiguous suffix, not a concatenation
            // across a gap. The reducer's state prefix remains separate below.
            let last = b.pending.keys.max()!
            var first = last
            while first > 1 && b.pending[first - 1] != nil { first -= 1 }
            shown = PagedList((first...last).compactMap { b.pending[$0] })
        }
        var snapshot = SessionEvents(
            events: shown, loaded: b.loaded, error: b.error, fetchError: b.fetchError)
        snapshot.stateEvents = b.events
        snapshot.historyPartial = !b.pending.isEmpty || UInt64(b.events.count) < (sessions[sid]?.lastSeq ?? 0)
        snapshot.loaded = b.loaded && !snapshot.historyPartial
        snapshot.live = liveSnapshots[sid]
        snapshot.liveError = liveErrors[sid] ?? (sessions[sid]?.checkpointStatus == "waiting" ? L10n.errorsWaitingForTheOwningComputerToUpdateTheCurrent() : nil)
        snapshot.verifiedEvents = b.verifiedEvents
        snapshot.unavailableCount = b.unavailableSeqs.count
        eventFeed(sid).set(snapshot)
    }
}
