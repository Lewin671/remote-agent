package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.proto.*
import java.util.Base64
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

/**
 * Foreground member session. Locked notification code receives a Notify child key only, never this
 * storage, signing key or complete content secret.
 */
class DeviceSecurityClient(
    private val store: DeviceSecurity.Storage,
    private val memberID: String,
    private val ed: SigningKey,
    private val expectedAccount: String? = null,
    private val exchange: suspend (DeviceSecurityRPC.Capsule) -> DeviceSecurityRPC.Capsule,
) {
    private val syncMutex = Mutex()
    private val historyLock = Any()
    private val historical = HashMap<ULong, Pair<DeviceSecurity.State, ByteArray>>()
    @Volatile private var closed = false

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
        return r.pin to r.state
    }

    fun close() {
        closed = true
        synchronized(historyLock) { historical.clear() }
    }

    suspend fun rpc(method: UInt, body: JsonElement): JsonElement {
        currentCoroutineContext().ensureActive()
        val r = read()
        val (q, w) = DeviceSecurityRPC.request(r.pin, memberID, ed, method, body)
        val reply = exchange(q)
        currentCoroutineContext().ensureActive()
        read()
        return r.openReply(w, reply)
    }

    suspend fun sync(): DeviceSecurity.Record = syncMutex.withLock {
        repeat(32768) {
            val r = read()
            val reply =
                rpc(3u, buildJsonObject { put("After", r.state.core.epoch.toLong()) }).jsonObject
            val head = hash(reply["Current"])
            val states =
                if (reply["States"] == JsonNull || reply["States"] == null) emptyList()
                else reply["States"]!!.jsonArray.toList()
            if (head.contentEquals(DeviceSecurity.stateHash(r.state))) {
                if (states.isNotEmpty()) throw DeviceSecurity.Failure()
                return@withLock r
            }
            if (states.isEmpty() || states.size > 2) throw DeviceSecurity.Failure("stale")
            var current = r
            for (value in states) {
                val next = DeviceSecurityRPC.stateFromJSON(value)
                try {
                    current = store.advanceSecurity(DeviceSecurity.stateHash(current.state), next)
                } catch (e: DeviceSecurity.Failure) {
                    if (e.code == "stale") break
                    throw e
                }
            }
            currentCoroutineContext().ensureActive()
        }
        throw DeviceSecurity.Failure("capacity")
    }

    /** Commit counter first; do not publish C before exact authorization. */
    suspend fun seal(
        purpose: DeviceSecurity.Purpose,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: ULong = 0uL,
        deadline: ULong = 0uL,
        plain: ByteArray,
        seed: ByteArray? = null,
    ): Envelope {
        repeat(3) {
            currentCoroutineContext().ensureActive()
            val old = read()
            val r = store.reserveSecurity(DeviceSecurity.stateHash(old.state))
            val h =
                DeviceSecurity.Header(
                    r.pin.account,
                    r.pin.domain,
                    r.state.core.epoch,
                    DeviceSecurity.stateHash(r.state),
                    r.state.core.contentEpoch,
                    memberID,
                    purpose.kind,
                    purpose,
                    machine,
                    session,
                    request,
                    seq,
                    deadline,
                    0u,
                    r.counter,
                )
            val formed = DeviceSecurity.form(r.secret(), seed, h, plain, ed)
            try {
                val reply = rpc(1u, DeviceSecurityRPC.authorizationRequest(formed))
                return DeviceSecurity.envelope(
                    DeviceSecurityRPC.attach(formed, reply, r.pin, r.state),
                )
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "stale") throw e
                sync()
            }
        }
        throw DeviceSecurity.Failure("stale")
    }

    suspend fun openCurrent(
        env: Envelope,
        seed: ByteArray? = null,
    ): Pair<DeviceSecurity.Header, ByteArray> {
        val w = DeviceSecurity.fromEnvelope(env)
        val r = sync()
        val (h, p) = DeviceSecurity.open(r.pin, r.state, w, r.secret(), seed)
        return h to unpackSecurity(h, p)
    }

    /** Read-only historical provenance; never a live action source. */
    suspend fun openHistory(
        env: Envelope,
        seed: ByteArray? = null,
    ): Pair<DeviceSecurity.Header, ByteArray> {
        val w = DeviceSecurity.fromEnvelope(env)
        val h = DeviceSecurity.parseHeader(w.formed.header)
        if (h.purpose.control) throw DeviceSecurity.Failure()
        var r = read()
        if (h.epoch > r.state.core.epoch) r = sync()
        if (h.epoch == r.state.core.epoch) {
            val (h, p) = DeviceSecurity.open(r.pin, r.state, w, r.secret(), seed)
            return h to unpackSecurity(h, p)
        }
        if (h.epoch > r.state.core.epoch) throw DeviceSecurity.Failure("stale")
        val cached =
            synchronized(historyLock) { historical[h.epoch] }
                ?: store.readSecurityHistory(h.epoch)?.let { it.state to it.secret }
        if (cached != null) {
            val (h, p) = DeviceSecurity.open(r.pin, cached.first, w, cached.second, seed)
            return h to unpackSecurity(h, p)
        }
        val sr = rpc(3u, buildJsonObject { put("After", (h.epoch - 1u).toLong()) }).jsonObject
        val state =
            DeviceSecurityRPC.stateFromJSON(
                sr["States"]!!.jsonArray.firstOrNull() ?: throw DeviceSecurity.Failure("untrusted"),
            )
        DeviceSecurity.verifyState(r.pin, state)
        if (state.core.epoch != h.epoch) throw DeviceSecurity.Failure("untrusted")
        val kr =
            rpc(4u, buildJsonObject { put("After", (h.contentEpoch - 1u).toLong()) }).jsonObject
        val key =
            kr["Keys"]!!.jsonArray.firstOrNull()?.jsonObject
                ?: throw DeviceSecurity.Failure("untrusted")
        if (key["Epoch"]!!.jsonPrimitive.long.toULong() != h.contentEpoch)
            throw DeviceSecurity.Failure("untrusted")
        val secret = decode64(key["Secret"]!!.jsonPrimitive.content)
        if (secret.size != 32) throw DeviceSecurity.Failure("untrusted")
        store.cacheSecurityHistory(DeviceSecurity.Historical(state, secret))
        synchronized(historyLock) {
            if (historical.size >= 64) historical.remove(historical.keys.min())
            historical[h.epoch] = state to secret
        }
        val (openedHeader, plain) = DeviceSecurity.open(r.pin, state, w, secret, seed)
        return openedHeader to unpackSecurity(openedHeader, plain)
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

    data class Catalog(val state: DeviceSecurity.State, val entries: List<DeviceSecurity.Wire>)

    suspend fun catalog(): Catalog {
        val r = sync()
        val expected = DeviceSecurity.stateHash(r.state)
        var cursor = byteArrayOf()
        val entries = ArrayList<DeviceSecurity.Wire>()
        repeat(4096) {
            val reply =
                rpc(
                        2u,
                        buildJsonObject {
                            put("After", Base64.getEncoder().encodeToString(cursor))
                            put(
                                "AtState",
                                JsonArray(expected.map { JsonPrimitive(it.toUByte().toInt()) }),
                            )
                        },
                    )
                    .jsonObject
            val state = DeviceSecurityRPC.stateFromJSON(reply["State"]!!)
            DeviceSecurity.verifyState(r.pin, state)
            if (!DeviceSecurity.stateHash(state).contentEquals(expected))
                throw DeviceSecurity.Failure("stale")
            val rows =
                if (reply["Entries"] == JsonNull || reply["Entries"] == null) emptyList()
                else reply["Entries"]!!.jsonArray.toList()
            for (row in rows) {
                entries.add(DeviceSecurityRPC.wireFromJSON(row.jsonObject["Wire"]!!))
                if (entries.size > 4096) throw DeviceSecurity.Failure("capacity")
            }
            val next = reply["Next"]
            if (next == null || next == JsonNull || next.jsonPrimitive.content.isEmpty())
                return Catalog(state, entries)
            val after = decode64(next.jsonPrimitive.content)
            if (compareBytes(after, cursor) <= 0) throw DeviceSecurity.Failure()
            cursor = after
        }
        throw DeviceSecurity.Failure("capacity")
    }

    data class Removal(
        val request: JsonObject,
        val phase: String,
        val waiting: List<String>,
        val error: String = "",
        val completion: ByteArray? = null,
        val chain: List<ByteArray> = emptyList(),
        val serverCleaned: Boolean = false,
    ) {
        val targets: List<String>
            get() =
                request["Targets"]!!.jsonArray.map { it.jsonObject["ID"]!!.jsonPrimitive.content }

        val operation: String
            get() = request["Operation"]!!.jsonPrimitive.content
    }

    private val removalMutex = Mutex()

    private fun readRemovals(): List<Removal> {
        val data = store.readRemovalOperations() ?: return emptyList()
        try {
            val array = Json.parseToJsonElement(data.toString(Charsets.UTF_8)).jsonArray
            if (array.size > 128) throw DeviceSecurity.Failure("storage")
            val current = read()
            return array.map {
                val o = it.jsonObject
                val request = o["Request"]!!.jsonObject
                if (
                    request["Account"]!!.jsonPrimitive.content != current.pin.account ||
                        request["Requester"]!!.jsonPrimitive.content != memberID ||
                        !hash(request["Domain"]).contentEquals(current.pin.domain)
                )
                    throw DeviceSecurity.Failure("storage")
                Removal(
                    request,
                    o["Phase"]!!.jsonPrimitive.content,
                    o["Waiting"]!!.jsonArray.map { it.jsonPrimitive.content },
                    o["Error"]?.jsonPrimitive?.content ?: "",
                    o["Completion"]
                        ?.takeIf { it != JsonNull }
                        ?.jsonPrimitive
                        ?.content
                        ?.let { decode64(it) },
                    o["Chain"]?.jsonArray?.map { decode64(it.jsonPrimitive.content) }
                        ?: emptyList(),
                    o["ServerCleaned"]?.jsonPrimitive?.booleanOrNull ?: false,
                )
            }
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure("storage")
        }
    }

    private suspend fun saveRemoval(op: Removal) = removalMutex.withLock {
        val operation = op.request["Operation"]!!.jsonPrimitive.content
        val all =
            readRemovals()
                .filterNot { it.request["Operation"]!!.jsonPrimitive.content == operation }
                .toMutableList()
        all.add(op)
        while (all.size > 128) {
            val old = all.indexOfFirst { it.phase == "complete" && it.serverCleaned }
            if (old < 0) throw DeviceSecurity.Failure("capacity")
            all.removeAt(old)
        }
        val data =
            JsonArray(
                    all.map { r ->
                        buildJsonObject {
                            put("Request", r.request)
                            put("Phase", r.phase)
                            put("Waiting", JsonArray(r.waiting.map { JsonPrimitive(it) }))
                            put("Error", r.error)
                            r.completion?.let {
                                put("Completion", Base64.getEncoder().encodeToString(it))
                            }
                            put(
                                "Chain",
                                JsonArray(
                                    r.chain.map {
                                        JsonPrimitive(Base64.getEncoder().encodeToString(it))
                                    },
                                ),
                            )
                            put("ServerCleaned", r.serverCleaned)
                        }
                    },
                )
                .toString()
                .toByteArray(Charsets.UTF_8)
        store.writeRemovalOperations(data)
    }

    suspend fun removalOperations(): List<Removal> = removalMutex.withLock { readRemovals() }

    suspend fun remove(target: String, commit: ByteArray, expected: ByteArray): Removal {
        val r = read()
        val request = DeviceSecurityRemoval.prepare(r.pin, memberID, target, expected, commit, ed)
        val op = Removal(request, "unconfirmed", emptyList())
        saveRemoval(op)
        return retryRemoval(op)
    }

    suspend fun retryRemoval(op: Removal): Removal {
        try {
            val group = rpc(8u, op.request).jsonObject
            val operations = group["Operations"]!!.jsonArray
            if (
                operations.none {
                    it.jsonObject["Request"]!!.jsonObject["Operation"] == op.request["Operation"]
                }
            )
                throw DeviceSecurity.Failure("untrusted")
            val pending = op.copy(phase = "pending", waiting = waiting(group), error = "")
            saveRemoval(pending)
            return pollRemoval(pending)
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            val next = op.copy(error = if (e is DeviceSecurity.Failure) e.code else "offline")
            saveRemoval(next)
            return next
        }
    }

    private fun waiting(group: JsonObject): List<String> {
        val receipts =
            if (group["Receipts"] == null || group["Receipts"] == JsonNull) emptySet()
            else
                group["Receipts"]!!
                    .jsonArray
                    .map { it.jsonObject["Executor"]!!.jsonPrimitive.content }
                    .toSet()
        return group["Required"]!!
            .jsonArray
            .map { it.jsonPrimitive.content }
            .filter { it !in receipts }
    }

    suspend fun pollRemoval(op: Removal): Removal {
        if (op.phase == "complete") return op
        val operation = op.request["Operation"]!!.jsonPrimitive.content
        val completion =
            try {
                rpc(9u, buildJsonObject { put("Operation", operation) })
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "pending") throw e
                val status =
                    rpc(
                            12u,
                            buildJsonObject {
                                put("Operation", operation)
                                put("After", 0)
                            },
                        )
                        .jsonObject["Group"]!!
                        .jsonObject
                val next = op.copy(phase = "pending", waiting = waiting(status), error = "")
                saveRemoval(next)
                return next
            }
        val group = completion.jsonObject["Group"]!!.jsonObject
        val base = DeviceSecurityRPC.stateFromJSON(group["Base"]!!)
        val head = DeviceSecurityRPC.stateFromJSON(group["Head"]!!)
        val chain = ArrayList<DeviceSecurity.State>()
        var after = base.core.epoch
        repeat(128) {
            if (after >= head.core.epoch) return@repeat
            val reply =
                rpc(
                        12u,
                        buildJsonObject {
                            put("Operation", operation)
                            put("After", after.toLong())
                        },
                    )
                    .jsonObject
            val states = reply["States"]!!.jsonArray
            if (states.size !in 1..2) throw DeviceSecurity.Failure("untrusted")
            for (value in states) {
                val state = DeviceSecurityRPC.stateFromJSON(value)
                if (state.core.epoch > head.core.epoch) throw DeviceSecurity.Failure("untrusted")
                chain.add(state)
                after = state.core.epoch
            }
        }
        DeviceSecurityRemoval.verify(read().pin, completion, op.request, chain)
        val done =
            op.copy(
                phase = "complete",
                waiting = emptyList(),
                error = "",
                completion = completion.toString().toByteArray(Charsets.UTF_8),
                chain = chain.map { DeviceSecurity.stateBytes(it) },
            )
        saveRemoval(done)
        return done
    }

    suspend fun markServerCleaned(op: Removal): Removal {
        val done = op.copy(serverCleaned = true, completion = null, chain = emptyList())
        saveRemoval(done)
        return done
    }

    private fun compareBytes(a: ByteArray, b: ByteArray): Int {
        for (i in 0 until minOf(a.size, b.size)) {
            val d = a[i].toUByte().toInt() - b[i].toUByte().toInt()
            if (d != 0) return d
        }
        return a.size - b.size
    }

    private fun hash(v: JsonElement?): ByteArray {
        try {
            val a = v!!.jsonArray
            if (a.size != 32) throw DeviceSecurity.Failure()
            return a.map {
                    val n = it.jsonPrimitive.int
                    if (n !in 0..255) throw DeviceSecurity.Failure()
                    n.toByte()
                }
                .toByteArray()
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    private fun decode64(s: String): ByteArray {
        try {
            if (s.length > DeviceSecurity.MAX_RPC * 2) throw DeviceSecurity.Failure()
            return Base64.getDecoder().decode(s).also {
                if (Base64.getEncoder().encodeToString(it) != s) throw DeviceSecurity.Failure()
            }
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }
}

internal fun unpackSecurity(h: DeviceSecurity.Header, p: ByteArray): ByteArray {
    val limit = if (h.purpose.mutable) 64 shl 10 else 4 shl 20
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
