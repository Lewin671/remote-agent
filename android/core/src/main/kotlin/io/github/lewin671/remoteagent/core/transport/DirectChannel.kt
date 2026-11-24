package io.github.lewin671.remoteagent.core.transport

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.cleanup
import io.github.lewin671.remoteagent.core.proto.DirectStateKind
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.proto.Fragment
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import java.nio.charset.StandardCharsets
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Locale
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.ConcurrentMap
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

private const val FRAME_BYTES = 16 shl 10
private const val PAYLOAD_BYTES = FRAME_BYTES - 128

private fun sha256(bytes: ByteArray): ByteArray =
    MessageDigest.getInstance("SHA-256").digest(bytes)

private fun hexBytes(bytes: ByteArray): String = buildString(bytes.size * 2) {
    for (b in bytes) append(String.format(Locale.ROOT, "%02x", b.toInt() and 0xff))
}

private fun decodeStrictUtf8(bytes: ByteArray): String = try {
    StandardCharsets.UTF_8.newDecoder()
        .onMalformedInput(CodingErrorAction.REPORT)
        .onUnmappableCharacter(CodingErrorAction.REPORT)
        .decode(ByteBuffer.wrap(bytes))
        .toString()
} catch (_: Exception) {
    throw PathChanged()
}

private val BARE_LF_OR_CR = Regex("""(^|[^\r])\n|\r(?!\n)""")

/** Minimal platform sanity check before calling WebRTC SDP APIs; semantic validation lives in shared/peerwire. */
internal fun validateMinimalSDP(bytes: ByteArray): String {
    if (bytes.isEmpty() || bytes.size > 60 * 1024) throw PathChanged()
    if (bytes.size >= 3 &&
        bytes[0] == 0xef.toByte() &&
        bytes[1] == 0xbb.toByte() &&
        bytes[2] == 0xbf.toByte()
    ) throw PathChanged()
    val raw = decodeStrictUtf8(bytes)
    if (!raw.endsWith("\r\n") ||
        '\u0000' in raw ||
        raw.any { it == '\u000B' || it == '\u000C' || it == '\u0085' || it == '\u2028' || it == '\u2029' } ||
        BARE_LF_OR_CR.containsMatchIn(raw)
    ) throw PathChanged()
    if ("m=application " !in raw || "a=fingerprint:sha-256 " !in raw) throw PathChanged()
    return raw
}

/** Shared by both PC uses and replacement connections; hints never reset it. */
internal class DirectBudget(private val maxPeers: Int = 4) {
    private class Usage(
        val owner: String,
        val uses: MutableSet<Int> = HashSet(),
        val bytes: IntArray = IntArray(2),
        val frames: IntArray = IntArray(2),
    )

    private val lock = Any()
    private val peers = HashMap<String, Usage>()
    private val totalBytes = IntArray(2)

    fun acquire(peer: String, owner: String, use: Int): Boolean = synchronized(lock) {
        var current = peers[peer]
        if (current == null) {
            if (peers.size >= maxPeers) return false
            current = Usage(owner)
            peers[peer] = current
        }
        if (current.owner != owner || use in current.uses || current.uses.size >= 2) return false
        current.uses.add(use)
        true
    }

    fun ownerAvailable(peer: String, owner: String): Boolean = synchronized(lock) {
        val current = peers[peer]
        current == null || current.owner == owner
    }

    fun release(peer: String, use: Int) = synchronized(lock) {
        val current = peers[peer] ?: return@synchronized
        current.uses.remove(use)
        prune(peer, current)
    }

    fun reserve(peer: String, owner: String, use: Int, direction: Int, bytes: Int, frames: Int): Boolean = synchronized(lock) {
        val current = peers[peer] ?: return false
        if (current.owner != owner || use !in current.uses) return false
        if (bytes < 0 || frames < 0 ||
            current.bytes[direction] + bytes > (2 shl 20) ||
            current.frames[direction] + frames > 128 ||
            totalBytes[direction] + bytes > (16 shl 20)
        ) return false
        current.bytes[direction] += bytes
        current.frames[direction] += frames
        totalBytes[direction] += bytes
        true
    }

    fun free(peer: String, direction: Int, bytes: Int, frames: Int) = synchronized(lock) {
        val current = peers[peer] ?: return@synchronized
        current.bytes[direction] -= bytes
        current.frames[direction] -= frames
        totalBytes[direction] -= bytes
        prune(peer, current)
    }

