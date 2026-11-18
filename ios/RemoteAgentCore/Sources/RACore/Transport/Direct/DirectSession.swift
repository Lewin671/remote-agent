import Foundation

struct DirectedOutput: Sendable {
    var raw: PeerWire.Bytes
    var deadline: UInt64
    var critical: Bool
    var valid: @Sendable () -> Bool
}

struct DirectSessionOptions: Sendable {
    var selfID: PeerWire.Identity
    var peer: PeerWire.Public
    var codec: PeerCodec
    var connection: PeerWire.Bytes
    var deadline: UInt64
    var budget: DirectBudget
    var stun: [String]
    var factory: any RtcFactory
    var valid: @Sendable () -> Bool
    var authorize: @Sendable () async throws -> Void
    var send: @Sendable (DirectedOutput) -> Bool
    var chosen: @Sendable () -> Void
    var beforeOffer: @Sendable () -> Void
    var ready: @Sendable (Int, DirectChannel) -> Void
    var closed: @Sendable (Int?) -> Void
    var frame: @Sendable (PeerWire.Bytes, Int) -> Void
    var capability: @Sendable (PeerWire.Opened) -> Void
    var open: (@Sendable (Int) -> Void)? = nil
    var probe: (@Sendable (Int, Double) -> Void)?
    var clock = DirectClock()
}

/// Why a round ended without a path, as far as this side can tell. Display only: set where the
/// failure is already detected, never read by control flow.
enum DirectFailure: Sendable { case peerUnsupported, noResponse, noTraversal }

/// Serial owner for one connection and both uses (web/src/transport/direct-session.ts). Every
/// mutation of the per-use state runs inside the chain, one job at a time, so a late callback
/// cannot interleave with another step; the chain's jobs keep the original authenticated bytes.
final class DirectSession: @unchecked Sendable {
    typealias W = PeerWire
    private final class PC {
        var generation: UInt64 = 1, seq: UInt64 = 0
        var restartSent = false, ended = false, confirmed = false, announced = false
        let gate: PeerSignal
        var sender: PeerSender
        var channel: DirectChannel?
        var batch: PeerCandidateBatcher
        var ufrag = W.Bytes(), mid = W.Bytes()
        var actions: [PeerSignal.Action] = []
        init(gate: PeerSignal, sender: PeerSender, batch: PeerCandidateBatcher) { self.gate = gate; self.sender = sender; self.batch = batch }
    }
    private struct Window { var next: UInt64 = 1; var pending: [UInt64: (opened: W.Opened, at: UInt64)] = [:]; var gap: UInt64 = 0 }

    let options: DirectSessionOptions
    private let chain = SerialChain()
    private let lock = NSLock()
    private var pcs: [PC] = []
    private let boot: PeerSender
    private var timer: Task<Void, Never>?
    private var tickQueued = false, ended = false, retired = false
    private var _tickCount = 0
    private var incoming = 0, incomingBytes = 0
    private var controlSeq: UInt64 = 0
    private var challenge: W.Bytes?
    private var receipts: [String: (raw: W.Bytes, until: UInt64)] = [:]
    private var controls: [String: (hash: W.Bytes, until: UInt64)] = [:]
    private var peerWindows: [UInt64: Window] = [:]
    private(set) var retainUntil: UInt64
    private var _established = false
    private var _failure: DirectFailure?

    var established: Bool { lock.withLock { _established } }
    var failure: DirectFailure? { lock.withLock { _failure } }
    var tickCount: Int { lock.withLock { _tickCount } }
    /// The computer as learned so far: its instance and boot nonce are filled in by its Challenge.
    var peer: W.Public { pcs[0].gate.peerNow }
    /// Not ended and not retired: flags only, so the engine can ask while holding its own lock.
    var live: Bool { lock.withLock { !ended && !retired } }
    var active: Bool { lock.withLock { !ended && !retired } && options.valid() }
    private var isEnded: Bool { lock.withLock { ended } }
    private var isRetired: Bool { lock.withLock { retired } }

