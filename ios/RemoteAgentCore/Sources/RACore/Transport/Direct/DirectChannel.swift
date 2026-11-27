import CryptoKit
import Foundation
import RAProto

private let frameBytes = 16 << 10
private let payloadBytes = frameBytes - 128

private func sha256Data(_ data: Data) -> Data {
    Data(SHA256.hash(data: data))
}

private func hexString(_ data: Data) -> String {
    data.map { String(format: "%02x", $0) }.joined()
}

/// Minimal platform sanity check before calling WebRTC SDP APIs; semantic validation lives in shared/peerwire.
func validateMinimalSDP(_ raw: Data) throws -> String {
    guard !raw.isEmpty, raw.count <= 60 * 1024 else { throw PathChanged() }
    if raw.count >= 3, raw[0] == 0xEF, raw[1] == 0xBB, raw[2] == 0xBF {
        throw PathChanged()
    }
    guard let text = String(data: raw, encoding: .utf8),
          text.hasSuffix("\r\n"),
          !text.unicodeScalars.contains(where: {
              $0.value == 0x00 || $0.value == 0x0B || $0.value == 0x0C ||
              $0.value == 0x85 || $0.value == 0x2028 || $0.value == 0x2029
          }) else {
        throw PathChanged()
    }
    var prevCR = false
    for scalar in text.unicodeScalars {
        if scalar.value == 0x0A {
            if !prevCR { throw PathChanged() }
            prevCR = false
        } else if prevCR {
            throw PathChanged()
        } else if scalar.value == 0x0D {
            prevCR = true
        }
    }
    if prevCR { throw PathChanged() }
    guard text.contains("m=application "), text.contains("a=fingerprint:sha-256 ") else {
        throw PathChanged()
    }
    return text
}

/// Wakes writers waiting for the DataChannel buffer to drain without losing signals.
final class AsyncSignal: @unchecked Sendable {
    private let lock = NSLock()
    private var waiters: [UUID: CheckedContinuation<Void, Never>] = [:]
    private var pending = false

    func signal() {
        lock.lock()
        let all = Array(waiters.values)
        waiters.removeAll()
        pending = all.isEmpty
        lock.unlock()
        for waiter in all { waiter.resume() }
    }

    func wait(ms: UInt64) async {
        let id = UUID()
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            lock.lock()
            if pending { pending = false; lock.unlock(); c.resume(); return }
            waiters[id] = c
            lock.unlock()
            Task { [weak self] in
                try? await Task.sleep(nanoseconds: ms * 1_000_000)
                self?.expire(id)
            }
        }
    }

    private func expire(_ id: UUID) {
        lock.lock(); let c = waiters.removeValue(forKey: id); lock.unlock(); c?.resume()
    }
}

/// Shared by both PC uses and replacement connections; hints never reset it.
final class DirectBudget: @unchecked Sendable {
    private struct Usage { var owner: String; var uses = Set<Int>(); var bytes = [0, 0]; var frames = [0, 0] }
    private let lock = NSLock()
    private var peers: [String: Usage] = [:]
    private var total = [0, 0]
    let maxPeers: Int

    init(maxPeers: Int = 4) { self.maxPeers = maxPeers }

    func acquire(_ peer: String, _ owner: String, _ use: Int) -> Bool {
        lock.lock(); defer { lock.unlock() }
        var current = peers[peer]
        if current == nil {
            if peers.count >= maxPeers { return false }
            current = Usage(owner: owner)
        }
        guard current!.owner == owner, !current!.uses.contains(use), current!.uses.count < 2 else { return false }
        current!.uses.insert(use); peers[peer] = current
        return true
    }

    func ownerAvailable(_ peer: String, _ owner: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return peers[peer] == nil || peers[peer]!.owner == owner
    }

    func release(_ peer: String, _ use: Int) {
        lock.lock(); defer { lock.unlock() }
        if var current = peers[peer] {
            current.uses.remove(use); peers[peer] = current; prune(peer)
        }
    }

