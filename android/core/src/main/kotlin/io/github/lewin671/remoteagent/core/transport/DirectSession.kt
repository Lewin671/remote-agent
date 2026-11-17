package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.peer.Schema
import io.github.lewin671.remoteagent.core.peer.Sdp
import io.github.lewin671.remoteagent.core.peer.Sender
import io.github.lewin671.remoteagent.core.peer.Signal
import io.github.lewin671.remoteagent.core.peer.Wire
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.util.concurrent.atomic.AtomicBoolean

internal data class DirectedOutput(
    val raw: ByteArray,
    val deadline: Long,
    val critical: Boolean,
    val valid: () -> Boolean,
)

internal class DirectSessionOptions(
    val self: Wire.Identity,
    val peer: Wire.Identity,
    val codec: Wire,
    val connection: ByteArray,
    val deadline: Long,
    val budget: DirectBudget,
    val stun: List<String>,
    val factory: RtcFactory,
    val valid: () -> Boolean,
    val authorize: suspend () -> Unit,
    val send: (DirectedOutput) -> Boolean,
    val chosen: () -> Unit,
    val beforeOffer: () -> Unit,
    val ready: (Int, DirectChannel) -> Unit,
    val closed: (Int?) -> Unit,
    val closeReason: ((DirectFailReason) -> Unit)? = null,
    val frame: (ByteArray, Int) -> Unit,
    val capability: (Wire.Opened) -> Unit,
    val probe: ((Int, Double) -> Unit)? = null,
    val clock: () -> Long = System::currentTimeMillis,
    val monotonic: () -> Long = ::monotonicMillis,
    val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    /** Where the bulk path's frames are reassembled and checked: not on every core. */
    val bulkReceiveDispatcher: CoroutineDispatcher = CpuLanes.bulk,
)

/** Serial owner for one connection and both uses. Deferred work keeps original bytes. */
internal class DirectSession(val options: DirectSessionOptions) {
    private class PC(
        val gate: Signal,
        var generation: Long,
        var sender: Sender,
        var batch: Sender.CandidateBatcher,
    ) {
        var restartSent = false
        var seq = 0L
        @Volatile var channel: DirectChannel? = null
        var ufrag = ByteArray(0)
        var mid = ByteArray(0)
        var ended = false
        var confirmed = false
        var announced = false
        val actions = ArrayDeque<Signal.Action>()
    }

    private class CachedItem(val bytes: ByteArray, val until: Long)
    private class PendingPeerScope(val opened: Wire.Opened, val at: Long)
    private class PeerWindow {
        var next = 1L
        val pending = HashMap<Long, PendingPeerScope>()
        var gap = 0L
    }

    private val actorDispatcher = options.dispatcher.limitedParallelism(1)
    private val actorJob = SupervisorJob()
    private val scope = CoroutineScope(actorJob + actorDispatcher)

    private val pcs: Array<PC>
    private val boot: Sender
    private val endedFlag = AtomicBoolean(false)
    private val retiredFlag = AtomicBoolean(false)
    private val tickQueued = AtomicBoolean(false)
    private val incomingCount = java.util.concurrent.atomic.AtomicInteger(0)
    private val incomingBytesCount = java.util.concurrent.atomic.AtomicInteger(0)

    private var controlSeq = 0L
    private var challenge: ByteArray? = null
    private val receipts = HashMap<String, CachedItem>()
    private val controls = HashMap<String, CachedItem>()
    private val peerWindows = HashMap<Long, PeerWindow>()
    @Volatile var retainUntil: Long
        private set
    @Volatile var established = false
        private set
    @Volatile private var peerResponded = false
    @Volatile private var unsupportedSeen = false
    @Volatile private var sawTraversal = false

    private val bornAt = options.clock()
    private val signalTrace = ArrayList<String>()
    private var roundReason: String? = null
    private val tickJob: Job
    private val mailbox = Channel<suspend () -> Unit>(Channel.UNLIMITED)

