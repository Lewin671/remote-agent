package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.ownedNetworkOperation
import io.github.lewin671.remoteagent.proto.GetSecurityStatesResponse
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex

/**
 * Authority/chain security manager backed by [DeviceSecurity.Storage], Go `SecCall`
 * verification, and [io.github.lewin671.remoteagent.core.transport.NetworkRound] sync.
 *
 * Envelope sealing/opening, origin verification, and body validation all run inside
 * `clientcore/security.Engine` rather than in Kotlin.
 */
class AuthoritySecurity(
    private val store: DeviceSecurity.Storage,
    private val memberID: String,
    private val expectedAccount: String? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    private val onSync: suspend (DeviceSecurity.Record, Long) -> Unit = { _, _ -> },
    private val onSyncFailed: () -> Unit = {},
    private val onInstall: (DeviceSecurity.Record) -> Unit = {},
    private val fetchStates: suspend (afterEpoch: ULong) -> GetSecurityStatesResponse,
) {
    private val syncMutex = Mutex()
    private val syncRound = io.github.lewin671.remoteagent.core.transport.NetworkRound(syncMutex)
    fun networkChanged() { syncRound.changed() }

    private val historyLock = Any()
    private var keyRecord: DeviceSecurity.Record? = null
    private val contentKeys = HashMap<ULong, ByteArray>()
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

    private fun read(): DeviceSecurity.Record = synchronized(historyLock) {
        if (closed) throw DeviceSecurity.Failure("storage")
        try {
            store.readSecurity().also {
                if (expectedAccount != null && it.pin.account != expectedAccount) {
                    throw DeviceSecurity.Failure("untrusted")
                }
            }
        } catch (f: DeviceSecurity.Failure) {
            throw f
        } catch (t: Throwable) {
            throw DeviceSecurity.Failure("storage").apply { initCause(t) }
        }
    }

    private fun contentKey(r: DeviceSecurity.Record, epoch: ULong): ByteArray? =
        synchronized(historyLock) {
            if (keyRecord != r) {
                contentKeys.clear()
                keyRecord = r
            }
            contentKeys[epoch]?.let { return@synchronized it }
            r.contentKey(epoch)?.let {
                contentKeys[epoch] = it
                return@synchronized it
            }
            val key = r.secret()
            val cur = r.head.core.contentEpoch
            if (epoch != cur) return@synchronized null
            contentKeys[epoch] = key
            runCatching { store.cacheSecurityKey(DeviceSecurity.stateHash(r.head), epoch, key) }
            key
        }

    fun clearCache() = synchronized(historyLock) {
        keyRecord = null
        contentKeys.clear()
        lastFloorWrite = 0uL
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

    fun contentKey(epoch: ULong): ByteArray? {
        val r = read()
        return contentKey(r, epoch)
    }

    fun close() = synchronized(historyLock) {
        closed = true
        syncRound.changed()
        keyRecord = null
        contentKeys.clear()
        lastFloorWrite = 0uL
        store.clearSecurityCache()
    }

    fun signingTime(wall: Long): Long = record().signingTime(wall)

    private fun now(r: DeviceSecurity.Record): ULong = maxOf(clock().toULong(), r.floor)

    /** Persist wake time locally; a slow relay must not close an independent terminal. */
    fun touch() {
        val r = read()
        store.touchSecurity(DeviceSecurity.stateHash(r.head), now(r))
    }

    suspend fun sync(budgetMs: Long = Long.MAX_VALUE): DeviceSecurity.Record {
        try {
            val (record, started) = if (budgetMs < Long.MAX_VALUE) {
                ownedNetworkOperation(budgetMs) { syncRoundWithStart() }
            } else {
                syncRoundWithStart()
            }
            onSync(record, started)
            return record
        } catch (e: CancellationException) {
            throw e
        } catch (e: Throwable) {
            onSyncFailed()
            throw e
        }
    }

    private suspend fun syncRoundWithStart(): Pair<DeviceSecurity.Record, Long> = syncRound.run {
        val started = clock()
        repeat(DeviceSecurity.MAX_STATES) {
            val before = read()
            val r = store.touchSecurity(DeviceSecurity.stateHash(before.head), now(before))
            val res = fetchStates(r.head.core.epoch)
            currentCoroutineContext().ensureActive()
            recoveryPending = res.recoveryPending
            recoveryReadyMs = res.recoveryReadyMs
            if (res.headEpoch.toULong() < r.head.core.epoch) throw DeviceSecurity.Failure("stale")
            if (res.statesCount == 0) {
                if (res.headEpoch.toULong() != r.head.core.epoch) {
                    throw DeviceSecurity.Failure("stale")
                }
                val cert =
                    res.lease.toByteArray().takeIf { it.isNotEmpty() }?.let { HeadLease.parse(it) }
                val finalRecord = if (cert != null) {
                    store.installSecurity(
                        DeviceSecurity.stateHash(r.head),
                        emptyList(),
                        emptyList(),
                        cert,
                    )
                } else {
                    r
                }
                if (DeviceSecurity.tooFarAhead(finalRecord.head, now(finalRecord))) {
                    clockSkew = true
                }
                return@run finalRecord to started
            }
            val states = res.statesList.map { DeviceSecurity.parseState(it.state.toByteArray()) }
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
        val updated = store.installSecurity(DeviceSecurity.stateHash(r.head), states, proofs, lease)
        onInstall(updated)
    }

    companion object {
        internal const val FLOOR_STEP = 60_000uL
    }
}
