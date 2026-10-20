package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient

/**
 * Actual Go state chain and envelopes for synthetic cross-language fixtures. The separate process
 * has no account files, keychain or production access. Its record is the v3 JSON from
 * internal/testutil/securityfixture.
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
                .toString()
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

    fun record(reply: JsonObject): DeviceSecurity.Record {
        val o = reply["Record"]!!.jsonObject
        val states =
            o["states"]!!.jsonArray.map { Base64.getDecoder().decode(it.jsonPrimitive.content) }
        val proofs =
            o["proofs"]!!.jsonArray.map {
                if (it is JsonNull) null else Base64.getDecoder().decode(it.jsonPrimitive.content)
            }
        return DeviceSecurity.Record.fromChain(
            o["account"]!!.jsonPrimitive.content,
            Base64.getDecoder().decode(o["domain"]!!.jsonPrimitive.content),
            Base64.getDecoder().decode(o["genesis"]!!.jsonPrimitive.content),
            o["member"]!!.jsonPrimitive.content,
            Base64.getDecoder().decode(o["x"]!!.jsonPrimitive.content),
            states,
            proofs,
        ).installed(emptyList(),emptyList(),HeadLease.parse(Base64.getDecoder().decode(o["lease"]!!.jsonPrimitive.content)))
    }

    /** Adds a member to the fixture's chain and returns its record and the new state. */
    fun enroll(seed: ByteArray, kind: DeviceKind): Pair<DeviceSecurity.Record, ByteArray> {
        val reply = call("enroll", "Seed" to blob(seed), "Kind" to JsonPrimitive(kind.number))
        return record(reply) to bytes(reply, "State")
    }

    fun record(member: String): DeviceSecurity.Record =
        record(call("record", "Member" to JsonPrimitive(member)))

    /** Removes members as the fixture's authority and returns the new state. */
    fun remove(targets: List<String>): ByteArray {
        val reply = call("remove", "Targets" to JsonArray(targets.map { JsonPrimitive(it) }))
        return bytes(reply, "State")
    }

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

        /** Starts the fixture on the account's authority phone identity. */
        fun initialize(
            account: Account,
            recovery: ByteArray? = null,
        ): Pair<TestSecurity, ByteArray> {
            check(!all.containsKey(account.server))
            val bin =
                File(
                    System.getProperty("raSecurityFixture")
                        ?: error("security fixture binary missing")
                )
            val process =
                ProcessBuilder(bin.absolutePath)
                    .redirectError(ProcessBuilder.Redirect.INHERIT)
                    .start()
            val fixture = TestSecurity(process)
            val accountId =
                account.accountId.ifEmpty { deviceId(Keys.derive(account.root).account.publicKey) }
            val reply =
                fixture.call(
                    "init",
                    "Account" to JsonPrimitive(accountId),
                    "Seed" to blob(account.deviceSeed),
                    "Recovery" to blob(recovery ?: byteArrayOf()),
                )
            fixture.stores[account.deviceId] = TestSecurityStore(fixture.record(reply))
            all[account.server] = fixture
            return fixture to fixture.bytes(reply, "Genesis")
        }
    }
}

class TestSecurityStore(initial: DeviceSecurity.Record) : DeviceSecurity.Storage {
    private var operation:SecurityOperation?=null
    override fun readSecurityOperation()=operation
    override fun writeSecurityOperation(operation:SecurityOperation){this.operation=operation}
    override fun clearSecurityOperation(){operation=null}
    private var record = initial.let{r -> if(r.lease==null && r.head.core.authorityID==deviceId(SigningKey(ByteArray(32){0x11}).publicKey)) r.installed(emptyList(),emptyList(),HeadLease.sign(r.head,SigningKey(ByteArray(32){0x11}),r.head.core.issuedAt.toLong())) else r }

    @Synchronized override fun readSecurity() = record

    @Synchronized
    override fun initializeSecurity(record: DeviceSecurity.Record) {
        throw DeviceSecurity.Failure("conflict")
    }

    @Synchronized
    override fun installSecurity(
        expectedState: ByteArray,
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?>,
        lease: HeadLease?,
    ): DeviceSecurity.Record {
        if (!DeviceSecurity.stateHash(record.head).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        record = record.installed(states, proofs,lease)
        return record
    }

    @Synchronized
    override fun touchSecurity(expectedState: ByteArray, floor: ULong): DeviceSecurity.Record {
        if (!DeviceSecurity.stateHash(record.head).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        record = record.touched(floor)
        return record
    }

    @Synchronized
    override fun cacheSecurityKey(expectedState: ByteArray, epoch: ULong, secret: ByteArray) {
        if (!DeviceSecurity.stateHash(record.head).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        if (!record.cachedKeys().containsKey(epoch)) record = record.withKey(epoch, secret)
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
    tokenStorage: LoginTokenStore? = null,
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
        tokenStorage,
    )