    init {
        val now = options.clock()
        if (!options.valid() || options.deadline <= now || options.deadline > now + 10_000L) {
            throw PathChanged()
        }
        fun makePc(use: Int): PC {
            val gate = Signal(options.codec, options.self, options.peer, options.connection, use.toLong(), now)
            gate.roundDeadline = options.deadline
            gate.current = java.util.function.BooleanSupplier { options.valid() }
            gate.leaseUntil = Long.MAX_VALUE
            return PC(gate, 1L, Sender(options.deadline), Sender.CandidateBatcher(1L, options.deadline, 1L))
        }
        pcs = arrayOf(makePc(1), makePc(2))
        boot = Sender(options.deadline)
        retainUntil = options.deadline + 300_000L

        // The SDK is slow to start on a phone; do it beside the handshake, not in it.
        scope.launch(Dispatchers.Default) {
            val t = options.clock()
            runCatching { options.factory.prewarm() }
            trace("warm", options.clock() - t, 0L, true)
        }
        scope.launch {
            for (work in mailbox) {
                if (endedFlag.get()) break
                runCatching { work() }
            }
        }

        tickJob = scope.launch {
            while (isActive && !endedFlag.get()) {
                delay(25)
                if (endedFlag.get()) return@launch
                val t = options.clock()
                for (p in pcs) {
                    if (!p.announced) p.gate.tick(t) else p.gate.clock = maxOf(p.gate.clock, t)
                    p.sender.advanceClock(t)
                }
                boot.advanceClock(t)
                if (!options.valid() || (retiredFlag.get() && t >= retainUntil)) {
                    closeWhy(if (!options.valid()) "not_allowed" else "retired_replay_window_over")
                    return@launch
                }
                if (tickQueued.compareAndSet(false, true)) {
                    queue {
                        try { tick() } finally { tickQueued.set(false) }
                    }
                }
            }
        }
    }

    val peer: Wire.Identity get() = pcs[0].gate.peer
    val active: Boolean get() = !endedFlag.get() && !retiredFlag.get() && options.valid()

    private fun queue(work: suspend () -> Unit) {
        if (!endedFlag.get()) {
            val queuedAt = options.clock()
            mailbox.trySend {
                val startedAt = options.clock()
                work()
                val ended = options.clock()
                // A job that waited or ran long holds up every message behind it.
                if (startedAt - queuedAt >= 1000L || ended - startedAt >= 300L) {
                    trace("job", ended - startedAt, startedAt - queuedAt, true)
                }
            }
        }
    }

    private suspend fun authorizeTimed() {
        val t = options.clock()
        options.authorize()
        val dt = options.clock() - t
        if (dt >= 100L) trace("auth", dt, 0L, true)
    }

    fun start() {
        queue {
            authorizeTimed()
            if (!active) return@queue
            val now = options.clock()
            val ctx = Wire.probeContext(options.self, options.peer, options.connection, 1L, now)
            ctx.deadline = options.deadline
            val body = Wire.pb(
                Wire.b(1, options.connection),
                Wire.b(2, options.self.instance),
                Wire.b(3, options.self.boot),
            )
            val raw = options.codec.seal(options.self, options.peer, ctx, 1L, body, null, null, null)
            val opened = options.codec.openSent(raw, options.self, options.peer, now)
            for (p in pcs) p.gate.rememberProbe(opened)
            boot.enqueue(raw, now, options.deadline, 1L)
        }
    }

    fun receive(raw: ByteArray): Boolean {
        if (endedFlag.get() || raw.size > (64 shl 10)) return false
        val count = incomingCount.incrementAndGet()
        val total = incomingBytesCount.addAndGet(raw.size)
        if (count > 8 || total > (1 shl 20)) {
            trace("drop", 0L, 0L, false)
            incomingCount.decrementAndGet()
            incomingBytesCount.addAndGet(-raw.size)
            return false
        }
        val frozen = raw.copyOf()
        trace("in", 0L, 0L, true)
        queue {
            try {
                receiveNow(frozen)
            } finally {
                incomingCount.decrementAndGet()
                incomingBytesCount.addAndGet(-frozen.size)
            }
        }
        return true
    }

