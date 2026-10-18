package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient

/**
 * Actual Go authority/HPKE/signatures for synthetic cross-language fixtures. The separate process
 * has no account files, keychain, or production access.
 */
class TestSecurity private constructor(private val process: Process) : AutoCloseable {
    private val input = process.outputStream.bufferedWriter()
    private val output = process.inputStream.bufferedReader()
    val stores = ConcurrentHashMap<String, TestSecurityStore>()

    @Synchronized
    fun call(op: String, vararg fields: Pair<String, JsonElement>): JsonObject {
        input.write(
            buildJsonObject {
                put("Op", op)
                for ((key, value) in fields) put(key, value)
            }
                .toString(),
        )
        input.newLine()
        input.flush()
        val line = output.readLine() ?: error("synthetic security fixture exited")
        val reply = Json.parseToJsonElement(line).jsonObject
        check(reply["Error"] == null) { "synthetic security fixture: ${reply["Error"]}" }
        return reply
    }

    fun bytes(reply: JsonObject, key: String) =
        Base64.getDecoder().decode(reply[key]!!.jsonPrimitive.content)

    fun record(member: String): DeviceSecurity.Record =
        DeviceSecurity.Record.parse(
            bytes(call("record", "Member" to JsonPrimitive(member)), "Record"),
        )

    fun seal(account: Account, h: Header, plain: ByteArray, seed: ByteArray? = null): Envelope {
        val reply =
            call(
                "seal",
                "Member" to JsonPrimitive(account.deviceId),
                "Header" to blob(h.toByteArray()),
                "Plain" to blob(plain),
                "SessionSeed" to blob(seed ?: byteArrayOf()),
            )
        return Envelope.parseFrom(bytes(reply, "Envelope"))
    }

    fun seal(account: Account, h: Header, message: MessageLite, seed: ByteArray? = null) =
        seal(account, h, message.toByteArray(), seed)

    fun open(account: Account, env: Envelope, seed: ByteArray? = null): Pair<Header, ByteArray> {
        val reply =
            call(
                "open",
                "Member" to JsonPrimitive(account.deviceId),
                "Envelope" to blob(env.toByteArray()),
                "SessionSeed" to blob(seed ?: byteArrayOf()),
            )
        return Header.parseFrom(bytes(reply, "Header")) to bytes(reply, "Plain")
    }

    fun rpc(capsule: ByteArray): ByteArray =
        bytes(call("rpc", "Capsule" to blob(capsule)), "Capsule")

    override fun close() {
        input.close()
        process.waitFor(5, java.util.concurrent.TimeUnit.SECONDS)
        process.destroyForcibly()
        stores.clear()
    }

    companion object {
        private val all = ConcurrentHashMap<String, TestSecurity>()

        fun blob(b: ByteArray) = JsonPrimitive(Base64.getEncoder().encodeToString(b))

        fun get(server: String) = all[server] ?: error("synthetic authority was not initialized")

        fun find(server: String) = all[server]

        fun remove(server: String) {
            all.remove(server)?.close()
        }

        fun initialize(account: Account): Pair<TestSecurity, ByteArray> {
            check(!all.containsKey(account.server))
            val bin =
                File(
                    System.getProperty("raSecurityFixture")
                        ?: error("security fixture binary missing"),
                )
            val process =
                ProcessBuilder(bin.absolutePath)
                    .redirectError(ProcessBuilder.Redirect.INHERIT)
                    .start()
            val fixture = TestSecurity(process)
            val key = Keys.derive(account.root)
            val reply =
                fixture.call(
                    "init",
                    "Account" to JsonPrimitive(deviceId(key.account.publicKey)),
                    "Seed" to blob(account.deviceSeed),
                )
            fixture.stores[account.deviceId] =
                TestSecurityStore(DeviceSecurity.Record.parse(fixture.bytes(reply, "Record")))
            all[account.server] = fixture
            return fixture to fixture.bytes(reply, "Genesis")
        }
    }
}

class TestSecurityStore(initial: DeviceSecurity.Record) : DeviceSecurity.Storage {
    private var record = initial
    private val history = HashMap<ULong, DeviceSecurity.Historical>()
    private var removals: ByteArray? = null

    @Synchronized override fun readSecurity() = record

    @Synchronized
    override fun initializeSecurity(record: DeviceSecurity.Record) {
        throw DeviceSecurity.Failure("conflict")
    }

    @Synchronized
    override fun reserveSecurity(expectedState: ByteArray): DeviceSecurity.Record {
        if (!DeviceSecurity.stateHash(record.state).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        record = record.incremented()
        return record
    }

    @Synchronized
    override fun advanceSecurity(
        expectedState: ByteArray,
        next: DeviceSecurity.State,
    ): DeviceSecurity.Record {
        if (!DeviceSecurity.stateHash(record.state).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        history[record.state.core.epoch] = DeviceSecurity.Historical(record.state, record.secret())
        record = record.advanced(next)
        return record
    }

    @Synchronized override fun readSecurityHistory(epoch: ULong) = history[epoch]

    @Synchronized
    override fun cacheSecurityHistory(history: DeviceSecurity.Historical) {
        DeviceSecurity.verifyState(record.pin, history.state)
        this.history[history.state.core.epoch] = history
    }

    @Synchronized override fun readRemovalOperations() = removals?.copyOf()

    @Synchronized
    override fun writeRemovalOperations(data: ByteArray) {
        removals = data.copyOf()
    }
}

fun fixtureRemote(
    account: Account,
    http: OkHttpClient,
    cache: CipherCache? = null,
    log: (String) -> Unit = {},
    clock: () -> Long = System::currentTimeMillis,
    frameQueue: Int = 1024,
    historyIdleMs: Long = 60000,
    historyIdleCount: Int = 8,
    historyIdleBytes: Long = 16L * 1024 * 1024,
    streamOpenMs: Long = 15000,
    linkGraceMs: Long = 10000,
): Remote =
    Remote(
        account,
        http,
        cache,
        log,
        clock,
        frameQueue,
        historyIdleMs,
        historyIdleCount,
        historyIdleBytes,
        streamOpenMs,
        linkGraceMs,
        TestSecurity.find(account.server)?.stores?.get(account.deviceId),
    )