    private fun prune(peer: String, current: Usage) {
        if (current.uses.isEmpty() && current.bytes[0] == 0 && current.bytes[1] == 0 &&
            current.frames[0] == 0 && current.frames[1] == 0
        ) {
            peers.remove(peer)
        }
    }
}

internal class DirectChannelOptions(
    val peer: String,
    val roundId: Long,
    val use: Int,
    val generation: Long,
    val budget: DirectBudget,
    val valid: () -> Boolean,
    val stun: List<String>,
    val factory: RtcFactory,
    val candidate: (RtcIceCandidate?) -> Unit,
    val state: (DirectStateKind, String) -> Unit,
    val dcState: ((Boolean) -> Unit)? = null,
    val stunError: ((Int, String) -> Unit)? = null,
    val frame: (ByteArray) -> Unit,
    val closed: () -> Unit,
    val open: (() -> Unit)? = null,
    val probe: ((Double) -> Unit)? = null,
    val clock: () -> Long = System::currentTimeMillis,
    val monotonic: () -> Long = { System.nanoTime() / 1_000_000L },
    val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    val receiveDispatcher: CoroutineDispatcher = Dispatchers.Default,
)

/** Framed DataChannel adapter; signaling, state machine, and routing policy live in Go Core. */
internal class DirectChannel(private val options: DirectChannelOptions) {
    private class PartialMessage(
        val count: Int,
        val length: Int,
        val hash: ByteArray,
        val parts: MutableMap<Int, ByteArray> = HashMap(),
        var bytes: Int = 0,
        val until: Long,
    )

    private val connOwner = options.roundId.toString()
    private val random = SecureRandom()
    private val lifetimeJob = SupervisorJob()
    private val scope = CoroutineScope(lifetimeJob + options.dispatcher)
    private val stateLock = Any()
    private val writeMutex = Mutex()
    private val wake = Channel<Unit>(Channel.CONFLATED)
    private val incoming = Channel<ByteArray>(Channel.UNLIMITED)

    @Volatile private var confirmed = false
    @Volatile private var ended = false
    @Volatile private var lastProbeAt = 0L
    private var missedProbes = 0
    private val reservationReleased = AtomicBoolean(false)
    private val writers = AtomicInteger(0)

    private val partial = HashMap<String, PartialMessage>()
    private val waiting = ArrayList<ByteArray>()
    private val probes = ConcurrentHashMap<String, Long>()

    private val pc: RtcPeerConnection
    private val data: RtcDataChannel
    private val probe: RtcDataChannel
    private val tickJob: Job