    private suspend fun receiveNow(raw: ByteArray) {
        authorizeTimed()
        if (endedFlag.get() || !options.valid()) return
        val peer = pcs[0].gate.peer
        val o = options.codec.open(raw, peer, options.self, options.clock())
        val c = o.ctx
        val now = options.clock()
        if (endedFlag.get() || !options.valid() || now >= c.deadline) return
        if (!c.connection.contentEquals(options.connection)) return
        trace("rx", o.type, c.use, true)
        peerResponded = true
        Schema.known(c.cls, o.type, o.payload)
        val key = Wire.cacheKey(c)
        val oldControl = controls[key]
        if (retiredFlag.get()) {
            if (oldControl != null && oldControl.bytes.contentEquals(o.hash)) {
                replyCached(o)
                return
            }
            for (p in pcs) {
                if (p.gate.replyAllowed(raw, now)) {
                    if (c.cls == 1L && o.type == 1L && challenge != null) transmit(challenge!!)
                    else ack(o)
                    break
                }
            }
            return
        }
        if (pcs[0].gate.seen.size + pcs[1].gate.seen.size + controls.size >= 2047 &&
            oldControl == null &&
            pcs.none { key in it.gate.seen }
        ) return

        retainUntil = maxOf(retainUntil, c.deadline + 300_000L)

        if (c.cls == 2L && o.type == 4L) {
            val fields = Schema.fields(o.payload, "Ack")
            val hash = Wire.getBytes(fields, 6)
            for (sender in listOf(boot, pcs[0].sender, pcs[1].sender)) {
                val sent = sender.pending[Wire.hex(hash)] ?: continue
                val original = options.codec.openSent(sent.raw, options.self, peer, now)
                val q = original.ctx
                if (q.cls != Wire.getUint(fields, 4) ||
                    q.seq != Wire.getUint(fields, 5) ||
                    q.generation != Wire.getUint(fields, 7) ||
                    q.use != Wire.getUint(fields, 8)
                ) return
                if (original.type == 2L && q.cls == 1L && q.generation == 0L) {
                    for (p in pcs) p.gate.receive(raw, now)
                }
                sender.ack(hash)
                for (i in 0..1) maybeOffer(i)
                return
            }
            return
        }

        if (c.cls == 1L && (o.type == 1L || o.type == 2L)) {
            for (p in pcs) p.gate.receive(raw, now)
            if (o.type == 1L) {
                if (challenge == null) {
                    val learned = pcs[0].gate.peer
                    val ctx = Wire.Context(options.self, learned, options.connection, 0L, 0L, 1L, now, 1L, true)
                    ctx.deadline = minOf(options.deadline, c.deadline)
                    val body = Wire.pb(
                        Wire.b(1, options.connection),
                        Wire.b(2, options.self.instance),
                        Wire.b(3, options.self.boot),
                        Wire.b(4, learned.boot),
                        Wire.b(6, o.hash),
                    )
                    val chRaw = options.codec.seal(options.self, learned, ctx, 2L, body, null, null, null)
                    challenge = chRaw
                    val sent = options.codec.openSent(chRaw, options.self, learned, now)
                    for (p in pcs) p.gate.rememberChallenge(sent)
                    boot.enqueue(chRaw, now, ctx.deadline, 1L)
                } else if (pcs[0].gate.replyAllowed(raw, now)) {
                    transmit(challenge!!)
                }
            } else {
                pcs[0].gate.probe?.let { boot.ack(it.hash) }
                ack(o)
                for (i in 0..1) maybeOffer(i)
            }
            return
        }

        if (c.cls == 2L) {
            if (oldControl != null) {
                if (oldControl.bytes.contentEquals(o.hash)) replyCached(o)
                return
            }
            if (o.type == 1L || o.type == 14L) {
                val fields = Schema.fields(o.payload, if (o.type == 1L) "Capabilities" else "CapabilitySnapshot")
                val expires = Wire.getUint(fields, if (o.type == 1L) 4 else 3)
                if (expires <= now || expires <= c.issued || expires - c.issued > 60_000L) return
                options.capability(o)
            }
            controls[key] = CachedItem(o.hash, c.deadline + 300_000L)
            if (o.type == 10L) {
                unsupported(o, now)
                return
            }
            if (o.type == 11L || o.type == 12L || o.type == 13L) return
            if (o.type == 1L || o.type == 14L) ack(o)
            else respond(o, 10L, Wire.pb(Wire.u(1, c.cls), Wire.u(2, o.type), Wire.u(3, c.seq), Wire.b(4, o.hash)))
            return
        }

        if (c.cls == 1L && c.use == 0L) {
            receivePeerScope(o, now)
            return
        }
        if (c.use < 1L || c.use > 2L) return
        val index = (c.use - 1L).toInt()
        val p = pcs[index]
        val actions = p.gate.receive(raw, now)
        if (p.gate.replyAllowed(raw, now)) ack(o)
        for (action in actions) {
            if (action.cls == 2L && action.type == 10L) {
                val original = options.codec.open(action.original, p.gate.peer, options.self, now)
                respond(original, 10L, action.payload)
            } else if (action.cls == 1L) {
                if (p.actions.size >= 32) {
                    retire()
                    return
                }
                p.actions.addLast(action)
            }
        }
        drain(index)
        maybeReady(index)
    }

