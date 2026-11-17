package io.github.lewin671.remoteagent.core.transport

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Authority
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.peer.Schema
import io.github.lewin671.remoteagent.core.peer.Wire
import io.github.lewin671.remoteagent.proto.CapabilitySnapshot
import io.github.lewin671.remoteagent.proto.DirectedPeerFrame
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardResponse
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.PeerFrameResult
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters
import java.net.URI
import java.security.SecureRandom
import java.util.concurrent.ConcurrentHashMap

interface DirectPreferenceStore {
    fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean
    fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean)
}

class InMemoryDirectPreferenceStore : DirectPreferenceStore {
    private val map = ConcurrentHashMap<String, Boolean>()
    override fun isEnabled(server: String, selfDeviceId: String, machineId: String): Boolean =
        map["$server:$selfDeviceId:$machineId"] ?: true

    override fun setEnabled(server: String, selfDeviceId: String, machineId: String, enabled: Boolean) {
        map["$server:$selfDeviceId:$machineId"] = enabled
    }
}

/** Account owner for optional direct paths. It never owns a logical request or
 * its outcome: Link and Remote retain authorization and delivery evidence. */
internal class DirectEngine(
    private val account: Account,
    private val rtcFactory: RtcFactory?,
    private val preferenceStore: DirectPreferenceStore = InMemoryDirectPreferenceStore(),
    private val authorize: suspend () -> Unit,
    private val changed: () -> Unit,
    private val clock: () -> Long = System::currentTimeMillis,
    private val monotonic: () -> Long = ::monotonicMillis,
    private val stunUrls: List<String> = defaultStunUrls(account.server),
    private val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    jitter: () -> Double = { java.util.concurrent.ThreadLocalRandom.current().nextDouble() },
) {
    private val retry = DirectRetry(jitter)

    private class PeerEntry(
        val id: String,
        val head: String,
        val boot: ByteArray,
    ) {
        lateinit var session: DirectSession
        val paths = HashMap<Int, DirectChannel>()
        @Volatile var live = true
        var capabilities: MachineCapabilities? = null
        var snapshotUntil = 0L
        var downloadUntil = 0L
        var snapshotSeq = 0L
        var downloadSeq = 0L
        var redialAt: Long? = null
        var readyAt: Long? = null
        var retryDelay: Long? = null
        var selected = false
    }

    private class PendingRequest(
        val peer: PeerEntry,
        val use: Int,
        val response: CompletableDeferred<Envelope>,
    )

    private class InboundUsage {
        var frames = 0
        var bytes = 0
    }

    private val random = SecureRandom()
    private val instance = ByteArray(16).also { random.nextBytes(it) }
    private val boot = ByteArray(32).also { random.nextBytes(it) }
    private val budget = DirectBudget()
    private val stateLock = Any()

    private val engineJob = SupervisorJob()
    private val scope = CoroutineScope(engineJob + dispatcher)
    private val receiveActor = CoroutineScope(engineJob + dispatcher.limitedParallelism(1))

    private var record: DeviceSecurity.Record? = null
    private var self: Wire.Identity? = null
    private var codec: Wire? = null
    private var head = ""
    private var freshUntil = 0L
    private var syncRevision = 0L
    private var lifetime = 0L
    private val routingOwners = HashMap<String, Any>()
    private val peers = HashMap<String, PeerEntry>()
    private val sessions = HashMap<String, PeerEntry>()
    private val routes = HashMap<String, DirectRouting>()
    private val pending = HashMap<String, PendingRequest>()

    private var incoming = 0
    private val inboundPeers = HashMap<String, InboundUsage>()
    private var incomingBytes = 0

    @Volatile private var leg: FrameStream? = null
    private var discoverTimer: Job? = null
    private val preferences = HashMap<String, Boolean>()
    private var frameTokens = 256.0
    private var byteTokens = (4 shl 20).toDouble()
    private var rateAtNs = System.nanoTime()
    private val retryAfter = HashMap<String, Long>()
    private val failReasons = HashMap<String, DirectFailReason>()

    /** Sends one authorized background read on the given path and returns when it
     * ends; the owner of the keys (Remote) provides it. Without it no warm-up
     * sample is taken. */
    @Volatile var probe: (suspend (id: String, path: RouteName) -> Unit)? = null
    private val pumpRunning = java.util.concurrent.atomic.AtomicBoolean(false)
    private val warmShown = HashMap<String, Boolean>()

    private fun notifyChanged() {
        runCatching { changed() }
    }

    fun enabled(id: String): Boolean = synchronized(stateLock) {
        preferences.getOrPut(id) {
            runCatching { preferenceStore.isEnabled(account.server, account.deviceId, id) }.getOrDefault(true)
        }
    }

    fun setEnabled(id: String, enabled: Boolean) {
        // Persistence must succeed before reporting a changed preference.
        preferenceStore.setEnabled(account.server, account.deviceId, id, enabled)
        val toClose: List<DirectSession>
        synchronized(stateLock) {
            preferences[id] = enabled
            retryAfter.remove(id)
            retry.reset(id)
            failReasons.remove(id)
            lifetime++
            routingOwners[id] = Any()
            toClose = sessions.values.filter { it.id == id }.onEach { it.live = false }.map { it.session }
            peers.remove(id)
            routingLocked(id).fail()
        }
        for (s in toClose) s.close()
        notifyChanged()
        if (enabled) scope.launch { discover() }
    }

    fun install(r: DeviceSecurity.Record) {
        val shouldInvalidate = synchronized(stateLock) {
            !r.member ||
                r.pin.account != record?.pin?.account ||
                Wire.hex(DeviceSecurity.stateHash(r.head)) != head
        }
        if (shouldInvalidate) invalidate()
    }

    fun invalidate() {
        val toClose: List<DirectSession>
        val allRoutes: List<DirectRouting>
        val toFailPending: List<PendingRequest>
        synchronized(stateLock) {
            syncRevision++
            lifetime++
            routingOwners.clear()
            retryAfter.clear()
            retry.clear()
            freshUntil = 0L
            toClose = sessions.values.onEach { it.live = false }.map { it.session }
            peers.clear()
            sessions.clear()
            allRoutes = routes.values.toList()
            toFailPending = pending.values.toList()
            pending.clear()
        }
        for (s in toClose) s.close()
        for (r in allRoutes) r.fail()
        for (p in toFailPending) p.response.completeExceptionally(PathChanged())
        notifyChanged()
    }

    suspend fun synced(r: DeviceSecurity.Record, started: Long) {
        val revision = synchronized(stateLock) { ++syncRevision }
        try {
            val h = r.head
            val at = maxOf(clock(), r.floor.coerceAtMost(9_223_372_036_854_775_807uL).toLong())
            if (!Authority.leaseValid(r, at)) throw PathChanged()
            if (!r.member || r.memberID != account.deviceId || clock() >= started + 20_000L) throw PathChanged()
            val member = h.core.member(r.memberID) ?: throw PathChanged()
            val xPrivate = r.peerBoxKey()
            val edPub = Ed25519PrivateKeyParameters(account.deviceSeed, 0).generatePublicKey().encoded
            val xPub = X25519PrivateKeyParameters(xPrivate, 0).generatePublicKey().encoded
            if (!edPub.contentEquals(member.ed) || !xPub.contentEquals(member.x)) throw PathChanged()
            val nextSelf = Wire.Identity(Wire.utf8(member.id), 1L, instance, boot, member.ed, member.x, account.deviceSeed, xPrivate)
            val stateHashBytes = DeviceSecurity.stateHash(h)
            val nextCodec = Wire(Wire.utf8(r.pin.account), r.pin.domain, stateHashBytes, h.core.epoch.toLong())
            val hash = Wire.hex(stateHashBytes)

            val toClose: List<DirectSession> = synchronized(stateLock) {
                if (revision != syncRevision) return
                val closedSessions = if (head != hash) {
                    lifetime++
                    routingOwners.clear()
                    retryAfter.clear()
                    retry.clear()
                    val list = sessions.values.onEach { it.live = false }.map { it.session }
                    peers.clear()
                    sessions.clear()
                    list
                } else {
                    emptyList()
                }
                record = r
                head = hash
                freshUntil = minOf(started + 20_000L, r.leaseExpiration())
                self = nextSelf
                codec = nextCodec
                closedSessions
            }
            for (s in toClose) s.close()
            discover()
        } catch (_: Exception) {
            val shouldInvalidate = synchronized(stateLock) { revision == syncRevision }
            if (shouldInvalidate) invalidate()
        }
    }

    fun attach(stream: FrameStream) {
        val toDiscover: List<DirectSession>
        synchronized(stateLock) {
            leg = stream
            // The main connection is back: the computer can be reached again, so a retry waiting out its delay need not.
            retryAfter.clear()
            if (discoverTimer == null) {
                lateinit var job: Job
                job = scope.launch {
                    while (isActive) {
                        delay(1000)
                        val shouldInvalidate = synchronized(stateLock) {
                            if (discoverTimer !== job) return@launch
                            !currentLocked() && peers.isNotEmpty()
                        }
                        if (shouldInvalidate) invalidate() else discover()
                    }
                }
                discoverTimer = job
            }
            toDiscover = sessions.values.map { it.session }
        }
        for (s in toDiscover) s.discovered()
        scope.launch { discover() }
    }

    fun detach(stream: FrameStream) {
        synchronized(stateLock) {
            if (leg === stream) leg = null
        }
    }

    private fun currentLocked(): Boolean {
        val r = record ?: return false
        return r.member && self != null && codec != null && clock() < freshUntil
    }

    private fun validLocked(p: PeerEntry): Boolean {
        if (!p.live || !currentLocked() || p.head != head) return false
        val isEnabled = preferences.getOrPut(p.id) {
            runCatching { preferenceStore.isEnabled(account.server, account.deviceId, p.id) }.getOrDefault(true)
        }
        return isEnabled
    }

    private fun publicIdentityLocked(id: String): Wire.Identity {
        val member = record!!.head.core.members.firstOrNull { it.id == id && it.kind == 1u } ?: throw PathChanged()
        return Wire.Identity(Wire.utf8(id), 2L, ByteArray(0), ByteArray(0), member.ed, member.x, null, null)
    }

    /** The delay for this round's end, counted once however many of its paths close. Caller holds stateLock. */
    private fun roundDelay(id: String, p: PeerEntry): Long =
        p.retryDelay ?: retry.delay(id, p.readyAt, clock()).also { p.retryDelay = it }

    private suspend fun discover() {
        if (rtcFactory == null) return
        val snapshot = synchronized(stateLock) {
            if (!currentLocked() || leg == null) return
            Triple(head, leg, lifetime) to record!!.head.core.members.toList()
        }
        val (scopeIds, members) = snapshot
        val (snapHead, snapLeg, snapLife) = scopeIds

        for (member in members) {
            val toRetire = synchronized(stateLock) {
                if (!currentLocked() || head != snapHead || leg !== snapLeg || lifetime != snapLife) return
                val prior = peers[member.id]
                if (prior?.redialAt != null && clock() >= prior.redialAt!! && prior.session.active) {
                    peers.remove(member.id)
                    retryAfter.remove(member.id)
                    prior.session
                } else null
            }
            toRetire?.retire()

            val entry = synchronized(stateLock) {
                if (!currentLocked() || head != snapHead || leg !== snapLeg || lifetime != snapLife) return
                val isEnabled = preferences.getOrPut(member.id) {
                    runCatching { preferenceStore.isEnabled(account.server, account.deviceId, member.id) }.getOrDefault(true)
                }
                if (member.kind != 1u || !isEnabled || peers[member.id]?.session?.active == true || clock() < (retryAfter[member.id] ?: 0L)) {
                    null
                } else if (peers.size >= 4) {
                    return
                } else {
                    runCatching {
                        val peerId = publicIdentityLocked(member.id)
                        val conn = ByteArray(16).also { random.nextBytes(it) }
                        createLocked(peerId, conn, clock() + 10_000L)
                    }.getOrNull()
                }
            }
            entry?.first?.let { p ->
                for (s in entry.second) s.close()
                p.session.start()
            }
        }
    }

    /** Caller must hold stateLock; returns (createdPeer, expiredSessionsToCloseOutsideLock). */
    private fun createLocked(peer: Wire.Identity, connection: ByteArray, deadline: Long): Pair<PeerEntry?, List<DirectSession>> {
        val factory = rtcFactory ?: return null to emptyList()
        val id = Wire.text(peer.id)
        val now = clock()
        val expired = ArrayList<DirectSession>()
        val iter = sessions.entries.iterator()
        while (iter.hasNext()) {
            val e = iter.next()
            if (now > e.value.session.retainUntil) {
                e.value.live = false
                expired.add(e.value.session)
                iter.remove()
            }
        }
        val isEnabled = preferences.getOrPut(id) {
            runCatching { preferenceStore.isEnabled(account.server, account.deviceId, id) }.getOrDefault(true)
        }
        if (!currentLocked() || !isEnabled || sessions.size >= 128 ||
            sessions.values.count { it.id == id } >= 32 ||
            (id !in peers && peers.size >= 4)
        ) {
            return null to expired
        }
        val connHex = Wire.hex(connection)
        val p = PeerEntry(id, head, peer.boot.copyOf())
        val choose = {
            val toRetire: List<DirectSession> = synchronized(stateLock) {
                if (!validLocked(p) || !p.session.active || sessions[connHex] !== p) return@synchronized emptyList()
                p.selected = true
                selectLocked(p)
                sessions.values.filter { it !== p && it.id == id }.map { it.session }
            }
            for (old in toRetire) old.retire()
        }
        p.session = DirectSession(
            DirectSessionOptions(
                self = self!!,
                peer = peer,
                codec = codec!!,
                connection = connection,
                deadline = minOf(deadline, now + 10_000L),
                budget = budget,
                stun = stunUrls,
                factory = factory,
                valid = { synchronized(stateLock) { validLocked(p) } },
                authorize = {
                    // The 20 s freshness window, renewed by the 12 s sync loop, already
                    // bounds how stale the head can be; a signal step inside it needs no
                    // relay round trip of its own (the web checks the stored record only).
                    // Outside the window, sync first, as before.
                    val ok = authorizeUnlessFresh({ synchronized(stateLock) { validLocked(p) } }, authorize)
                    if (!ok) throw PathChanged()
                },
                send = { out -> send(p, out) },
                chosen = choose,
                beforeOffer = choose,
                ready = { use, path ->
                    val accepted = synchronized(stateLock) {
                        if (!validLocked(p) || !p.session.active || peers[id] !== p) {
                            false
                        } else {
                            p.paths[use] = path
                            if (p.readyAt == null) p.readyAt = clock()
                            failReasons.remove(id)
                            if (use == 1) routingLocked(id).arm()
                            true
                        }
                    }
                    if (!accepted) path.close() else notifyChanged()
                },
                closeReason = { reason ->
                    synchronized(stateLock) {
                        if (preferences[id] != false && peers[id] === p) {
                            failReasons[id] = reason
                        }
                    }
                    notifyChanged()
                },
                closed = { use ->
                    var shouldNotify = false
                    val toFail: List<PendingRequest>
                    synchronized(stateLock) {
                        toFail = pending.entries
                            .filter { it.value.peer === p && (use == null || it.value.use == use) }
                            .map { it.value }
                        for (req in toFail) {
                            pending.entries.removeIf { it.value === req }
                        }
                        if (peers[id] === p) {
                            shouldNotify = true
                            if (use != null && p.session.active && p.session.established) {
                                if (p.redialAt == null) p.redialAt = clock() + roundDelay(id, p)
                                routingLocked(id).fail()
                            }
                            if (p.paths.values.none { it.ready }) {
                                routingLocked(id).fail()
                                if (peers[id] === p) peers.remove(id)
                                retryAfter[id] = clock() + roundDelay(id, p)
                                p.live = false
                            }
                        }
                    }
                    for (req in toFail) req.response.completeExceptionally(PathChanged())
                    if (!p.live) scope.launch { p.session.retire() }
                    if (shouldNotify) notifyChanged()
                },
                frame = { raw, use -> frame(p, raw, use) },
                capability = { opened -> capability(p, opened) },
                clock = clock,
                monotonic = monotonic,
                dispatcher = dispatcher,
            ),
        )
        sessions[connHex] = p
        val prior = peers[id]
        if (prior == null || !prior.selected || !prior.session.active) {
            selectLocked(p)
        }
        return p to expired
    }

    private fun selectLocked(peer: PeerEntry) {
        if (peers[peer.id] === peer) return
        if (peer.id in peers) routingLocked(peer.id).fail()
        routingOwners[peer.id] = Any()
        peers[peer.id] = peer
    }

    inner class RouteTracker(
        private val id: String,
        private val hint: RouteHint,
        private val peer: Any?,
        private val path: DirectChannel?,
        private val life: Long,
        private val owner: Any,
    ) {
        private fun isCurrent(): Boolean = synchronized(stateLock) {
            val use = if (hint is RouteHint.Read) 1 else 2
            life == lifetime && routingOwners[id] === owner && peers[id] === peer && (peer as? PeerEntry)?.paths?.get(use) === path
        }

        fun observe(direct: Boolean, bytes: Int, milliseconds: Double) {
            val route = synchronized(stateLock) {
                if (!isCurrent()) return
                routingLocked(id)
            }
            route.observe(
                hint,
                if (direct) RouteName.DIRECT else RouteName.RELAY,
                bytes,
                milliseconds,
                path?.queuedBytes ?: 0L,
            )
            notifyChanged()
        }

        fun failed() {
            val route = synchronized(stateLock) {
                if (!isCurrent()) return
                routingLocked(id)
            }
            route.fail(hint)
            notifyChanged()
        }
    }

    fun track(id: String, hint: RouteHint): RouteTracker = synchronized(stateLock) {
        val peer = peers[id]
        val path = peer?.paths?.get(if (hint is RouteHint.Read) 1 else 2)
        val owner = routingOwners.getOrPut(id) { Any() }
        RouteTracker(id, hint, peer, path, lifetime, owner)
    }

    private fun capability(p: PeerEntry, opened: Wire.Opened) {
        synchronized(stateLock) {
            if (!validLocked(p)) return
            if (opened.type == 14L) {
                if (java.lang.Long.compareUnsigned(opened.ctx.seq, p.snapshotSeq) <= 0) return
                val snapshot = runCatching { CapabilitySnapshot.parseFrom(opened.payload) }.getOrNull() ?: return
                if (!snapshot.hasCapabilities()) return
                p.snapshotSeq = opened.ctx.seq
                p.capabilities = snapshot.capabilities
                p.snapshotUntil = snapshot.expiresAtMs
            } else {
                if (java.lang.Long.compareUnsigned(opened.ctx.seq, p.downloadSeq) <= 0) return
                p.downloadSeq = opened.ctx.seq
                val fields = Schema.fields(opened.payload, "Capabilities")
                p.downloadUntil = if (Wire.getUint(fields, 5) == 1L) Wire.getUint(fields, 4) else 0L
            }
        }
        notifyChanged()
    }

    private fun send(p: PeerEntry, out: DirectedOutput): Boolean {
        val targetLeg = synchronized(stateLock) {
            if (!validLocked(p) || !out.valid()) return false
            val currentLeg = leg ?: return false
            val nowNs = System.nanoTime()
            val elapsedSec = maxOf(0.0, (nowNs - rateAtNs) / 1_000_000_000.0)
            rateAtNs = nowNs
            frameTokens = minOf(256.0, frameTokens + elapsedSec * 256.0)
            byteTokens = minOf((4 shl 20).toDouble(), byteTokens + elapsedSec * (4 shl 20))
            if (frameTokens < 1.0 || byteTokens < out.raw.size) return false
            frameTokens -= 1.0
            byteTokens -= out.raw.size
            currentLeg
        }
        return try {
            val fields = Wire.parse(out.raw, 5)
            val directed = DirectedPeerFrame.newBuilder()
                .setToDevice(ByteString.copyFrom(Wire.getBytes(fields, 2)))
                .setFrameClassValue(Wire.getUint(fields, 3).toInt())
                .setFrameId(ByteString.copyFrom(Wire.getBytes(fields, 4)))
                .setSealed(ByteString.copyFrom(Wire.getBytes(fields, 5)))
                .build()
            val frame = PeerFrame.newBuilder().setDirectedPeerFrame(directed).build().toByteArray()
            targetLeg.send(frame)
        } catch (_: Exception) {
            false
        }
    }

    fun receive(raw: ByteArray, sourceLeg: FrameStream) {
        val copy: ByteArray
        val life: Long
        val sourceId: String
        synchronized(stateLock) {
            if (sourceLeg !== leg || !currentLocked() || raw.size > 65536 || incoming >= 64 || incomingBytes + raw.size > (4 shl 20)) return
            val selfId = self!!.id
            val r = record!!
            try {
                val f = Wire.parse(raw, 5)
                val cls = Wire.getUint(f, 3)
                val from = Wire.getBytes(f, 1)
                val to = Wire.getBytes(f, 2)
                val id = Wire.getBytes(f, 4)
                sourceId = Wire.text(from)
                if (f.size != 5 || from.size != 32 || !to.contentEquals(selfId) || id.size != 16 ||
                    cls < 1L || cls > 2L || (cls == 2L && raw.size > 16384) ||
                    r.head.core.members.none { it.id == sourceId && it.kind == 1u }
                ) return
            } catch (_: Exception) {
                return
            }
            val usage = inboundPeers.getOrPut(sourceId) { InboundUsage() }
            if (usage.frames >= 8 || usage.bytes + raw.size > (1 shl 20)) return
            usage.frames++
            usage.bytes += raw.size
            incoming++
            incomingBytes += raw.size
            copy = raw.copyOf()
            life = lifetime
        }

        receiveActor.launch {
            try {
                val snap = synchronized(stateLock) {
                    if (!currentLocked() || sourceLeg !== leg || life != lifetime) return@launch
                    val fields = Wire.parse(copy, 5)
                    val id = Wire.text(Wire.getBytes(fields, 1))
                    val isEnabled = preferences.getOrPut(id) {
                        runCatching { preferenceStore.isEnabled(account.server, account.deviceId, id) }.getOrDefault(true)
                    }
                    if (!Wire.getBytes(fields, 2).contentEquals(self!!.id) || !isEnabled) return@launch
                    val peer = runCatching { publicIdentityLocked(id) }.getOrNull() ?: return@launch
                    arrayOf(id, head, codec!!, self!!, peer)
                }
                val id = snap[0] as String
                val snapHead = snap[1] as String
                val snapCodec = snap[2] as Wire
                val snapSelf = snap[3] as Wire.Identity
                val peer = snap[4] as Wire.Identity

                val opened = runCatching { snapCodec.open(copy, peer, snapSelf, clock()) }.getOrNull()
                if (opened != null && opened.ctx.cls == 1L && opened.type == 1L) {
                    val connHex = Wire.hex(opened.ctx.connection)
                    var expiredToClose: List<DirectSession> = emptyList()
                    var staleBootToClose: List<DirectSession> = emptyList()
                    val target = synchronized(stateLock) {
                        if (!currentLocked() || snapHead != head || sourceLeg !== leg || life != lifetime) return@launch
                        sessions[connHex] ?: run {
                            peer.instance = opened.ctx.senderInstance.copyOf()
                            peer.boot = opened.ctx.senderBoot.copyOf()
                            staleBootToClose = sessions.values
                                .filter { it.id == id && it.session.peer.boot.isNotEmpty() && !it.session.peer.boot.contentEquals(peer.boot) }
                                .onEach { it.live = false }
                                .map { it.session }
                            // A new process of the computer starts afresh: no old failure slows it.
                            if (staleBootToClose.isNotEmpty()) {
                                retryAfter.remove(id)
                                retry.reset(id)
                            }
                            val created = createLocked(peer, opened.ctx.connection, opened.ctx.deadline)
                            expiredToClose = created.second
                            created.first
                        }
                    }
                    for (s in staleBootToClose) s.close()
                    for (s in expiredToClose) s.close()
                    if (target != null && synchronized(stateLock) { validLocked(target) && snapHead == head && sourceLeg === leg && life == lifetime }) {
                        target.session.receive(copy)
                    }
                } else {
                    val candidates = synchronized(stateLock) {
                        if (!currentLocked() || snapHead != head || sourceLeg !== leg || life != lifetime) return@launch
                        sessions.values.filter { it.id == id && it.head == snapHead }
                    }
                    var matched: PeerEntry? = null
                    for (p in candidates) {
                        val o = runCatching { snapCodec.open(copy, p.session.peer, snapSelf, clock()) }.getOrNull() ?: continue
                        if (o.ctx.connection.contentEquals(p.session.options.connection)) {
                            matched = p
                            break
                        }
                    }
                    if (matched != null && synchronized(stateLock) { validLocked(matched) && snapHead == head && sourceLeg === leg && life == lifetime }) {
                        matched.session.receive(copy)
                    }
                }
            } catch (_: Exception) {
                // Optional peer frames cannot fail the engine or main WS.
            } finally {
                synchronized(stateLock) {
                    incoming--
                    incomingBytes -= copy.size
                    val usage = inboundPeers[sourceId]
                    if (usage != null) {
                        usage.frames--
                        usage.bytes -= copy.size
                        if (usage.frames <= 0) inboundPeers.remove(sourceId)
                    }
                }
            }
        }
    }

    fun result(raw: ByteArray, sourceLeg: FrameStream) {
        val snap = synchronized(stateLock) {
            if (sourceLeg !== leg) return
            lifetime to sessions.values.map { it.session }
        }
        val (life, list) = snap
        try {
            val res = PeerFrameResult.parseFrom(raw)
            val id = res.frameId.toByteArray()
            val status = res.statusValue
            val retry = res.retryAfterMs
            for (s in list) {
                s.delivery(id, status, retry) { synchronized(stateLock) { sourceLeg === leg && life == lifetime } }
            }
        } catch (_: Exception) {
            // An unauthenticated receipt cannot authorize a path.
        }
    }

    private fun frame(peer: PeerEntry, raw: ByteArray, use: Int) {
        if (!synchronized(stateLock) { validLocked(peer) }) return
        try {
            val env = Envelope.parseFrom(raw)
            val header = DeviceSecurity.parseHeader(env.securityHeader.toByteArray())
            val req = synchronized(stateLock) { pending[header.request] }
            if (req != null && req.peer === peer && req.use == use && header.machine == peer.id) {
                req.response.complete(env)
            }
        } catch (_: Exception) {
            // Remote authenticates every routed result.
        }
    }

    private fun routingLocked(id: String): DirectRouting = routes.getOrPut(id) { DirectRouting(monotonic, ::kick) }

    /** A screen that shows the numbers (the 「连接」 group, the connection check)
     * was opened: when direct is connected and the live window is too thin to
     * decide, one warm-up, with the same bounds and merged with a running one. */
    fun refresh(id: String) {
        val route = synchronized(stateLock) {
            if (readyLocked(id, RouteHint.Read)) routingLocked(id) else null
        } ?: return
        if (route.sparse()) route.arm()
    }

    /** Schedules the pump; never blocks, never takes the engine lock. */
    private fun kick() {
        if (!pumpRunning.compareAndSet(false, true)) return
        scope.launch { pump() }
    }

    /** Drives the warm-up of every computer: one step every 250 ms while any is
     * open. The numbers (alternation, in-flight, per-path count, rate) are the
     * routing's; this only asks it, and only while the read path is ready. */
    private suspend fun pump() {
        try {
            while (kotlin.coroutines.coroutineContext.isActive) {
                var more = false
                val routesNow = synchronized(stateLock) { routes.entries.map { it.key to it.value } }
                for ((id, route) in routesNow) {
                    val warming = route.warming()
                    if (route.pending()) more = true
                    val ready = synchronized(stateLock) { readyLocked(id, RouteHint.Read) }
                    if (warming && ready) {
                        val path = route.nextProbe()
                        val fn = probe
                        if (path != null) {
                            scope.launch {
                                try {
                                    fn?.invoke(id, path)
                                } catch (e: kotlinx.coroutines.CancellationException) {
                                    throw e
                                } catch (_: Exception) {
                                } finally {
                                    route.probeDone(path)
                                    notifyChanged()
                                }
                            }
                        }
                    }
                    val shown = route.warming()
                    val changedShown = synchronized(stateLock) {
                        (warmShown[id] != shown).also { warmShown[id] = shown }
                    }
                    if (changedShown) notifyChanged()
                }
                if (!more) break
                delay(ReadRule.WARMUP_MIN_GAP_MS)
            }
        } finally {
            pumpRunning.set(false)
            // A trigger that landed between the last check and the flag reset.
            val again = synchronized(stateLock) { routes.values.toList() }.any { it.pending() }
            if (again && scope.isActive) kick()
        }
    }

    fun routing(id: String): DirectRouting = synchronized(stateLock) { routingLocked(id) }

    fun capabilities(id: String): MachineCapabilities? = synchronized(stateLock) {
        val p = peers[id] ?: return@synchronized null
        if (validLocked(p) && clock() < p.snapshotUntil) p.capabilities else null
    }

    fun ready(id: String, hint: RouteHint): Boolean = synchronized(stateLock) {
        readyLocked(id, hint)
    }

    private fun readyLocked(id: String, hint: RouteHint): Boolean {
        val p = peers[id] ?: return false
        val use = if (hint is RouteHint.Read) 1 else 2
        if (!validLocked(p) || !p.session.active || p.paths[use]?.ready != true) return false
        if (hint is RouteHint.Bulk && hint.upload != null) {
            val caps = if (clock() < p.snapshotUntil) p.capabilities else null
            return caps != null && hint.bytes > 0 && hint.bytes <= caps.maxChunkBytes &&
                (if (hint.upload == UploadKind.FILE) caps.fileUpload else caps.imageUpload)
        }
        return hint !is RouteHint.Bulk || clock() < p.downloadUntil
    }

    /** `probe` takes a warm-up sample on the direct path whatever the routing
     * prefers; the routing already bounded it. */
    fun attempt(id: String, hint: RouteHint, handoff: () -> Unit, probe: Boolean = false, attemptId: String = newRequestId()): PathAttempt? {
        val chosen = synchronized(stateLock) {
            if (probe) {
                if (hint !is RouteHint.Read || !readyLocked(id, hint)) return@synchronized null
            } else {
                if (routingLocked(id).choose(hint, readyLocked(id, hint)) != RouteName.DIRECT) return@synchronized null
            }
            val p = peers[id] ?: return@synchronized null
            val use = if (hint is RouteHint.Read) 1 else 2
            val path = p.paths[use] ?: return@synchronized null
            Triple(p, use, path)
        } ?: return null

        val (peer, use, path) = chosen
        val resolvedAttemptId = attemptId
        val tracking = track(id, hint)
        fun isCurrent(): Boolean = synchronized(stateLock) {
            peers[id] === peer && peer.paths[use] === path && readyLocked(id, hint)
        }

        return object : PathAttempt {
            override val direct = true
            override val attemptId = resolvedAttemptId
            override suspend fun prepare(prior: Boolean) {
                if (!isCurrent()) throw PathChanged()
            }

            override suspend fun send(machineId: String, command: Envelope): ForwardResponse {
                val header = DeviceSecurity.parseHeader(command.securityHeader.toByteArray())
                val requestId = header.request
                val deferred = CompletableDeferred<Envelope>()
                val entry = PendingRequest(peer, use, deferred)
                synchronized(stateLock) {
                    if (!isCurrent() || pending.size >= 128 || requestId in pending) throw PathChanged()
                    pending[requestId] = entry
                }
                val deadlineMs = header.deadline.coerceAtMost(9_223_372_036_854_775_807uL).toLong()
                val timeoutMs = maxOf(1L, minOf(3000L, deadlineMs - clock()))
                try {
                    val env = withTimeout(timeoutMs) {
                        path.write(command.toByteArray(), handoff)
                        deferred.await()
                    }
                    return ForwardResponse.newBuilder().setResult(env).setAttemptId(attemptId).build()
                } catch (e: Exception) {
                    tracking.failed()
                    if (e is kotlinx.coroutines.CancellationException && e !is kotlinx.coroutines.TimeoutCancellationException) throw e
                    throw PathChanged()
                } finally {
                    synchronized(stateLock) {
                        if (pending[requestId] === entry) pending.remove(requestId)
                    }
                }
            }

            override fun refusal(error: com.connectrpc.ConnectException): PathRefusal? = null
            override suspend fun refreshAuth(prior: Boolean) {
                authorize()
            }
        }
    }

    fun filePath(id: String): RouteName = synchronized(stateLock) {
        val p = peers[id]
        if (p != null && validLocked(p) && p.paths[2]?.ready == true) routingLocked(id).filePath else RouteName.RELAY
    }

    fun directStatus(id: String): DirectStatus = synchronized(stateLock) {
        val enabled = preferences.getOrPut(id) {
            runCatching { preferenceStore.isEnabled(account.server, account.deviceId, id) }.getOrDefault(true)
        }
        val supported = rtcFactory != null
        val m = routingLocked(id).measured()
        if (!supported) {
            return@synchronized DirectStatus(supported = false, enabled = enabled, stage = DirectStage.UNCONNECTED)
        }
        if (!enabled) {
            return@synchronized DirectStatus(supported = true, enabled = false, stage = DirectStage.OFF)
        }
        val p = peers[id]
        val live = p != null && validLocked(p) && p.session.active
        val connected = live && p!!.paths.values.any { it.ready }
        val readReady = readyLocked(id, RouteHint.Read)
        val readDirect = connected && readReady && m.readDirect
        val fileDirect = connected && p!!.paths[2]?.ready == true && m.fileDirect
        val warming = readReady && m.warming
        val stage = when {
            connected && (readDirect || fileDirect) -> DirectStage.DIRECT_ACTIVE
            connected && warming -> DirectStage.MEASURING
            connected -> DirectStage.CONNECTED_RELAY
            live -> DirectStage.TRYING
            else -> DirectStage.UNCONNECTED
        }
        DirectStatus(
            supported = true,
            enabled = true,
            stage = stage,
            readDirect = readDirect,
            fileDirect = fileDirect,
            relayNotSlower = m.relayNotSlower,
            reason = if (stage == DirectStage.UNCONNECTED) failReasons[id] else null,
            bulkDirectMibPerSec = m.bulkDirectMibPerSec,
            bulkRelayMibPerSec = m.bulkRelayMibPerSec,
            readDirectMs = m.readDirectMs,
            readRelayMs = m.readRelayMs,
        )
    }

    fun close() {
        synchronized(stateLock) {
            discoverTimer?.cancel()
            discoverTimer = null
            leg = null
        }
        invalidate()
        engineJob.cancel()
    }

    companion object {
        fun defaultStunUrls(server: String): List<String> = runCatching {
            val host = URI(server).host ?: return emptyList()
            val formatted = if (":" in host && !host.startsWith("[")) "[$host]" else host
            listOf("stun:$formatted:3478")
        }.getOrDefault(emptyList())
    }
}

/**
 * One signal step's authorization: inside the freshness window nothing more is
 * needed; outside it, sync first. Returns whether the path is valid afterwards.
 * A relay round trip per signal step, as before, spent seconds of the 10 s
 * round on a phone.
 */
internal suspend fun authorizeUnlessFresh(valid: () -> Boolean, sync: suspend () -> Unit): Boolean {
    if (!valid()) sync()
    return valid()
}