    func reserve(_ peer: String, _ owner: String, _ use: Int, _ direction: Int, _ bytes: Int, _ frames: Int) -> Bool {
        lock.lock(); defer { lock.unlock() }
        guard var current = peers[peer], current.owner == owner, current.uses.contains(use) else { return false }
        guard bytes >= 0, frames >= 0,
              current.bytes[direction] + bytes <= 2 << 20,
              current.frames[direction] + frames <= 128,
              total[direction] + bytes <= 16 << 20 else { return false }
        current.bytes[direction] += bytes; current.frames[direction] += frames; total[direction] += bytes
        peers[peer] = current
        return true
    }

    func free(_ peer: String, _ direction: Int, _ bytes: Int, _ frames: Int) {
        lock.lock(); defer { lock.unlock() }
        if var current = peers[peer] {
            current.bytes[direction] -= bytes; current.frames[direction] -= frames; total[direction] -= bytes
            peers[peer] = current; prune(peer)
        }
    }

    private func prune(_ peer: String) {
        guard let current = peers[peer] else { return }
        if current.uses.isEmpty && current.bytes == [0, 0] && current.frames == [0, 0] {
            peers.removeValue(forKey: peer)
        }
    }

    var isEmpty: Bool { lock.lock(); defer { lock.unlock() }; return peers.isEmpty && total == [0, 0] }
}

private func cleanup(_ steps: (() -> Void)...) {
    for step in steps { step() }
}

private func decodeStrictFragment(_ raw: Data) throws -> PBFragment {
    let frame = try PBFragment(serializedBytes: raw)
    guard frame.unknownFields.data.isEmpty else { throw PathChanged() }
    return frame
}

struct DirectChannelOptions: Sendable {
    var peer: String
    var roundId: UInt64
    var use: Int
    var generation: UInt64
    var budget: DirectBudget
    var valid: @Sendable () -> Bool
    var stun: [String]
    var factory: any RtcFactory
    var candidate: @Sendable (RtcIceCandidate?) -> Void
    var state: @Sendable (PBCoreDirectStateKind, String) -> Void
    var dcState: (@Sendable (Bool) -> Void)? = nil
    var stunError: (@Sendable (Int, String) -> Void)? = nil
    var frame: @Sendable (Data) -> Void
    var closed: @Sendable () -> Void
    var open: (@Sendable () -> Void)? = nil
    var probeReply: (@Sendable (Data) -> Void)? = nil
    var clock: @Sendable () -> UInt64 = { UInt64(Date().timeIntervalSince1970 * 1000) }
    var monotonic: @Sendable () -> Double = { Double(DispatchTime.now().uptimeNanoseconds) / 1e6 }
    var autoTick: Bool = true
    var closeDispatch: @Sendable (@escaping @Sendable () -> Void) -> Void = { block in
        DispatchQueue.global(qos: .default).async(execute: block)
    }
}

/// Framed WebRTC DataChannel pair (id 0 reliable data, id 1 unreliable probe); signaling, state machine,
/// and routing policy live in Go Core.
final class DirectChannel: @unchecked Sendable {
    private struct Partial {
        var count: Int, length: Int, hash: Data
        var parts: [Int: Data] = [:]
        var bytes = 0
        var until: Double
    }

    private let options: DirectChannelOptions
    private let owner: String
    private let lock = NSLock()
    private let wake = AsyncSignal()
    private let writes = SerialChain()
    private var confirmed = false, ended = false, reservationReleased = false
    private var writers = 0
    private var partial: [String: Partial] = [:]
    private var waiting: [Data] = []
    private var pc: (any RtcPeerConnection)!
    private var data: (any RtcDataChannel)!
    private var probe: (any RtcDataChannel)!
    private var incoming: AsyncStream<Data>.Continuation!
    private var tick: Task<Void, Never>?