    private fun validPeerScope(o: Wire.Opened): Boolean {
        if (o.type == 9L) return true
        if (o.type != 8L || !pcs[0].gate.offerer()) return false
        val targets = Schema.restart(o.payload)
        return targets.entries.all { (use, gen) ->
            use in 1L..2L && pcs[(use - 1L).toInt()].gate.generation == gen
        }
    }

    private suspend fun receivePeerScope(initial: Wire.Opened, now: Long) {
        var o = initial
        if (now >= options.deadline) return
        val key = Wire.cacheKey(o.ctx)
        val old = controls[key]
        if (old != null) {
            if (old.bytes.contentEquals(o.hash)) replyCached(o)
            return
        }
        if (!validPeerScope(o)) return
        val window = peerWindows.getOrPut(o.ctx.generation) {
            if (peerWindows.size >= 32) return
            PeerWindow()
        }
        if (java.lang.Long.compareUnsigned(o.ctx.seq, window.next) < 0 || window.pending.size >= 32) return
        controls[key] = CachedItem(o.hash, o.ctx.deadline + 300_000L)
        if (java.lang.Long.compareUnsigned(o.ctx.seq, window.next) > 0) {
            window.pending[o.ctx.seq] = PendingPeerScope(o, now)
            if (window.gap == 0L) {
                window.gap = minOf(now + 2500L, o.ctx.deadline, options.deadline)
            }
            return
        }
        while (true) {
            val fresh = options.codec.open(o.raw, peer, options.self, options.clock())
            if (!active || !validPeerScope(fresh)) {
                retire()
                return
            }
            ack(fresh)
            if (fresh.type == 9L) {
                retire()
                return
            }
            for ((use, _) in Schema.restart(fresh.payload)) {
                restartPC((use - 1L).toInt())
            }
            window.next++
            val nextItem = window.pending.remove(window.next) ?: break
            o = nextItem.opened
        }
        window.gap = if (window.pending.isEmpty()) 0L
        else window.pending.values.minOf { minOf(it.at + 2500L, it.opened.ctx.deadline, options.deadline) }
    }

