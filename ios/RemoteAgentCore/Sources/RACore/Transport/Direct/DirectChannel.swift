import Foundation
import RAProto

private let frameBytes = 16 << 10
private let payloadBytes = frameBytes - 128

struct DirectChannelOptions: Sendable {
    var peer: String
    var connection: PeerWire.Bytes
    var use: Int
    var generation: UInt64
    var deadline: UInt64
    var budget: DirectBudget
    var valid: @Sendable () -> Bool
    var stun: [String]
    var factory: any RtcFactory
    var candidate: @Sendable (RtcIceCandidate?) -> Void
    var state: @Sendable (RtcPeerState) -> Void
    var frame: @Sendable (PeerWire.Bytes) -> Void
    var closed: @Sendable () -> Void
    var probe: (@Sendable (Double) -> Void)?
    var clock = DirectClock()
}

/// One framed PeerConnection use (docs/transport.md §2): a reliable ordered business channel and a
/// small unreliable probe channel, both negotiated, no others. SDK objects, SDP and candidates
/// stay below the session. Fragments are reassembled and hashed here, off every actor of the app.
final class DirectChannel: @unchecked Sendable {
    private struct Partial {
        var count: Int, length: Int, hash: PeerWire.Bytes
        var parts: [Int: PeerWire.Bytes] = [:]
        var bytes = 0
        var until: Double
    }
    private let options: DirectChannelOptions
    private let owner: String
    private let lock = NSLock()
    private let wake = AsyncSignal()
    private let writes = SerialChain()
    private var confirmed = false, stunReady = false, ended = false, reservationReleased = false
    private var writers = 0, missedProbes = 0
    private var lastProbe = -Double.infinity
    private var partial: [String: Partial] = [:]
    private var waiting: [PeerWire.Bytes] = []
    private var probes: [String: Double] = [:]
    private var pc: (any RtcPeerConnection)!
    private var data: (any RtcDataChannel)!
    private var probe: (any RtcDataChannel)!
    private var incoming: AsyncStream<Data>.Continuation!
    private var tick: Task<Void, Never>?
    private var consumer: Task<Void, Never>?