    init {
        if (!options.valid() ||
            options.stun.size > 2 ||
            options.stun.any { url -> !url.startsWith("stun:") || (url.contains("?") && !url.endsWith("?transport=udp")) } ||
            !options.budget.acquire(options.peer, connOwner, options.use)
        ) {
            throw PathChanged()
        }
        var created: RtcPeerConnection? = null
        try {
            created = options.factory.create(
                options.stun,
                object : RtcPeerListener {
                    override fun onIceCandidate(candidate: RtcIceCandidate) {
                        if (valid()) options.candidate(candidate)
                    }

                    override fun onIceGatheringComplete() {
                        if (valid()) options.candidate(null)
                    }

                    override fun onConnectionStateChange(state: RtcPeerState) {
                        if (!valid()) return
                        options.state(DirectStateKind.DIRECT_STATE_KIND_CONN, state.name.lowercase())
                        if (state == RtcPeerState.FAILED || state == RtcPeerState.CLOSED || state == RtcPeerState.DISCONNECTED) {
                            close()
                        }
                    }

                    override fun onIceConnectionStateChange(state: String) {
                        if (valid()) options.state(DirectStateKind.DIRECT_STATE_KIND_ICE, state)
                    }

                    override fun onIceGatheringStateChange(state: String) {
                        if (valid()) options.state(DirectStateKind.DIRECT_STATE_KIND_GATHER, state)
                    }

                    override fun onIceCandidateError(errorCode: Int, address: String) {
                        if (valid()) options.stunError?.invoke(errorCode, address)
                    }

                    override fun onUnexpectedDataChannel() {
                        if (valid()) close()
                    }
                },
            )
            val dataListener = object : RtcDataChannelListener {
                override fun onStateChange() {
                    val s = data.state()
                    if (!valid()) return
                    if (s == RtcChannelState.OPEN) {
                        options.state(DirectStateKind.DIRECT_STATE_KIND_DC, "open")
                        options.dcState?.invoke(true)
                        wake.trySend(Unit)
                        options.open?.invoke()
                    } else if (s == RtcChannelState.CLOSED) {
                        options.state(DirectStateKind.DIRECT_STATE_KIND_DC, "closed")
                        options.dcState?.invoke(false)
                        close()
                    }
                }

                override fun onBufferedAmountChange(previousAmount: Long) {
                    if (data.bufferedAmount() <= (32 shl 10)) wake.trySend(Unit)
                }

                override fun onMessage(data: ByteArray) {
                    if (!valid()) return
                    if (data.size > FRAME_BYTES) {
                        close()
                        return
                    }
                    incoming.trySend(data)
                }
            }
            val probeListener = object : RtcDataChannelListener {
                override fun onStateChange() {
                    if (valid() && probe.state() == RtcChannelState.CLOSED) close()
                }

                override fun onBufferedAmountChange(previousAmount: Long) {}

                override fun onMessage(data: ByteArray) {
                    if (!ready || data.size != 17) return
                    if (data[0] == 0.toByte()) {
                        val reply = data.copyOf()
                        reply[0] = 1
                        probe.send(reply, true)
                    } else if (data[0] == 1.toByte()) {
                        val id = hexBytes(data.copyOfRange(1, 17))
                        val startNs = probes.remove(id)
                        if (startNs != null) {
                            synchronized(stateLock) { missedProbes = 0 }
                            options.probe?.invoke((System.nanoTime() - startNs) / 1_000_000.0)
                        }
                    }
                }
            }
            pc = created
            data = pc.createDataChannel("remote-agent", 0, ordered = true, maxRetransmits = null, dataListener)
            probe = pc.createDataChannel("remote-agent", 1, ordered = false, maxRetransmits = 0, probeListener)
        } catch (e: Exception) {
            runCatching { created?.close() }
            options.budget.release(options.peer, options.use)
            throw PathChanged()
        }

        scope.launch(options.receiveDispatcher) {
            for (msg in incoming) {
                if (!valid()) break
                try {
                    receive(msg)
                } catch (_: Exception) {
                    close()
                    break
                }
            }
        }

        tickJob = scope.launch {
            while (isActive && !ended) {
                delay(100)
                if (!valid() || (confirmed && (data.state() == RtcChannelState.CLOSED || probe.state() == RtcChannelState.CLOSED))) {
                    close()
                    return@launch
                }
                val now = options.monotonic()
                val nowNs = System.nanoTime()
                val expiredParts = synchronized(stateLock) {
                    val expired = partial.entries.filter { now >= it.value.until }.map { it.key to it.value }
                    for ((k, _) in expired) partial.remove(k)
                    expired
                }
                for ((_, part) in expiredParts) {
                    options.budget.free(options.peer, 1, part.bytes, part.parts.size)
                }
                val tooManyMissed = expireStaleProbes(probes, nowNs) {
                    synchronized(stateLock) { ++missedProbes }
                }
                if (tooManyMissed) {
                    close()
                    return@launch
                }
                if (ready && probe.state() == RtcChannelState.OPEN && now - lastProbeAt >= 1000L) {
                    lastProbeAt = now
                    val raw = ByteArray(17)
                    random.nextBytes(raw)
                    raw[0] = 0
                    probes[hexBytes(raw.copyOfRange(1, 17))] = System.nanoTime()
                    if (!probe.send(raw, true)) {
                        close()
                        return@launch
                    }
                }
            }
        }
    }

    private fun valid(): Boolean = !ended && options.valid()

    val ready: Boolean
        get() = confirmed && valid() && data.state() == RtcChannelState.OPEN

    val queuedBytes: Long
        get() = data.bufferedAmount()

    suspend fun description(offer: Boolean, restart: Boolean = false): ByteArray {
        if (!valid()) throw PathChanged()
        val sdp = if (offer) pc.createOffer(restart) else pc.createAnswer()
        if (!valid() || sdp.isEmpty()) throw PathChanged()
        val raw = sdp.toByteArray(StandardCharsets.UTF_8)
        validateMinimalSDP(raw)
        pc.setLocalDescription(offer, sdp)
        if (!valid()) throw PathChanged()
        return raw
    }