    private fun ack(o: Wire.Opened) {
        if ((o.ctx.cls == 1L && o.type == 1L) || (o.ctx.cls == 2L && o.type in setOf(4L, 10L, 11L, 12L, 13L))) return
        if (replyCached(o)) return
        if (o.ctx.cls == 1L && (o.type < 1L || o.type > 9L)) return
        respond(
            o,
            4L,
            Wire.pb(
                Wire.u(4, o.ctx.cls),
                Wire.u(5, o.ctx.seq),
                Wire.b(6, o.hash),
                Wire.u(7, o.ctx.generation),
                Wire.u(8, o.ctx.use),
            ),
        )
    }

    private fun replyCached(o: Wire.Opened): Boolean {
        val reply = receipts[Wire.hex(o.hash)] ?: return false
        if (options.clock() < o.ctx.deadline) transmit(reply.bytes)
        return true
    }

    private fun respond(original: Wire.Opened, type: Long, body: ByteArray) {
        val now = options.clock()
        if (now >= original.ctx.deadline || receipts.size >= 2048 || !options.valid()) return
        val ctx = Wire.Context(options.self, pcs[0].gate.peer, options.connection, 0L, 1L, ++controlSeq, now, 2L, false)
        if (original.ctx.deadline < ctx.deadline) ctx.deadline = original.ctx.deadline
        val raw = options.codec.seal(options.self, pcs[0].gate.peer, ctx, type, body, null, null, null)
        receipts[Wire.hex(original.hash)] = CachedItem(raw, original.ctx.deadline + 300_000L)
        transmit(raw)
    }

    private fun unsupported(o: Wire.Opened, now: Long) {
        val fields = Schema.fields(o.payload, "Unsupported")
        val hash = Wire.getBytes(fields, 4)
        for ((index, p) in pcs.withIndex()) {
            val pending = p.sender.pending[Wire.hex(hash)] ?: continue
            val original = options.codec.openSent(pending.raw, options.self, p.gate.peer, now)
            if (original.ctx.cls != Wire.getUint(fields, 1) ||
                original.type != Wire.getUint(fields, 2) ||
                original.ctx.seq != Wire.getUint(fields, 3)
            ) return
            p.sender.ack(hash)
            val old = p.channel
            p.channel = null
            old?.close()
            p.gate.closed = true
            unsupportedSeen = true
            options.closeReason?.invoke(DirectFailReason.PEER_UNSUPPORTED)
            return
        }
    }

    private fun signal(index: Int, type: Long, body: ByteArray): Boolean {
        val p = pcs[index]
        val now = options.clock()
        val generation = p.gate.generation
        val sender = p.sender
        val ctx = Wire.Context(options.self, p.gate.peer, options.connection, (index + 1).toLong(), p.gate.generation, ++p.seq, now, 1L, false)
        ctx.deadline = options.deadline
        val raw = options.codec.seal(options.self, p.gate.peer, ctx, type, body, null, null, null)
        if (!active || p.gate.generation != generation || p.sender !== sender) return false
        return sender.enqueue(raw, now, ctx.deadline, 1L)
    }