    init(_ options: DirectChannelOptions) throws {
        self.options = options
        owner = PeerWire.hex(options.connection)
        guard options.valid(), options.stun.count <= 2,
              options.stun.allSatisfy({ $0.hasPrefix("stun:") && (!$0.contains("?") || $0.hasSuffix("?transport=udp")) }),
              options.budget.acquire(options.peer, owner, options.use) else { throw PathChanged() }
        let (stream, continuation) = AsyncStream<Data>.makeStream(bufferingPolicy: .unbounded)
        incoming = continuation
        var created: (any RtcPeerConnection)?
        do {
            created = try options.factory.create(stun: options.stun, listener: PeerEvents(owner: self))
            pc = created
            data = try created!.createDataChannel(id: 0, ordered: true, maxRetransmits: nil, listener: DataEvents(owner: self))
            probe = try created!.createDataChannel(id: 1, ordered: false, maxRetransmits: 0, listener: ProbeEvents(owner: self))
        } catch {
            created?.close(); continuation.finish()
            options.budget.release(options.peer, options.use)
            throw PathChanged()
        }
        // Reassembly and the final hash run on a lane of their own: a 1 MiB block must never
        // queue ahead of an approval on the Remote actor or the session's chain.
        consumer = Task.detached(priority: .utility) { [weak self] in
            for await raw in stream {
                guard let self, self.valid() else { break }
                do { try self.receive(Array(raw)) } catch { self.close(); break }
            }
        }
        tick = Task.detached { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 100_000_000)
                guard let self, !self.isEnded else { return }
                self.tickOnce()
            }
        }
    }

    private var isEnded: Bool { lock.lock(); defer { lock.unlock() }; return ended }
    private func valid() -> Bool {
        lock.lock(); let state = (ended, confirmed); lock.unlock()
        return !state.0 && options.valid() && (state.1 || options.clock.now() < options.deadline)
    }
    var ready: Bool { lock.lock(); let c = confirmed; lock.unlock(); return c && valid() && data.state == .open }
    /// Ready as far as the channel's own flags say: no question to the owner, so the engine may ask under its lock.
    var usable: Bool { lock.lock(); let c = confirmed && !ended; lock.unlock(); return c && data.state == .open }
    var traversalReady: Bool { lock.lock(); defer { lock.unlock() }; return stunReady }
    var queuedBytes: Int { data.bufferedAmount }
    var isClosed: Bool { isEnded }

    // MARK: SDK events (any thread)
    fileprivate func peerCandidate(_ candidate: RtcIceCandidate) {
        if candidate.isSrflx { lock.lock(); stunReady = true; lock.unlock() }
        if valid() { options.candidate(candidate) }
    }
    fileprivate func peerGathered() { if valid() { options.candidate(nil) } }
    fileprivate func peerState(_ state: RtcPeerState) {
        if isEnded { return }
        options.state(state)
        if state == .failed || state == .closed || state == .disconnected { close() }
    }
    fileprivate func dataChanged() {
        if data.state == .open { wake.signal() } else if data.state == .closed { close() }
    }
    fileprivate func dataDrained() { if data.bufferedAmount <= 32 << 10 { wake.signal() } }
    fileprivate func dataMessage(_ raw: Data) {
        guard valid() else { return }
        if raw.count > frameBytes { close(); return }
        incoming.yield(raw)
    }
    fileprivate func probeChanged() { if probe.state == .closed { close() } }
    fileprivate func probeMessage(_ raw: Data) {
        guard ready, raw.count == 17 else { return }
        let bytes = Array(raw)
        if bytes[0] == 0 { var reply = bytes; reply[0] = 1; _ = probe.send(Data(reply)) }
        else if bytes[0] == 1 {
            let id = PeerWire.hex(Array(bytes[1...]))
            lock.lock(); let start = probes.removeValue(forKey: id); if start != nil { missedProbes = 0 }; lock.unlock()
            if let start { options.probe?(options.clock.monotonic() - start) }
        }
    }

    private func tickOnce() {
        guard valid() else { close(); return }
        let now = options.clock.monotonic()
        var expired: [Partial] = []
        var tooMany = false
        lock.lock()
        for (key, part) in partial where now >= part.until { expired.append(part); partial.removeValue(forKey: key) }
        for (id, start) in probes where now - start >= 1000 { probes.removeValue(forKey: id); missedProbes += 1; if missedProbes >= 3 { tooMany = true } }
        lock.unlock()
        for part in expired { options.budget.free(options.peer, 1, part.bytes, part.parts.count) }
        if tooMany { close(); return }
        if ready && probe.state == .open && now - lastProbe >= 1000 {
            lastProbe = now
            var raw = PeerWire.random(17); raw[0] = 0
            lock.lock(); probes[PeerWire.hex(Array(raw[1...]))] = now; lock.unlock()
            _ = probe.send(Data(raw))
        }
    }

    // MARK: negotiation
    func description(offer: Bool, restart: Bool = false) async throws -> PeerWire.Bytes {
        guard valid() else { throw PathChanged() }
        let sdp = offer ? try await pc.createOffer(iceRestart: restart) : try await pc.createAnswer()
        guard valid(), !sdp.isEmpty else { throw PathChanged() }
        let raw = PeerWire.utf8(sdp)
        try PeerSdp.validate(Data(raw))
        // Freeze the SDK original before collection. Never trim the local description.
        try await pc.setLocalDescription(offer: offer, sdp: sdp)
        guard valid() else { throw PathChanged() }
        return raw
    }
    func remote(_ raw: PeerWire.Bytes, offer: Bool) async throws {
        guard valid() else { throw PathChanged() }
        try PeerSdp.validate(Data(raw))
        guard let text = String(bytes: raw, encoding: .utf8) else { throw PathChanged() }
        try await pc.setRemoteDescription(offer: offer, sdp: text)
        guard valid() else { throw PathChanged() }
    }
    func addCandidate(_ candidate: RtcIceCandidate?) async throws {
        guard valid() else { throw PathChanged() }
        if let candidate { try await pc.addIceCandidate(candidate.candidate, sdpMid: candidate.sdpMid, sdpMLineIndex: candidate.sdpMLineIndex, usernameFragment: "") }
        guard valid() else { throw PathChanged() }
    }
    func confirm() {
        lock.lock()
        guard !ended, options.valid(), confirmed || options.clock.now() < options.deadline else { lock.unlock(); return }
        confirmed = true; let queued = waiting; waiting = []
        lock.unlock()
        for raw in queued { deliver(raw) }
    }

    // MARK: writing
    /// Seals nothing and decides nothing: `raw` is an already authorized envelope. `handoff` runs
    /// once, right before the first fragment goes out, and may refuse.
    func write(_ raw: PeerWire.Bytes, handoff: @escaping @Sendable () throws -> Void) async throws {
        let count = (raw.count + payloadBytes - 1) / payloadBytes
        guard ready, !raw.isEmpty, raw.count <= 1 << 20, options.budget.reserve(options.peer, owner, options.use, 0, raw.count, count) else { throw PathChanged() }
        lock.withLock { writers += 1 }
        let job = writes.run { [self] () -> Error? in
            defer { endWriter(raw.count, count) }
            do { try await writeNow(raw, handoff); return nil } catch { return error }
        }
        // A cancelled caller stops waiting; the job still ends and frees its reservation.
        let failure = await withTaskCancellationHandler { await job.value } onCancel: { [wake] in job.cancel(); wake.signal() }
        if let failure { throw failure }
        try Task.checkCancellation()
    }
    private func endWriter(_ bytes: Int, _ frames: Int) {
        options.budget.free(options.peer, 0, bytes, frames)
        lock.withLock { writers -= 1 }
        releaseReservation()
    }
    private func writeNow(_ raw: PeerWire.Bytes, _ handoff: () throws -> Void) async throws {
        let hash = PeerWire.hash(raw), id = PeerWire.random(16), count = (raw.count + payloadBytes - 1) / payloadBytes
        for index in 0..<count {
            while valid() && (!ready || data.bufferedAmount > 64 << 10) {
                try Task.checkCancellation()
                if isEnded { throw PathChanged() }
                await wake.wait(ms: 50)
            }
            try Task.checkCancellation()
            guard ready else { throw PathChanged() }
            var fragment = PBFragment()
            fragment.messageID = Data(id); fragment.index = UInt32(index); fragment.count = UInt32(count)
            fragment.ciphertextLength = UInt32(raw.count); fragment.ciphertextSha256 = Data(hash)
            fragment.pathGeneration = options.generation
            fragment.fragment = Data(raw[(index * payloadBytes)..<min(raw.count, (index + 1) * payloadBytes)])
            let frame = try fragment.serializedData()
            let maximum = pc.maxMessageSize ?? frameBytes
            guard frame.count <= frameBytes, maximum <= 0 || frame.count <= maximum else { throw PathChanged() }
            if index == 0 { try handoff() }
            if !data.send(frame) { close(); throw PathChanged() }
        }
    }

    // MARK: receiving
    private func receive(_ raw: PeerWire.Bytes) throws {
        guard valid() else { return }
        _ = try PeerSchema.normalize(raw, "Fragment")
        let frame = try PBFragment(serializedBytes: raw)
        guard frame.messageID.count == 16, frame.ciphertextSha256.count == 32, frame.pathGeneration == options.generation,
              frame.count > 0, frame.count <= 128, frame.index < frame.count, frame.ciphertextLength > 0, frame.ciphertextLength <= 1 << 20, !frame.fragment.isEmpty else { return }
        let key = PeerWire.hex(Array(frame.messageID)), piece = Array(frame.fragment), hash = Array(frame.ciphertextSha256)
        var complete: PeerWire.Bytes?
        lock.lock()
        if ended { lock.unlock(); return }
        var part = partial[key] ?? Partial(count: Int(frame.count), length: Int(frame.ciphertextLength), hash: hash, until: options.clock.monotonic() + 5000)
        if part.count != Int(frame.count) || part.length != Int(frame.ciphertextLength) || part.hash != hash { lock.unlock(); return }
        if let old = part.parts[Int(frame.index)] {
            lock.unlock()
            if old != piece { close() }
            return
        }
        if part.bytes + piece.count > part.length || !options.budget.reserve(options.peer, owner, options.use, 1, piece.count, 1) { lock.unlock(); return }
        part.parts[Int(frame.index)] = piece; part.bytes += piece.count; partial[key] = part
        if part.parts.count == part.count {
            partial.removeValue(forKey: key)
            if part.bytes != part.length || !options.budget.reserve(options.peer, owner, options.use, 1, part.bytes, 1) {
                options.budget.free(options.peer, 1, part.bytes, part.parts.count)
            } else {
                var joined = PeerWire.Bytes(); joined.reserveCapacity(part.length)
                for index in 0..<part.count { joined += part.parts[index]! }
                // The pieces' reservation becomes the whole message's; only that stays charged.
                options.budget.free(options.peer, 1, part.bytes, part.parts.count)
                complete = joined
            }
        }
        lock.unlock()
        guard let joined = complete else { return }
        // The hash is the one costly step: outside the lock, with the buffer already reserved.
        guard PeerWire.hash(joined) == hash, valid() else { options.budget.free(options.peer, 1, joined.count, 1); return }
        lock.lock()
        if ended { lock.unlock(); options.budget.free(options.peer, 1, joined.count, 1); return }
        let now = confirmed
        if !now { waiting.append(joined) }
        lock.unlock()
        if now { deliver(joined) }
    }
    private func deliver(_ raw: PeerWire.Bytes) {
        options.budget.free(options.peer, 1, raw.count, 1)
        if valid() && lock.withLock({ confirmed }) { options.frame(raw) }
    }

    func close() {
        var parts: [Partial] = [], held: [PeerWire.Bytes] = []
        lock.lock()
        if ended { lock.unlock(); return }
        ended = true; parts = Array(partial.values); partial = [:]; held = waiting; waiting = []; probes = [:]
        lock.unlock()
        for part in parts { options.budget.free(options.peer, 1, part.bytes, part.parts.count) }
        for raw in held { options.budget.free(options.peer, 1, raw.count, 1) }
        wake.signal(); incoming.finish(); tick?.cancel()
        // SDK shutdown may block for a while: never under a lock, and the observer runs after it.
        cleanup({ [pc] in pc?.close() }, { [options] in options.closed() }, { [self] in releaseReservation() })
    }
    private func releaseReservation() {
        lock.lock()
        let release = ended && writers == 0 && !reservationReleased
        if release { reservationReleased = true }
        lock.unlock()
        if release { options.budget.release(options.peer, options.use) }
    }

    // Listener shims hold the channel weakly: the SDK may outlive it.
    private struct PeerEvents: RtcPeerListener {
        weak var owner: DirectChannel?
        func onIceCandidate(_ c: RtcIceCandidate) { owner?.peerCandidate(c) }
        func onIceGatheringComplete() { owner?.peerGathered() }
        func onConnectionStateChange(_ s: RtcPeerState) { owner?.peerState(s) }
        func onUnexpectedDataChannel() { owner?.close() }
    }
    private struct DataEvents: RtcDataChannelListener {
        weak var owner: DirectChannel?
        func onStateChange() { owner?.dataChanged() }
        func onBufferedAmountChange() { owner?.dataDrained() }
        func onMessage(_ d: Data) { owner?.dataMessage(d) }
    }
    private struct ProbeEvents: RtcDataChannelListener {
        weak var owner: DirectChannel?
        func onStateChange() { owner?.probeChanged() }
        func onBufferedAmountChange() {}
        func onMessage(_ d: Data) { owner?.probeMessage(d) }
    }
}