    init(_ options: DirectChannelOptions) throws {
        self.options = options
        self.owner = String(options.roundId)
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
        // Reassembly and SHA-256 verification stay off caller actors without utility-QoS starvation.
        Task.detached { [weak self] in
            for await raw in stream {
                guard let self, self.valid() else { break }
                do { try self.receive(raw) } catch { self.close(); break }
            }
        }
        if options.autoTick {
            tick = Task.detached { [weak self] in
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: 100_000_000)
                    guard let self, !self.isEnded else { return }
                    self.tickOnce()
                }
            }
        }
    }

    private var isEnded: Bool { lock.withLock { ended } }
    private func valid() -> Bool { !isEnded && options.valid() }
    var ready: Bool { lock.withLock { confirmed } && valid() && data.state == .open }
    var usable: Bool { lock.withLock { confirmed && !ended } && data.state == .open }
    var isClosed: Bool { isEnded }

    // MARK: SDK events
    fileprivate func peerCandidate(_ candidate: RtcIceCandidate) {
        if valid() { options.candidate(candidate) }
    }
    fileprivate func peerGathered() {
        if valid() { options.candidate(nil) }
    }
    fileprivate func peerState(_ state: RtcPeerState) {
        guard valid() else { return }
        let stateToken = switch state {
        case .new: "new"
        case .connecting: "connecting"
        case .connected: "connected"
        case .disconnected: "disconnected"
        case .failed: "failed"
        case .closed: "closed"
        }
        options.state(.conn, stateToken)
        if state == .failed || state == .closed || state == .disconnected { close() }
    }
    fileprivate func iceConnState(_ state: String) {
        if valid() && !state.isEmpty { options.state(.ice, state) }
    }
    fileprivate func iceGatherState(_ state: String) {
        if valid() && !state.isEmpty { options.state(.gather, state) }
    }
    fileprivate func iceCandidateError(code: Int, address: String) {
        if valid() { options.stunError?(code, address) }
    }
    fileprivate func dataChanged() {
        guard valid() else { return }
        if data.state == .open {
            wake.signal()
            options.dcState?(true)
            options.open?()
        } else if data.state == .closed {
            options.dcState?(false)
            close()
        }
    }
    fileprivate func dataDrained() { if data.bufferedAmount <= 32 << 10 { wake.signal() } }
    fileprivate func dataMessage(_ raw: Data) {
        guard valid() else { return }
        if raw.count > frameBytes { close(); return }
        incoming.yield(raw)
    }
    fileprivate func probeChanged() { if valid() && probe.state == .closed { close() } }
    fileprivate func probeMessage(_ raw: Data) {
        guard ready, raw.count == 17 else { return }
        let bytes = [UInt8](raw)
        if bytes[0] == 0 {
            var reply = bytes; reply[0] = 1
            _ = probe.send(Data(reply))
        } else if bytes[0] == 1 {
            options.probeReply?(raw)
        }
    }

    @discardableResult
    func sendProbe(_ raw: Data) -> Bool {
        guard ready, probe.state == .open, raw.count == 17 else { return false }
        if !probe.send(raw) {
            close()
            return false
        }
        return true
    }

    func tickOnce() {
        guard valid() else { close(); return }
        if lock.withLock({ confirmed }) && (data.state == .closed || probe.state == .closed) {
            close()
            return
        }
        let now = options.monotonic()
        var expired: [Partial] = []
        lock.lock()
        for (key, part) in partial where now >= part.until { expired.append(part); partial.removeValue(forKey: key) }
        lock.unlock()
        for part in expired { options.budget.free(options.peer, 1, part.bytes, part.parts.count) }
    }

    // MARK: negotiation
    func description(offer: Bool, restart: Bool = false) async throws -> Data {
        guard valid() else { throw PathChanged() }
        let sdp = offer ? try await pc.createOffer(iceRestart: restart) : try await pc.createAnswer()
        guard valid(), !sdp.isEmpty else { throw PathChanged() }
        let raw = Data(sdp.utf8)
        _ = try validateMinimalSDP(raw)
        try await pc.setLocalDescription(offer: offer, sdp: sdp)
        guard valid() else { throw PathChanged() }
        return raw
    }

    func remote(_ raw: Data, offer: Bool) async throws {
        guard valid() else { throw PathChanged() }
        let text = try validateMinimalSDP(raw)
        try await pc.setRemoteDescription(offer: offer, sdp: text)
        guard valid() else { throw PathChanged() }
    }

    func addCandidate(_ candidate: RtcIceCandidate?) async throws {
        guard valid() else { throw PathChanged() }
        if let candidate {
            try await pc.addIceCandidate(candidate.candidate, sdpMid: candidate.sdpMid, sdpMLineIndex: candidate.sdpMLineIndex, usernameFragment: "")
        }
        guard valid() else { throw PathChanged() }
    }

    func addCandidateLine(_ line: String, mid: String, mlineIndex: Int, ufrag: String) async throws {
        guard valid() else { throw PathChanged() }
        let normalizedMid = mid.isEmpty ? "0" : mid
        try await pc.addIceCandidate(line, sdpMid: normalizedMid, sdpMLineIndex: mlineIndex, usernameFragment: ufrag)
        guard valid() else { throw PathChanged() }
    }

    func confirm(_ beforeDeliver: () -> Void = {}) {
        lock.lock()
        guard !ended, options.valid() else { lock.unlock(); return }
        confirmed = true
        let queued = waiting
        waiting = []
        lock.unlock()
        beforeDeliver()
        for raw in queued { deliver(raw) }
    }

    func readStats() async -> PBCoreDirectStatsReport {
        guard valid() else { return PBCoreDirectStatsReport() }
        return await pc.readStats()
    }

    // MARK: writing
    func write(_ raw: Data, handoff: @escaping @Sendable () throws -> Void) async throws {
        let count = (raw.count + payloadBytes - 1) / payloadBytes
        guard ready, !raw.isEmpty, raw.count <= 1 << 20,
              options.budget.reserve(options.peer, owner, options.use, 0, raw.count, count) else { throw PathChanged() }
        lock.withLock { writers += 1 }
        let job = writes.run { [self] () -> Error? in
            defer { endWriter(raw.count, count) }
            do { try await writeNow(raw, handoff); return nil } catch { return error }
        }
        let failure = await withTaskCancellationHandler { await job.value } onCancel: { [wake] in job.cancel(); wake.signal() }
        if let failure { throw failure }
        try Task.checkCancellation()
    }

    private func endWriter(_ bytes: Int, _ frames: Int) {
        options.budget.free(options.peer, 0, bytes, frames)
        lock.withLock { writers -= 1 }
        releaseReservation()
    }

    private func writeNow(_ raw: Data, _ handoff: () throws -> Void) async throws {
        let hash = sha256Data(raw)
        let id = randomBytes(16)
        let count = (raw.count + payloadBytes - 1) / payloadBytes
        for index in 0..<count {
            while valid() && (!ready || data.bufferedAmount > 64 << 10) {
                try Task.checkCancellation()
                if isEnded { throw PathChanged() }
                await wake.wait(ms: 50)
            }
            try Task.checkCancellation()
            guard ready else { throw PathChanged() }
            var fragment = PBFragment()
            fragment.messageID = id
            fragment.index = UInt32(index)
            fragment.count = UInt32(count)
            fragment.ciphertextLength = UInt32(raw.count)
            fragment.ciphertextSha256 = hash
            fragment.pathGeneration = options.generation
            fragment.fragment = raw.subdata(in: (index * payloadBytes)..<min(raw.count, (index + 1) * payloadBytes))
            let frame = try fragment.serializedData()
            let maximum = pc.maxMessageSize ?? frameBytes
            guard frame.count <= frameBytes, maximum <= 0 || frame.count <= maximum else { throw PathChanged() }
            if index == 0 { try handoff() }
            if !data.send(frame) { close(); throw PathChanged() }
        }
    }

    // MARK: receiving
    private func receive(_ raw: Data) throws {
        guard valid() else { return }
        let frame = try decodeStrictFragment(raw)
        guard frame.messageID.count == 16,
              frame.ciphertextSha256.count == 32,
              frame.pathGeneration == options.generation,
              frame.count > 0, frame.count <= 128,
              frame.index < frame.count,
              frame.ciphertextLength > 0, frame.ciphertextLength <= 1 << 20,
              !frame.fragment.isEmpty else { return }
        let key = hexString(frame.messageID)
        let piece = frame.fragment
        let hash = frame.ciphertextSha256
        var complete: Data?
        lock.lock()
        if ended { lock.unlock(); return }
        var part = partial[key] ?? Partial(count: Int(frame.count), length: Int(frame.ciphertextLength), hash: hash, until: options.monotonic() + 5000)
        if part.count != Int(frame.count) || part.length != Int(frame.ciphertextLength) || part.hash != hash { lock.unlock(); return }
        if let old = part.parts[Int(frame.index)] {
            lock.unlock()
            if old != piece { close() }
            return
        }
        if part.bytes + piece.count > part.length || !options.budget.reserve(options.peer, owner, options.use, 1, piece.count, 1) {
            lock.unlock()
            return
        }
        part.parts[Int(frame.index)] = piece; part.bytes += piece.count; partial[key] = part
        if part.parts.count == part.count {
            partial.removeValue(forKey: key)
            if part.bytes != part.length || !options.budget.reserve(options.peer, owner, options.use, 1, part.bytes, 1) {
                options.budget.free(options.peer, 1, part.bytes, part.parts.count)
            } else {
                var joined = Data(); joined.reserveCapacity(part.length)
                for index in 0..<part.count { joined.append(part.parts[index]!) }
                options.budget.free(options.peer, 1, part.bytes, part.parts.count)
                complete = joined
            }
        }
        lock.unlock()
        guard let joined = complete else { return }
        guard sha256Data(joined) == hash, valid() else {
            options.budget.free(options.peer, 1, joined.count, 1)
            return
        }
        lock.lock()
        if ended {
            lock.unlock()
            options.budget.free(options.peer, 1, joined.count, 1)
            return
        }
        let now = confirmed
        if !now { waiting.append(joined) }
        lock.unlock()
        if now { deliver(joined) }
    }

    private func deliver(_ raw: Data) {
        options.budget.free(options.peer, 1, raw.count, 1)
        if valid() && lock.withLock({ confirmed }) { options.frame(raw) }
    }

    func close() {
        var parts: [Partial] = [], held: [Data] = []
        lock.lock()
        if ended { lock.unlock(); return }
        ended = true
        parts = Array(partial.values); partial = [:]
        held = waiting; waiting = []
        lock.unlock()
        for part in parts { options.budget.free(options.peer, 1, part.bytes, part.parts.count) }
        for raw in held { options.budget.free(options.peer, 1, raw.count, 1) }
        wake.signal(); incoming.finish(); tick?.cancel()
        cleanup({ [options] in options.closed() }, { [self] in releaseReservation() })
        let peerConn = pc
        // PeerConnection.close() can block inside libwebrtc locks; never stall callers or Swift's cooperative pool.
        options.closeDispatch { peerConn?.close() }
    }

    private func releaseReservation() {
        lock.lock()
        let release = ended && writers == 0 && !reservationReleased
        if release { reservationReleased = true }
        lock.unlock()
        if release { options.budget.release(options.peer, options.use) }
    }

    private struct PeerEvents: RtcPeerListener {
        weak var owner: DirectChannel?
        func onIceCandidate(_ c: RtcIceCandidate) { owner?.peerCandidate(c) }
        func onIceGatheringComplete() { owner?.peerGathered() }
        func onConnectionStateChange(_ s: RtcPeerState) { owner?.peerState(s) }
        func onUnexpectedDataChannel() { owner?.close() }
        func onIceConnectionStateChange(_ state: String) { owner?.iceConnState(state) }
        func onIceGatheringStateChange(_ state: String) { owner?.iceGatherState(state) }
        func onIceCandidateError(code: Int, address: String) { owner?.iceCandidateError(code: code, address: address) }
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