    private fun makePC(index: Int): Boolean {
        val p = pcs[index]
        if (p.generation != p.gate.generation) resetPC(index)
        if (p.channel != null) return true
        if (!active || p.gate.closed || options.clock() >= options.deadline) return false
        val generation = p.gate.generation
        var created: DirectChannel? = null
        val isCurrent = { created != null && p.channel === created && p.generation == generation && p.gate.generation == generation }
        return try {
            val ch = DirectChannel(
                DirectChannelOptions(
                    peer = Wire.text(options.peer.id),
                    connection = options.connection,
                    use = index + 1,
                    generation = generation,
                    deadline = options.deadline,
                    budget = options.budget,
                    valid = { active && p.gate.generation == generation && (created == null || isCurrent()) },
                    stun = options.stun,
                    factory = options.factory,
                    candidate = { cand ->
                        queue {
                            if (!active || !isCurrent()) return@queue
                            if (cand == null) {
                                p.ended = true
                            } else if (!p.batch.add(Wire.utf8(cand.candidate), generation, options.clock(), 1L)) {
                                created?.close()
                            }
                        }
                    },
                    state = { st ->
                        queue {
                            if (!active || !isCurrent()) return@queue
                            if (st == RtcPeerState.FAILED || st == RtcPeerState.DISCONNECTED) {
                                restartPC(index)
                            } else if (st == RtcPeerState.CONNECTED && created?.aborted == false) {
                                p.gate.dtls = true
                                maybeReady(index)
                            }
                        }
                    },
                    frame = { raw -> if (active && isCurrent()) options.frame(raw, index + 1) },
                    closed = { if (isCurrent()) options.closed(index + 1) },
                    probe = { elapsed -> if (active && isCurrent()) options.probe?.invoke(index + 1, elapsed) },
                    clock = options.clock,
                    monotonic = options.monotonic,
                    dispatcher = options.dispatcher,
                    receiveDispatcher = if (index == 1) options.bulkReceiveDispatcher else Dispatchers.Default,
                ),
            )
            created = ch
            if (endedFlag.get() || retiredFlag.get()) {
                ch.close()
                false
            } else {
                p.channel = ch
                options.chosen()
                true
            }
        } catch (_: Exception) {
            false
        }
    }

    private suspend fun maybeOffer(index: Int) {
        val p = pcs[index]
        if (!active || !p.gate.bootReady || !p.gate.offerer() || p.gate.localSDP || p.gate.closed || options.clock() >= options.deadline) return
        if (p.channel == null) options.beforeOffer()
        if (!makePC(index)) return
        authorizeTimed()
        if (!active) return
        val ch = p.channel ?: return
        val raw = ch.description(offer = true)
        val info = Sdp.inspectSDP(raw)
        p.gate.setLocal(raw, true, p.gate.generation)
        p.ufrag = info.ufrag
        p.mid = info.mid
        signal(index, 3L, Sdp.sdpBody(raw))
    }

    private suspend fun drain(index: Int) {
        val p = pcs[index]
        while (p.actions.isNotEmpty() && active) {
            val action = p.actions.first()
            if (action.generation != p.gate.generation) {
                p.actions.removeFirst()
                continue
            }
            authorizeTimed()
            if (!active) return
            if (action.type in setOf(3L, 4L, 5L, 6L)) {
                if (action.type == 3L) options.beforeOffer()
                if (!makePC(index)) return
                if (!p.gate.dispatch(action.original, options.clock())) {
                    p.actions.removeFirst()
                    continue
                }
            }
            p.actions.removeFirst()
            val ch = p.channel
            when (action.type) {
                3L, 4L -> {
                    ch!!.remote(action.payload, action.type == 3L)
                    if (action.type == 3L) {
                        val raw = ch.description(offer = false)
                        val info = Sdp.inspectSDP(raw)
                        p.gate.setLocal(raw, false, p.gate.generation)
                        p.ufrag = info.ufrag
                        p.mid = info.mid
                        signal(index, 4L, Sdp.sdpBody(raw))
                    }
                }
                5L -> {
                    for (cand in Sdp.readCandidates(action.payload).list) {
                        ch!!.addCandidate(Wire.text(cand.original), Wire.text(cand.mid), cand.index.toInt(), Wire.text(cand.ufrag))
                    }
                }
                6L -> Unit // libwebrtc requires no null end-of-candidates call
                8L -> restartPC(index)
                9L -> ch?.close()
            }
        }
    }

