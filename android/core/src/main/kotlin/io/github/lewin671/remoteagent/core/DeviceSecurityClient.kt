package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** An authenticated event separated from its missing key by a verified recovery. */
class RecoveryHistoryUnavailable(val header: DeviceSecurity.Header) : Exception("恢复前的记录不可读")

/**
 * Foreground member session. Locked notification code receives a Notify child key only, never this
 * storage, signing key or complete content secret. Envelopes are sealed and opened locally; only
 * signed states come from the relay.
 */
class DeviceSecurityClient(
    private val store: DeviceSecurity.Storage,
    private val memberID: String,
    private val ed: SigningKey,
    private val expectedAccount: String? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    private val fetchStates: suspend (afterEpoch: ULong) -> GetSecurityStatesResponse,
) {
    private val syncMutex = Mutex()
    private val historyLock = Any()
    private var lastFloorWrite = 0uL
    @Volatile private var closed = false
    @Volatile
    var recoveryPending: Boolean = false
        private set

    @Volatile
    var recoveryReadyMs: Long = 0L
        private set

    /** A state is issued more than five minutes ahead of this phone's clock. */
    @Volatile
    var clockSkew: Boolean = false
        private set

    private fun read(): DeviceSecurity.Record {
        if (closed) throw DeviceSecurity.Failure("storage")
        return store.readSecurity().also {
            if (
                it.memberID != memberID ||
                    deviceId(ed.publicKey) != memberID ||
                    expectedAccount != null && it.pin.account != expectedAccount
            )
                throw DeviceSecurity.Failure("untrusted")
        }
    }

    fun identity(): Pair<DeviceSecurity.Pin, DeviceSecurity.State> {
        val r = read()
        return r.pin to r.head
    }

    fun record(): DeviceSecurity.Record = read()

    fun head(): DeviceSecurity.State = read().head

    fun stateAt(epoch: ULong): DeviceSecurity.State? = read().stateAt(epoch)

    fun chain(): List<DeviceSecurity.State> = read().chain

    fun chainProofs(): Map<ULong, ByteArray> = read().proofs

    /** The content key of an epoch, available from the chain while the device holds it. */
    fun contentKey(epoch: ULong): ByteArray? {
        val r = read()
        val key = r.contentKey(epoch) ?: return null
        if (!r.cachedKeys().containsKey(epoch) && epoch <= r.head.core.contentEpoch) {
            runCatching { store.cacheSecurityKey(DeviceSecurity.stateHash(r.head), epoch, key) }
        }
        return key
    }

    fun close() {
        closed = true
        synchronized(historyLock) {}
    }

    private fun now(r: DeviceSecurity.Record): ULong = maxOf(clock().toULong(), r.floor)

    /** Pulls every state after the local head. The relay cannot make the head go backwards. */
    suspend fun sync(): DeviceSecurity.Record = syncMutex.withLock {
        repeat(DeviceSecurity.MAX_STATES) {
            // Ordinary phones receive no control envelopes. Persist their local
            // observed time even for an unchanged head or a failed state pull.
            val before = read()
            val r = store.touchSecurity(DeviceSecurity.stateHash(before.head), now(before))
            val res = fetchStates(r.head.core.epoch)
            recoveryPending = res.recoveryPending
            recoveryReadyMs = res.recoveryReadyMs
            if (res.headEpoch.toULong() < r.head.core.epoch) throw DeviceSecurity.Failure("stale")
            if (res.statesCount == 0) {
                if (res.headEpoch.toULong() != r.head.core.epoch)
                    throw DeviceSecurity.Failure("stale")
                val cert =
                    res.lease.toByteArray().takeIf { it.isNotEmpty() }?.let { HeadLease.parse(it) }
                return@withLock if (cert != null)
                    store.installSecurity(
                        DeviceSecurity.stateHash(r.head),
                        emptyList(),
                        emptyList(),
                        cert,
                    )
                else r
            }
            if (res.statesCount > 8) throw DeviceSecurity.Failure("untrusted")
            val states = res.statesList.map { DeviceSecurity.parseState(it.state.toByteArray()) }
            val at = clock().toULong()
            for (state in states) {
                if (DeviceSecurity.tooFarAhead(state, at)) {
                    clockSkew = true
                    throw DeviceSecurity.Failure("clock")
                }
            }
            clockSkew = false
            val proofs =
                res.statesList.map {
                    if (it.recovery.isEmpty) null else it.recovery.toByteArray()
                }
            currentCoroutineContext().ensureActive()
            store.installSecurity(DeviceSecurity.stateHash(r.head), states, proofs)
            currentCoroutineContext().ensureActive()
        }
        throw DeviceSecurity.Failure("capacity")
    }

    /** Publishes states that this phone signed (genesis, renewal, membership, veto). */
    fun install(
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?> = emptyList(),
        lease: HeadLease? = null,
    ) {
        val at = clock().toULong()
        for (state in states) {
            if (DeviceSecurity.tooFarAhead(state, at)) {
                clockSkew = true
                throw DeviceSecurity.Failure("clock")
            }
        }
        val r = read()
        store.installSecurity(DeviceSecurity.stateHash(r.head), states, proofs, lease)
    }

    /** Seals locally with a random nonce; no storage write or authorization round trip. */
    fun seal(
        purpose: DeviceSecurity.Purpose,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: ULong = 0uL,
        deadline: ULong = 0uL,
        plain: ByteArray,
        seed: ByteArray? = null,
    ): Envelope {
        val old = read()
        if (!old.member) throw DeviceSecurity.Failure("untrusted")
        // The old content key must stop producing every kind of new ciphertext.
        // Historical reads deliberately do not use this publication gate.
        if (!Authority.leaseValid(old, clock())) throw DeviceSecurity.Failure("lease")
        val r = old
        val h =
            DeviceSecurity.Header(
                r.pin.account,
                r.pin.domain,
                r.head.core.epoch,
                DeviceSecurity.stateHash(r.head),
                r.head.core.contentEpoch,
                memberID,
                purpose.kind,
                purpose,
                machine,
                session,
                request,
                seq,
                deadline,
                0u,
            )
        val key =
            r.contentKey(r.head.core.contentEpoch) ?: throw DeviceSecurity.Failure("unavailable")
        val formed = DeviceSecurity.form(key, seed, h, plain, ed)
        return DeviceSecurity.envelope(DeviceSecurity.Wire(formed))
    }

    private fun openKey(
        r: DeviceSecurity.Record,
        s: DeviceSecurity.State,
        h: DeviceSecurity.Header,
        w: DeviceSecurity.Wire,
        live: Boolean,
    ): ByteArray {
        r.contentKey(h.contentEpoch)?.let {
            return it
        }
        // Missing keys cannot turn a bad signature, state or route into a skip.
        DeviceSecurity.verifyOrigin(s, r.head, live, now(r), w)
        val boundary =
            r.proofs.keys.any { epoch ->
                val state = r.stateAt(epoch)
                epoch > h.epoch &&
                    state != null &&
                    state.core.contentEpoch > h.contentEpoch &&
                    state.prevSecret.isEmpty()
            }
        if (h.purpose == DeviceSecurity.Purpose.EVENT && boundary)
            throw RecoveryHistoryUnavailable(h)
        throw DeviceSecurity.Failure("unavailable")
    }

    suspend fun openCurrent(
        env: Envelope,
        seed: ByteArray? = null,
    ): Pair<DeviceSecurity.Header, ByteArray> {
        val w = DeviceSecurity.fromEnvelope(env)
        var r = read()
        var h = DeviceSecurity.parseHeader(w.formed.header)
        if (h.epoch > r.head.core.epoch) r = sync()
        if (h.purpose.control && !Authority.leaseValid(r, clock()))
            throw DeviceSecurity.Failure("lease")
        val s =
            r.stateAt(h.epoch)
                ?: if (h.epoch == r.head.core.epoch) r.head
                else throw DeviceSecurity.Failure("stale")
        val key = openKey(r, s, h, w, true)
        val at = now(r)
        if (DeviceSecurity.tooFarAhead(s, at)) {
            clockSkew = true
            throw DeviceSecurity.Failure("clock")
        }
        val (opened, plain) = DeviceSecurity.open(s, r.head, true, at, w, key, seed)
        if (opened.purpose.control) touchFloor(r, at)
        return opened to unpackSecurity(opened, plain)
    }

    /** Read-only historical provenance; never a live action source. */
    suspend fun openHistory(
        env: Envelope,
        seed: ByteArray? = null,
    ): Pair<DeviceSecurity.Header, ByteArray> {
        val w = DeviceSecurity.fromEnvelope(env)
        var h = DeviceSecurity.parseHeader(w.formed.header)
        if (h.purpose.control) throw DeviceSecurity.Failure()
        var r = read()
        if (h.epoch > r.head.core.epoch) r = sync()
        val s = r.stateAt(h.epoch) ?: throw DeviceSecurity.Failure("stale")
        val key = openKey(r, s, h, w, false)
        val (opened, plain) = DeviceSecurity.open(s, s, false, 0uL, w, key, seed)
        return opened to unpackSecurity(opened, plain)
    }

    private fun touchFloor(r: DeviceSecurity.Record, at: ULong) {
        if (at <= r.floor) return
        if (at - lastFloorWrite < 1000uL) return
        lastFloorWrite = at
        runCatching { store.touchSecurity(DeviceSecurity.stateHash(r.head), at) }
    }

    suspend fun <T : MessageLite> openMessage(
        env: Envelope,
        history: Boolean,
        seed: ByteArray? = null,
        parse: (ByteArray) -> T,
    ): Pair<DeviceSecurity.Header, T> {
        val (h, p) = if (history) openHistory(env, seed) else openCurrent(env, seed)
        val m =
            try {
                parse(p)
            } catch (_: Exception) {
                throw DeviceSecurity.Failure()
            }
        validateSecurityBody(h, m)
        return h to m
    }
}