    suspend fun remote(raw: ByteArray, offer: Boolean) {
        if (!valid()) throw PathChanged()
        val sdp = validateMinimalSDP(raw)
        pc.setRemoteDescription(offer, sdp)
        if (!valid()) throw PathChanged()
    }

    suspend fun addCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {
        if (!valid()) throw PathChanged()
        pc.addIceCandidate(candidate, sdpMid, sdpMLineIndex, usernameFragment)
        if (!valid()) throw PathChanged()
    }

    fun confirm() {
        val queued = synchronized(stateLock) {
            if (!valid()) return
            confirmed = true
            waiting.toList().also { waiting.clear() }
        }
        for (raw in queued) deliver(raw)
    }

    suspend fun readStats(): DirectStatsReport {
        val report = runCatching { pc.readStats() }.getOrDefault(DirectStatsReport.getDefaultInstance())
        return if (report.dcState.isNotEmpty()) {
            report
        } else {
            val dcState = runCatching { data.state().name.lowercase() }.getOrDefault("")
            if (dcState.isNotEmpty()) report.toBuilder().setDcState(dcState).build() else report
        }
    }

    suspend fun write(raw: ByteArray, handoff: () -> Unit) {
        val count = (raw.size + PAYLOAD_BYTES - 1) / PAYLOAD_BYTES
        if (raw.isEmpty() || raw.size > (1 shl 20)) throw PathChanged()
        writers.incrementAndGet()
        var reserved = false
        try {
            if (!ready || !options.budget.reserve(options.peer, connOwner, options.use, 0, raw.size, count)) {
                throw PathChanged()
            }
            reserved = true
            val frozen = raw.copyOf()
            writeMutex.withLock {
                writeNow(frozen, handoff)
            }
        } finally {
            if (reserved) options.budget.free(options.peer, 0, raw.size, count)
            writers.decrementAndGet()
            releaseReservation()
        }
    }

    private suspend fun writeNow(raw: ByteArray, handoff: () -> Unit) {
        val hash = sha256(raw)
        val id = ByteArray(16)
        random.nextBytes(id)
        val count = (raw.size + PAYLOAD_BYTES - 1) / PAYLOAD_BYTES
        for (index in 0 until count) {
            while (valid() && (!ready || data.bufferedAmount() > (64 shl 10))) {
                currentCoroutineContext().ensureActive()
                if (ended) throw PathChanged()
                val res = wake.receiveCatching()
                if (res.isClosed) throw PathChanged()
            }
            currentCoroutineContext().ensureActive()
            if (!ready) throw PathChanged()
            val end = minOf(raw.size, (index + 1) * PAYLOAD_BYTES)
            val slice = raw.copyOfRange(index * PAYLOAD_BYTES, end)
            val frame = Fragment.newBuilder()
                .setMessageId(ByteString.copyFrom(id))
                .setIndex(index)
                .setCount(count)
                .setCiphertextLength(raw.size)
                .setCiphertextSha256(ByteString.copyFrom(hash))
                .setPathGeneration(options.generation)
                .setFragment(ByteString.copyFrom(slice))
                .build()
                .toByteArray()
            val maxMsg = pc.maxMessageSize() ?: FRAME_BYTES
            if (frame.size > FRAME_BYTES || (maxMsg > 0 && frame.size > maxMsg)) {
                throw PathChanged()
            }
            if (index == 0) handoff()
            if (!data.send(frame, true)) {
                close()
                throw PathChanged()
            }
        }
    }

    private fun hasUnknownFragmentFields(raw: ByteArray): Boolean {
        val input = com.google.protobuf.CodedInputStream.newInstance(raw)
        while (true) {
            val tag = input.readTag()
            if (tag == 0) return false
            val field = com.google.protobuf.WireFormat.getTagFieldNumber(tag)
            val wire = com.google.protobuf.WireFormat.getTagWireType(tag)
            val ok = when (field) {
                1, 5, 7 -> wire == com.google.protobuf.WireFormat.WIRETYPE_LENGTH_DELIMITED
                2, 3, 4, 6 -> wire == com.google.protobuf.WireFormat.WIRETYPE_VARINT
                else -> false
            }
            if (!ok || !input.skipField(tag)) return true
        }
    }