    private fun maybeReady(index: Int) {
        val p = pcs[index]
        val ch = p.channel ?: return
        val generation = p.gate.generation
        if (!active || ch.aborted) return
        if (!p.confirmed && p.gate.localSDP && p.gate.remoteSDP && p.gate.dtls) {
            val ok = signal(index, 7L, p.gate.confirmBody())
            if (!active || p.channel !== ch || p.gate.generation != generation || ch.aborted) return
            p.confirmed = ok
        }
        if (ch.traversalReady) sawTraversal = true
        // A connected DTLS transport and the peer's confirmation prove the pair works; a
        // server-reflexive candidate is not required (a computer with a public address has
        // none, and neither has any pair whose STUN is unreachable). It only explains a failure.
        if (p.confirmed && !p.announced && p.gate.ready()) {
            p.announced = true
            established = true
            ch.confirm()
            options.ready(index + 1, ch)
        }
    }

    private suspend fun tick() {
        val now = options.clock()
        if (peerWindows.values.any { it.gap != 0L && now >= it.gap }) {
            retire()
            return
        }
        controls.entries.removeIf { now > it.value.until }
        receipts.entries.removeIf { now > it.value.until }
        if (now >= options.deadline && !established && !retiredFlag.get()) {
            closeWhy("round_deadline")
            return
        }
        for (raw in boot.tick(now)) transmit(raw)
        for ((index, p) in pcs.withIndex()) {
            for (k in p.gate.cacheUntil.keys.toList()) {
                val until = p.gate.cacheUntil[k] ?: continue
                if (now > until) {
                    p.gate.cacheUntil.remove(k)
                    p.gate.seen.remove(k)
                }
            }
            if (!p.announced && (p.gate.closed || now >= options.deadline)) {
                val old = p.channel
                p.channel = null
                old?.close()
            }
            if (active && p.gate.retired && now < options.deadline) restartPC(index)
            if (active && !p.gate.closed) {
                maybeOffer(index)
                drain(index)
                maybeReady(index)
            }
            var batchIdx = p.batch.batches
            for (group in p.batch.flush(now, p.ended, p.gate.generation, 1L)) {
                val items = group.map { orig -> Sdp.CandidateItem(orig, p.ufrag, p.mid, 0L, p.gate.generation) }
                signal(index, 5L, Sdp.candidateBody(items, batchIdx++))
            }
            if (p.ended && p.batch.pending.isEmpty() && p.channel != null) {
                p.ended = false
                signal(index, 6L, Sdp.endBody(p.ufrag, p.batch.count, p.batch.batches))
            }
            for (raw in p.sender.tick(now)) transmit(raw)
        }
    }

    private fun resetPC(index: Int) {
        val p = pcs[index]
        val old = p.channel
        p.channel = null
        old?.close()
        p.generation = p.gate.generation
        p.seq = 0L
        p.sender = Sender(options.deadline)
        p.batch = Sender.CandidateBatcher(p.gate.generation, options.deadline, 1L)
        p.confirmed = false
        p.announced = false
        p.ended = false
        p.restartSent = false
        p.ufrag = ByteArray(0)
        p.mid = ByteArray(0)
    }

    private suspend fun restartPC(index: Int) {
        val p = pcs[index]
        if (!active || options.clock() >= options.deadline) return
        if (p.gate.offerer()) {
            p.gate.nextGeneration(p.gate.generation + 1L)
            resetPC(index)
            p.actions.clear()
            maybeOffer(index)
        } else if (!p.restartSent) {
            p.restartSent = signal(
                index,
                8L,
                Wire.pb(Wire.b(1, Wire.pb(Wire.u(1, (index + 1).toLong()), Wire.u(2, p.gate.generation)))),
            )
        }
    }