    init(_ options: DirectSessionOptions) throws {
        self.options = options
        let now = options.clock.now()
        guard options.valid(), options.deadline > now, options.deadline <= now + 10_000 else { throw PathChanged() }
        boot = PeerSender(options.deadline)
        retainUntil = options.deadline + 300_000
        for use in 1...2 {
            let gate = PeerSignal(options.codec, options.selfID, options.peer, options.connection, UInt64(use), now)
            gate.roundDeadline = options.deadline; gate.current = options.valid; gate.leaseUntil = UInt64.max / 2
            pcs.append(PC(gate: gate, sender: PeerSender(options.deadline), batch: PeerCandidateBatcher(1, options.deadline, 1)))
        }
        timer = Task.detached { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 25_000_000)
                guard let self, !self.isEnded else { return }
                self.timerFired()
            }
        }
    }

    private func timerFired() {
        let now = options.clock.now()
        if !options.valid() || (isRetired && now >= retainUntil) { close(); return }
        let queue = lock.withLock { () -> Bool in if tickQueued { return false }; tickQueued = true; return true }
        if !queue { return }
        queued { [self] in await tick(); lock.withLock { tickQueued = false } }
    }
    /// A job in the chain; errors end only that job, like a rejected promise caught by the chain.
    @discardableResult
    private func queued(_ work: @escaping @Sendable () async throws -> Void) -> Task<Void, Never>? {
        chain.run { [weak self] in
            guard let self, !self.isEnded else { return }
            do { try await work() } catch {}
        }
    }

    func start() async {
        await queued {
            try await self.options.authorize(); guard self.active else { return }
            let now = self.options.clock.now(), o = self.options
            var context = W.Context(o.selfID.peer, W.member(o.peer), o.connection, 0, 0, 1, now, 1, true); context.recipientUnknown = 1
            context.deadline = o.deadline
            let body = W.pb([W.b(1, o.connection), W.b(2, o.selfID.instance), W.b(3, o.selfID.boot)])
            let raw = try o.codec.seal(o.selfID, o.peer, context, 1, body)
            let opened = try o.codec.openSent(raw, o.selfID, o.peer, now)
            for p in self.pcs { try p.gate.rememberProbe(opened) }
            _ = self.boot.enqueue(raw, now, o.deadline, 1)
        }?.value
    }

    /// Unauthenticated bytes are only counted here (8 frames / 1 MiB at once); everything else
    /// happens in the chain after the member's keys opened them.
    @discardableResult
    func receive(_ raw: W.Bytes) -> Bool {
        let accepted = lock.withLock { () -> Bool in
            if ended || raw.count > 64 << 10 || incoming >= 8 || incomingBytes + raw.count > 1 << 20 { return false }
            incoming += 1; incomingBytes += raw.count; return true
        }
        if !accepted { return false }
        queued { try await self.receiveNow(raw) }
        queued { self.lock.withLock { self.incoming -= 1; self.incomingBytes -= raw.count } }
        return true
    }

    private func receiveNow(_ raw: W.Bytes) async throws {
        let o = options
        try await o.authorize(); if isEnded || !o.valid() { return }
        let peer = pcs[0].gate.peer
        let opened = try o.codec.open(raw, peer, o.selfID, o.clock.now()), c = opened.ctx, now = o.clock.now()
        if isEnded || !o.valid() || now >= c.deadline { return }
        if c.connection != o.connection { return }
        _ = try PeerSchema.known(c.cls, opened.type, opened.payload)
        let key = PeerSignal.key(c), oldControl = controls[key]
        if isRetired {
            if let old = oldControl, old.hash == opened.hash { await replyCached(opened); return }
            for p in pcs where p.gate.replyAllowed(raw, now) {
                if c.cls == 1 && opened.type == 1, let ch = challenge { await transmit(ch) } else { try await ack(opened) }
                break
            }
            return
        }
        if pcs[0].gate.seen.count + pcs[1].gate.seen.count + controls.count >= 2047 && oldControl == nil && !pcs.contains(where: { $0.gate.seen[key] != nil }) { return }
        retainUntil = max(retainUntil, c.deadline + 300_000)
        if c.cls == 2 && opened.type == 4 { try await receiveAck(opened, raw, peer, now); return }
        if c.cls == 1 && (opened.type == 1 || opened.type == 2) {
            for p in pcs { _ = try p.gate.receive(raw, now) }
            if opened.type == 1 {
                if challenge == nil {
                    let learned = pcs[0].gate.peer
                    var context = W.Context(o.selfID.peer, learned, o.connection, 0, 0, 1, now, 1, true); context.deadline = min(o.deadline, c.deadline)
                    let body = W.pb([W.b(1, o.connection), W.b(2, o.selfID.instance), W.b(3, o.selfID.boot), W.b(4, learned.boot), W.b(6, opened.hash)])
                    let sealed = try o.codec.seal(o.selfID, learned, context, 2, body)
                    challenge = sealed
                    let sent = try o.codec.openSent(sealed, o.selfID, learned, now)
                    for p in pcs { try p.gate.rememberChallenge(sent) }
                    _ = boot.enqueue(sealed, now, context.deadline, 1)
                } else if pcs[0].gate.replyAllowed(raw, now), let ch = challenge { await transmit(ch) }
            } else {
                if let probe = pcs[0].gate.probe { _ = boot.ack(probe.hash) }
                try await ack(opened)
                for i in 0..<2 { try await maybeOffer(i) }
            }
            return
        }
        if c.cls == 2 { try await receiveControl(opened, oldControl, now); return }
        if c.cls == 1 && c.use == 0 { try await receivePeerScope(opened, now); return }
        guard c.use >= 1 && c.use <= 2 else { return }
        let index = Int(c.use) - 1, p = pcs[index]
        let actions = try p.gate.receive(raw, now)
        if p.gate.replyAllowed(raw, now) { try await ack(opened) }
        for action in actions {
            if action.cls == 2 && action.type == 10 {
                let original = try o.codec.open(action.original, p.gate.peer, o.selfID, now)
                try await respond(original, 10, action.payload)
            } else if action.cls == 1 {
                if p.actions.count >= 32 { retireNow(); return }
                p.actions.append(action)
            }
        }
        try await drain(index); try await maybeReady(index)
    }

    private func receiveAck(_ opened: W.Opened, _ raw: W.Bytes, _ peer: W.Public, _ now: UInt64) async throws {
        let o = options, f = try PeerSchema.fields(opened.payload, "Ack"), hash = f[6]!.data
        for sender in [boot] + pcs.map(\.sender) {
            guard let sent = sender.pending[W.hex(hash)] else { continue }
            let original = try o.codec.openSent(sent.raw, o.selfID, peer, now), q = original.ctx
            if q.cls != f[4]!.value || q.seq != f[5]!.value || q.generation != f[7]!.value || q.use != f[8]!.value { return }
            if original.type == 2 && q.cls == 1 && q.generation == 0 { for p in pcs { _ = try p.gate.receive(raw, now) } }
            _ = lock.withLock { sender.ack(hash) }
            for i in 0..<2 { try await maybeOffer(i) }
            return
        }
    }

    private func receiveControl(_ opened: W.Opened, _ old: (hash: W.Bytes, until: UInt64)?, _ now: UInt64) async throws {
        let c = opened.ctx, key = PeerSignal.key(c)
        if let old { if old.hash == opened.hash { await replyCached(opened) }; return }
        if opened.type == 1 || opened.type == 14 {
            let f = try PeerSchema.fields(opened.payload, opened.type == 1 ? "Capabilities" : "CapabilitySnapshot"), expires = f[opened.type == 1 ? 4 : 3]!.value
            if expires <= now || expires <= c.issued || expires - c.issued > 60_000 { return }
            options.capability(opened)
        }
        controls[key] = (opened.hash, c.deadline + 300_000)
        if opened.type == 10 { try await unsupported(opened, now); return }
        if [11, 12, 13].contains(opened.type) { return }
        if opened.type == 1 || opened.type == 14 { try await ack(opened) }
        else { try await respond(opened, 10, W.pb([W.u(1, c.cls), W.u(2, opened.type), W.u(3, c.seq), W.b(4, opened.hash)])) }
    }

    private func validPeerScope(_ o: W.Opened) throws -> Bool {
        if o.type == 9 { return true }
        guard o.type == 8, pcs[0].gate.offerer else { return false }
        let targets = try PeerSchema.restart(o.payload)
        return targets.allSatisfy { use, generation in use >= 1 && use <= 2 && pcs[Int(use) - 1].gate.generation == generation }
    }

    private func receivePeerScope(_ opened: W.Opened, _ now: UInt64) async throws {
        var o = opened
        if now >= options.deadline { return }
        let key = PeerSignal.key(o.ctx)
        if let old = controls[key] { if old.hash == o.hash { await replyCached(o) }; return }
        guard try validPeerScope(o) else { return }
        var window = peerWindows[o.ctx.generation]
        if window == nil { if peerWindows.count >= 32 { return }; window = Window() }
        if o.ctx.seq < window!.next || window!.pending.count >= 32 { return }
        controls[key] = (o.hash, o.ctx.deadline + 300_000)
        if o.ctx.seq > window!.next {
            window!.pending[o.ctx.seq] = (o, now)
            if window!.gap == 0 { window!.gap = min(now + 2500, o.ctx.deadline, options.deadline) }
            peerWindows[o.ctx.generation] = window; return
        }
        let generation = o.ctx.generation
        while true {
            let fresh = try options.codec.open(o.raw, peer, options.selfID, options.clock.now())
            if !active { retireNow(); return }
            guard try validPeerScope(fresh) else { retireNow(); return }
            try await ack(fresh)
            if fresh.type == 9 { retireNow(); return }
            for (use, _) in try PeerSchema.restart(fresh.payload) { try await restartPC(Int(use) - 1) }
            window!.next += 1
            guard let next = window!.pending.removeValue(forKey: window!.next) else { break }
            o = next.opened
        }
        window!.gap = window!.pending.values.map { min($0.at + 2500, $0.opened.ctx.deadline, options.deadline) }.min() ?? 0
        peerWindows[generation] = window
    }

    private func ack(_ o: W.Opened) async throws {
        if o.ctx.cls == 1 && o.type == 1 || o.ctx.cls == 2 && [4, 10, 11, 12, 13].contains(o.type) { return }
        if await replyCached(o) { return }
        if o.ctx.cls == 1 && (o.type < 1 || o.type > 9) { return }
        try await respond(o, 4, W.pb([W.u(4, o.ctx.cls), W.u(5, o.ctx.seq), W.b(6, o.hash), W.u(7, o.ctx.generation), W.u(8, o.ctx.use)]))
    }
    @discardableResult
    private func replyCached(_ o: W.Opened) async -> Bool {
        guard let reply = receipts[W.hex(o.hash)] else { return false }
        if options.clock.now() < o.ctx.deadline { await transmit(reply.raw) }
        return true
    }
    private func respond(_ original: W.Opened, _ type: UInt64, _ body: W.Bytes) async throws {
        let now = options.clock.now()
        if now >= original.ctx.deadline || receipts.count >= 2048 || !options.valid() { return }
        controlSeq += 1
        var context = W.Context(options.selfID.peer, pcs[0].gate.peer, options.connection, 0, 1, controlSeq, now, 2)
        context.deadline = min(original.ctx.deadline, context.deadline)
        let raw = try options.codec.seal(options.selfID, pcs[0].gate.peer, context, type, body)
        receipts[W.hex(original.hash)] = (raw, original.ctx.deadline + 300_000)
        await transmit(raw)
    }
    private func unsupported(_ o: W.Opened, _ now: UInt64) async throws {
        let f = try PeerSchema.fields(o.payload, "Unsupported"), hash = f[4]!.data
        for (index, p) in pcs.enumerated() {
            guard let pending = p.sender.pending[W.hex(hash)] else { continue }
            let original = try options.codec.openSent(pending.raw, options.selfID, p.gate.peer, now)
            if original.ctx.cls != f[1]!.value || original.type != f[2]!.value || original.ctx.seq != f[3]!.value { return }
            _ = p.sender.ack(hash); p.channel?.close(); p.channel = nil; p.gate.closed = true
            lock.withLock { if _failure == nil { _failure = .peerUnsupported } }
            _ = index; return
        }
    }

    func sendControl(_ type: UInt64, _ body: W.Bytes) {
        queued { [self] in
            try await options.authorize()
            guard active else { return }
            let now = options.clock.now()
            controlSeq += 1
            let context = W.Context(options.selfID.peer, pcs[0].gate.peer, options.connection, 0, 1, controlSeq, now, 2)
            let raw = try options.codec.seal(options.selfID, pcs[0].gate.peer, context, type, body)
            await transmit(raw)
        }
    }

    @discardableResult
    private func signal(_ index: Int, _ type: UInt64, _ body: W.Bytes) async throws -> Bool {
        let p = pcs[index], now = options.clock.now(), generation = p.gate.generation, sender = p.sender
        p.seq += 1
        var context = W.Context(options.selfID.peer, p.gate.peer, options.connection, UInt64(index + 1), p.gate.generation, p.seq, now); context.deadline = options.deadline
        let raw = try options.codec.seal(options.selfID, p.gate.peer, context, type, body)
        if !active || p.gate.generation != generation || p.sender !== sender { return false }
        return sender.enqueue(raw, now, context.deadline, 1)
    }

    private func makePC(_ index: Int) -> Bool {
        let p = pcs[index]
        if p.generation != p.gate.generation { resetPC(index) }
        if p.channel != nil { return true }
        if !active || p.gate.closed || options.clock.now() >= options.deadline { return false }
        let generation = p.gate.generation, o = options
        let box = Box<DirectChannel>()
        let current: @Sendable () -> Bool = { [weak self] in
            guard let self, let channel = box.value else { return false }
            return self.chainOwns(index, channel, generation)
        }
        do {
            let channel = try DirectChannel(DirectChannelOptions(
                peer: String(decoding: o.peer.id, as: UTF8.self), connection: o.connection, use: index + 1, generation: generation, deadline: o.deadline, budget: o.budget,
                valid: { [weak self] in
                    guard let self, self.active else { return false }
                    return self.chainGeneration(index) == generation && (box.value == nil || current())
                },
                stun: o.stun, factory: o.factory,
                candidate: { [weak self] candidate in
                    self?.queued { [weak self] in
                        guard let self, self.active, current() else { return }
                        let p = self.pcs[index]
                        if let candidate {
                            if !p.batch.add(W.utf8(candidate.candidate), generation, self.options.clock.now(), 1) { box.value?.close() }
                        } else { p.ended = true }
                    }
                },
                state: { [weak self] state in
                    self?.queued { [weak self] in
                        guard let self, self.active, current() else { return }
                        if state == .failed || state == .disconnected { try await self.restartPC(index) }
                        else if state == .connected { self.pcs[index].gate.dtls = true; try await self.maybeReady(index) }
                    }
                },
                frame: { [weak self] raw in if let self, self.active, current() { self.options.frame(raw, index + 1) } },
                closed: { [weak self] in if let self, current() { self.options.closed(index + 1) } },
                open: { [weak self] in self?.queued { [weak self] in if let self, self.active, current() { self.options.open?(index + 1) } } },
                probe: { [weak self] ms in if let self, self.active, current() { self.options.probe?(index + 1, ms) } },
                clock: o.clock))
            box.value = channel
            p.channel = channel; publish(index); o.chosen()
            return true
        } catch { return false }
    }
    // The chain's per-use state is read by SDK callbacks on other threads only through these two
    // snapshots; the values they compare are written inside the chain.
    private let mirror = NSLock()
    private var mirrorChannel: [DirectChannel?] = [nil, nil], mirrorGeneration: [UInt64] = [1, 1]
    private func chainGeneration(_ index: Int) -> UInt64 { mirror.withLock { mirrorGeneration[index] } }
    private func chainOwns(_ index: Int, _ channel: DirectChannel, _ generation: UInt64) -> Bool {
        mirror.withLock { mirrorChannel[index] === channel && mirrorGeneration[index] == generation }
    }
    private func publish(_ index: Int) {
        let p = pcs[index]
        mirror.withLock { mirrorChannel[index] = p.channel; mirrorGeneration[index] = p.gate.generation }
    }

    private func maybeOffer(_ index: Int) async throws {
        let p = pcs[index]
        guard active, p.gate.bootReady, p.gate.offerer, !p.gate.localSDP, !p.gate.closed, options.clock.now() < options.deadline else { return }
        if p.channel == nil { options.beforeOffer() }
        guard makePC(index) else { return }
        publish(index)
        try await options.authorize(); guard active else { return }
        let raw = try await p.channel!.description(offer: true), info = try PeerSignal.info(raw)
        try p.gate.setLocal(raw, true, p.gate.generation); p.ufrag = info.0; p.mid = info.1
        try await signal(index, 3, PeerCandidates.sdpBody(raw))
    }

    private func drain(_ index: Int) async throws {
        let p = pcs[index]
        while !p.actions.isEmpty && active {
            let action = p.actions[0]
            if action.generation != p.gate.generation { p.actions.removeFirst(); continue }
            try await options.authorize(); guard active else { return }
            if [3, 4, 5, 6].contains(action.type) {
                if action.type == 3 { options.beforeOffer() }
                guard makePC(index) else { return }
                publish(index)
                if !p.gate.dispatch(action.original, options.clock.now()) { p.actions.removeFirst(); continue }
            }
            p.actions.removeFirst()
            switch action.type {
            case 3, 4:
                try await p.channel!.remote(action.payload, offer: action.type == 3)
                if action.type == 3 {
                    let raw = try await p.channel!.description(offer: false), info = try PeerSignal.info(raw)
                    try p.gate.setLocal(raw, false, p.gate.generation); p.ufrag = info.0; p.mid = info.1
                    try await signal(index, 4, PeerCandidates.sdpBody(raw))
                }
            case 5:
                for c in try PeerCandidates.read(action.payload) {
                    try await p.channel!.addCandidate(RtcIceCandidate(candidate: String(decoding: c.original, as: UTF8.self), sdpMid: String(decoding: c.mid, as: UTF8.self), sdpMLineIndex: Int(c.index), isSrflx: false))
                }
            case 6: try await p.channel!.addCandidate(nil)
            case 8: try await restartPC(index)
            case 9: p.channel?.close()
            default: break
            }
        }
    }

    private func maybeReady(_ index: Int) async throws {
        let p = pcs[index], generation = p.gate.generation
        guard let channel = p.channel, active, !channel.isClosed else { return }
        if !p.confirmed && p.gate.localSDP && p.gate.remoteSDP && p.gate.dtls {
            let confirmed = try await signal(index, 7, p.gate.confirmBody())
            if !active || p.channel !== channel || p.gate.generation != generation || channel.isClosed { return }
            p.confirmed = confirmed
        }
        if p.confirmed && !p.announced && p.gate.ready {
            p.announced = true; lock.withLock { _established = true }
            channel.confirm { options.ready(index + 1, channel) }
        }
    }

    private func tick() async {
        let o = options, now = o.clock.now()
        for p in pcs { if !p.announced { _ = p.gate.tick(now) } else { p.gate.clock = max(p.gate.clock, now) }; p.sender.clock = max(p.sender.clock, now) }
        boot.clock = max(boot.clock, now)
        if peerWindows.values.contains(where: { $0.gap != 0 && now >= $0.gap }) { retireNow(); return }
        controls = controls.filter { now <= $0.value.until }
        receipts = receipts.filter { now <= $0.value.until }
        if now >= o.deadline && !established && !isRetired {
            // Without the computer's proof there was no handshake at all; with it but no
            // server-reflexive address from STUN, the network blocked discovery.
            lock.withLock {
                if _failure == nil { _failure = pcs.allSatisfy({ !$0.gate.bootReady }) ? .noResponse : pcs.contains(where: { $0.channel != nil && !($0.channel!.traversalReady) }) ? .noTraversal : nil }
            }
            close(); return
        }
        for raw in boot.tick(now) { await transmit(raw) }
        for (index, p) in pcs.enumerated() {
            for (key, until) in p.gate.cacheUntil where now > until { p.gate.cacheUntil.removeValue(forKey: key); p.gate.seen.removeValue(forKey: key) }
            if !p.announced && (p.gate.closed || now >= o.deadline) { p.channel?.close(); p.channel = nil; publish(index) }
            if active && p.gate.retired && now < o.deadline { try? await restartPC(index) }
            if active && !p.gate.closed { try? await maybeOffer(index); try? await drain(index); try? await maybeReady(index) }
            var batch = p.batch.batches
            for group in p.batch.flush(now, p.ended, p.gate.generation, 1) {
                let candidates = group.map { PeerCandidates.Candidate(original: $0, ufrag: p.ufrag, mid: p.mid, index: 0, generation: p.gate.generation) }
                _ = try? await signal(index, 5, PeerCandidates.candidateBody(candidates, batch)); batch += 1
            }
            if p.ended && p.batch.pending.isEmpty && p.channel != nil { p.ended = false; _ = try? await signal(index, 6, PeerCandidates.endBody(p.ufrag, p.batch.count, p.batch.batches)) }
            for raw in p.sender.tick(now) { await transmit(raw) }
        }
        lock.withLock { _tickCount += 1 }
    }

    private func resetPC(_ index: Int) {
        let p = pcs[index], old = p.channel; p.channel = nil; old?.close()
        p.generation = p.gate.generation; p.seq = 0; p.sender = PeerSender(options.deadline)
        p.batch = PeerCandidateBatcher(p.gate.generation, options.deadline, 1)
        p.confirmed = false; p.announced = false; p.ended = false; p.restartSent = false; p.ufrag = []; p.mid = []
        publish(index)
    }
    private func restartPC(_ index: Int) async throws {
        let p = pcs[index]
        guard active, options.clock.now() < options.deadline else { return }
        if p.gate.offerer { try p.gate.nextGeneration(p.gate.generation + 1); resetPC(index); p.actions = []; try await maybeOffer(index) }
        else if !p.restartSent { p.restartSent = try await signal(index, 8, W.pb([W.b(1, W.pb([W.u(1, UInt64(index + 1)), W.u(2, p.gate.generation)]))])) }
    }

    @discardableResult
    private func transmit(_ raw: W.Bytes) async -> Bool {
        guard let o = try? options.codec.openSent(raw, options.selfID, pcs[0].gate.peer, options.clock.now()) else { return false }
        let c = o.ctx, deadline = c.deadline, type = o.type, cls = c.cls, use = c.use, generation = c.generation
        let peerID = String(decoding: options.peer.id, as: UTF8.self), connection = W.hex(options.connection), budget = options.budget
        let valid: @Sendable () -> Bool = { [weak self] in
            guard let self, !self.isEnded, self.options.valid(), self.options.clock.now() < deadline else { return false }
            if cls == 1 && use > 0 && use <= 2 && generation != self.chainGeneration(Int(use) - 1) { return false }
            if self.isRetired && cls == 1 && (type == 1 || (type >= 3 && type <= 8)) { return false }
            if cls == 1 && type == 1 && !budget.ownerAvailable(peerID, connection) { return false }
            return true
        }
        return options.send(DirectedOutput(raw: raw, deadline: deadline, critical: cls == 2 || ![5, 6].contains(type), valid: valid))
    }

    func delivery(_ id: W.Bytes, status: UInt32, retry: UInt32, current: @escaping @Sendable () -> Bool) {
        queued { [self] in
            guard current() else { return }
            for sender in [boot] + pcs.map(\.sender) {
                lock.withLock {
                    for pending in sender.pending.values {
                        guard let f = try? W.parse(pending.raw, 5), f[4]?.data == id else { continue }
                        if status == 3 { sender.limited(pending.hash, options.clock.now(), UInt64(retry)) }
                        if status == 2 { sender.offline() }
                    }
                }
            }
        }
    }
    func discovered() { queued { [self] in boot.discovered(); for p in pcs { p.sender.discovered() } } }
    func drainForTest() async { await chain.drain() }
    func tickForTest() async { await queued { await self.tick() }?.value }
    func retire() {
        lock.withLock { retired = true }
        queued { [self] in retireNow() }
    }
    private func retireNow() {
        lock.withLock { retired = true }
        for (index, p) in pcs.enumerated() { p.gate.retire(false); p.channel?.close(); p.channel = nil; p.actions = []; publish(index) }
    }
    func close() {
        let first = lock.withLock { () -> Bool in if ended { return false }; ended = true; return true }
        guard first else { return }
        timer?.cancel()
        // Closing SDK objects can block: the owned channels come out of the mirror, outside every lock.
        for channel in mirror.withLock({ mirrorChannel }) { channel?.close() }
        options.closed(nil)
    }
}

final class Box<T: AnyObject>: @unchecked Sendable {
    private let lock = NSLock(); private var stored: T?
    var value: T? { get { lock.withLock { stored } } set { lock.withLock { stored = newValue } } }
}