    private fun receive(raw: ByteArray) {
        if (!valid()) return
        if (hasUnknownFragmentFields(raw)) return
        val frame = Fragment.parseFrom(raw)
        if (frame.messageId.size() != 16 ||
            frame.ciphertextSha256.size() != 32 ||
            frame.pathGeneration != options.generation ||
            frame.count <= 0 || frame.count > 128 ||
            frame.index < 0 || frame.index >= frame.count ||
            frame.ciphertextLength <= 0 || frame.ciphertextLength > (1 shl 20) ||
            frame.fragment.isEmpty
        ) return

        val key = hexBytes(frame.messageId.toByteArray())
        val fragBytes = frame.fragment.toByteArray()
        val msgHash = frame.ciphertextSha256.toByteArray()
        var completed: ByteArray? = null
        var mismatch = false

        synchronized(stateLock) {
            if (!valid()) return
            val part = partial.getOrPut(key) {
                PartialMessage(
                    count = frame.count,
                    length = frame.ciphertextLength,
                    hash = msgHash,
                    until = options.monotonic() + 5000L,
                )
            }
            if (part.count != frame.count || part.length != frame.ciphertextLength || !part.hash.contentEquals(msgHash)) {
                return
            }
            val old = part.parts[frame.index]
            if (old != null) {
                if (!old.contentEquals(fragBytes)) mismatch = true
                return@synchronized
            }
            if (part.bytes + fragBytes.size > part.length) return
            if (!options.budget.reserve(options.peer, connOwner, options.use, 1, fragBytes.size, 1)) return
            part.parts[frame.index] = fragBytes
            part.bytes += fragBytes.size
            if (part.parts.size != part.count) return
            partial.remove(key)
            val totalReserved = part.bytes
            val totalParts = part.parts.size
            if (part.bytes != part.length || !options.budget.reserve(options.peer, connOwner, options.use, 1, part.bytes, 1)) {
                options.budget.free(options.peer, 1, totalReserved, totalParts)
                return
            }
            val buf = ByteArray(part.length)
            var offset = 0
            for (i in 0 until part.count) {
                val p = part.parts[i]!!
                System.arraycopy(p, 0, buf, offset, p.size)
                offset += p.size
            }
            options.budget.free(options.peer, 1, totalReserved, totalParts)
            completed = buf
        }

        if (mismatch) {
            close()
            return
        }
        val full = completed ?: return
        val hashValid = try {
            msgHash.contentEquals(sha256(full))
        } catch (e: Exception) {
            options.budget.free(options.peer, 1, full.size, 1)
            throw e
        }
        if (!hashValid || !valid()) {
            options.budget.free(options.peer, 1, full.size, 1)
            return
        }
        val deliverNow = synchronized(stateLock) {
            if (ended) {
                options.budget.free(options.peer, 1, full.size, 1)
                return
            }
            if (confirmed) true
            else {
                waiting.add(full)
                false
            }
        }
        if (deliverNow) deliver(full)
    }

    private fun deliver(raw: ByteArray) {
        options.budget.free(options.peer, 1, raw.size, 1)
        if (valid() && confirmed) options.frame(raw)
    }

    fun close() {
        val toFreeParts: List<PartialMessage>
        val toFreeWaiting: List<ByteArray>
        synchronized(stateLock) {
            if (ended) return
            ended = true
            toFreeParts = partial.values.toList()
            partial.clear()
            toFreeWaiting = waiting.toList()
            waiting.clear()
        }
        for (part in toFreeParts) options.budget.free(options.peer, 1, part.bytes, part.parts.size)
        for (raw in toFreeWaiting) options.budget.free(options.peer, 1, raw.size, 1)
        probes.clear()
        wake.close()
        incoming.close()
        tickJob.cancel()
        lifetimeJob.cancel()
        cleanup(
            { options.closed() },
            { releaseReservation() },
        )
        // WebRTC PeerConnection.close() can block inside native locks; run off-caller so callers and DirectBudget never stall.
        CoroutineScope(Dispatchers.IO).launch { runCatching { pc.close() } }
    }

    private fun releaseReservation() {
        if (ended && writers.get() == 0 && reservationReleased.compareAndSet(false, true)) {
            options.budget.release(options.peer, options.use)
        }
    }
}

internal fun expireStaleProbes(
    probes: ConcurrentMap<String, Long>,
    nowNs: Long,
    onMissed: () -> Int,
): Boolean {
    for ((id, startNs) in probes.entries) {
        if (nowNs - startNs >= 1_000_000_000L && probes.remove(id, startNs)) {
            if (onMissed() >= 3) {
                return true
            }
        }
    }
    return false
}