    private fun transmit(raw: ByteArray): Boolean {
        val o = options.codec.openSent(raw, options.self, pcs[0].gate.peer, options.clock())
        val c = o.ctx
        val sent = options.send(
            DirectedOutput(
                raw = raw,
                deadline = c.deadline,
                critical = c.cls == 2L || (o.type != 5L && o.type != 6L),
                valid = {
                    !endedFlag.get() &&
                        options.valid() &&
                        options.clock() < c.deadline &&
                        !(c.cls == 1L && c.use in 1L..2L && c.generation != pcs[(c.use - 1L).toInt()].generation) &&
                        !(retiredFlag.get() && c.cls == 1L && (o.type == 1L || o.type in 3L..8L)) &&
                        !(c.cls == 1L && o.type == 1L && !options.budget.ownerAvailable(Wire.text(options.peer.id), Wire.hex(options.connection)))
                },
            ),
        )
        trace("tx", o.type, c.use, sent)
        return sent
    }

    fun delivery(id: ByteArray, status: Int, retryMs: Int, current: () -> Boolean) {
        queue {
            if (!current()) return@queue
            for (sender in listOf(boot, pcs[0].sender, pcs[1].sender)) {
                for (pending in sender.pending.values) {
                    val f = Wire.parse(pending.raw, 5)
                    if (!Wire.getBytes(f, 4).contentEquals(id)) continue
                    if (status == 3) sender.limited(pending.hash, options.clock(), retryMs.toLong())
                    if (status == 2) sender.offline()
                }
            }
        }
    }

    fun discovered() {
        queue {
            boot.discovered()
            for (p in pcs) p.sender.discovered()
        }
    }

    fun retire() {
        if (!retiredFlag.compareAndSet(false, true)) return
        DirectDiag.note("round retired at=${options.clock() - bornAt} established=$established")
        val toClose = ArrayList<DirectChannel>(2)
        for (p in pcs) {
            p.gate.retire(false)
            val ch = p.channel
            p.channel = null
            if (ch != null) toClose.add(ch)
        }
        queue { for (p in pcs) p.actions.clear() }
        for (ch in toClose) ch.close()
    }

    /** The kind (not the content) of each signal sent or received, with the use and ms since the round began; at most 40. */
    private fun trace(dir: String, type: Long, use: Long, ok: Boolean) {
        if (dir == "rx" && type == 4L && use == 0L || dir == "tx" && type == 4L && use == 0L) return // acknowledgements
        if (dir == "in") return
        synchronized(signalTrace) {
            if (signalTrace.size < 64) signalTrace.add("$dir$type.$use${if (ok) "" else "!"}@${options.clock() - bornAt}")
        }
    }

    /** Records why the round ends (the first reason wins) and closes it. */
    private fun closeWhy(reason: String) {
        if (roundReason == null) roundReason = reason
        close()
    }

    fun close() {
        if (!endedFlag.compareAndSet(false, true)) return
        tickJob.cancel()
        val steps = pcs.joinToString(" ") { p ->
            val g = p.gate
            "pc${g.use}=" + listOf(g.bootReady to 'b', g.localSDP to 'l', g.remoteSDP to 'r', g.dtls to 'd', p.confirmed to 'c', p.announced to 'a')
                .joinToString("") { (on, c) -> if (on) c.toString() else "-" }
        }
        DirectDiag.note("round end=${roundReason ?: "closed_by_owner"} at=${options.clock() - bornAt} established=$established $steps signals=${synchronized(signalTrace) { signalTrace.joinToString(" ") }}")
        val anyTraversal = sawTraversal || pcs.any { it.channel?.traversalReady == true }
        val reason = when {
            unsupportedSeen -> DirectFailReason.PEER_UNSUPPORTED
            established -> DirectFailReason.FAILED
            !peerResponded -> DirectFailReason.NO_RESPONSE
            !anyTraversal -> DirectFailReason.NO_TRAVERSAL
            else -> DirectFailReason.FAILED
        }
        val toClose = ArrayList<DirectChannel>(2)
        for (p in pcs) {
            val ch = p.channel
            p.channel = null
            if (ch != null) toClose.add(ch)
        }
        actorJob.cancel()
        mailbox.close()
        if (!retiredFlag.get()) options.closeReason?.invoke(reason)
        for (ch in toClose) runCatching { ch.close() }
        options.closed(null)
    }
}