internal fun unpackSecurity(h: DeviceSecurity.Header, p: ByteArray): ByteArray {
    val limit = if (h.purpose.mutable) DeviceSecurity.MAX_MUTABLE else DeviceSecurity.MAX_CIPHER
    return when (h.compression) {
        0u -> {
            if (p.size > limit) throw DeviceSecurity.Failure("capacity")
            p
        }
        1u -> unpack(p, limit)
        else -> throw DeviceSecurity.Failure()
    }
}

internal fun validateSecurityBody(h: DeviceSecurity.Header, m: MessageLite) {
    val ok =
        when (h.purpose) {
            DeviceSecurity.Purpose.EVENT ->
                m is Event &&
                    m.sessionId == h.session &&
                    m.seq.toULong() == h.seq &&
                    m.kindCase != Event.KindCase.KIND_NOT_SET
            DeviceSecurity.Purpose.SESSION_META -> m is Session && m.id == h.session
            DeviceSecurity.Purpose.MACHINE_META -> m is MachineMeta
            DeviceSecurity.Purpose.DEVICE_META -> m is DeviceMeta
            DeviceSecurity.Purpose.PREFS -> m is ProjectPrefs && m.schema == 1
            DeviceSecurity.Purpose.COMMAND ->
                m is Command && m.kindCase != Command.KindCase.KIND_NOT_SET
            DeviceSecurity.Purpose.RESULT -> m is CommandResult
            DeviceSecurity.Purpose.IMAGE -> m is ImagePayload
            DeviceSecurity.Purpose.NOTIFY ->
                m is NotifyContent &&
                    m.machineId == h.machine &&
                    m.sessionId == h.session &&
                    (m.kind == NotifyKind.NOTIFY_KIND_ATTENTION ||
                        m.kind == NotifyKind.NOTIFY_KIND_TURN_ENDED)
            DeviceSecurity.Purpose.SESSION_KEY -> false
            DeviceSecurity.Purpose.TERMINAL_INPUT ->
                m is TerminalFrame && m.kindCase == TerminalFrame.KindCase.INPUT
            DeviceSecurity.Purpose.TERMINAL_RESIZE ->
                m is TerminalFrame && m.kindCase == TerminalFrame.KindCase.RESIZE
            DeviceSecurity.Purpose.TERMINAL_HISTORY ->
                m is TerminalFrame && m.kindCase == TerminalFrame.KindCase.HISTORY_REQUEST
            DeviceSecurity.Purpose.TERMINAL_OUTPUT ->
                m is TerminalFrame &&
                    m.kindCase in
                        setOf(
                            TerminalFrame.KindCase.SNAPSHOT,
                            TerminalFrame.KindCase.OUTPUT,
                            TerminalFrame.KindCase.HISTORY_PAGE,
                            TerminalFrame.KindCase.CLOSED,
                        )
        }
    if (!ok) throw DeviceSecurity.Failure()
}
