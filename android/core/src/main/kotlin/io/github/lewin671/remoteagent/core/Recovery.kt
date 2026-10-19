package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.CompleteRecoveryRequest
import io.github.lewin671.remoteagent.proto.GetRecoveryChallengeRequest
import io.github.lewin671.remoteagent.proto.GetRecoveryContextRequest
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import io.github.lewin671.remoteagent.proto.SecurityState
import io.github.lewin671.remoteagent.proto.SubmitRecoveryRequest
import okhttp3.OkHttpClient

/**
 * Recovery codes: a 32-byte Ed25519 seed shown once as Crockford Base32 and a QR code. The key only
 * signs a recovery statement; it cannot decrypt anything, and the phone does not store it.
 */
object RecoveryCode {
    private const val ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

    /**
     * What a code carries: the seed that signs recovery statements and the first 10 bytes of its
     * genesis hash. The prefix is how a recovering phone checks the chain the relay serves it
     * against its own domain (finding B3).
     */
    data class Decoded(val seed: ByteArray, val genesis: ByteArray)

    fun encode(seed: ByteArray, genesis: ByteArray): String {
        require(seed.size == 32 && genesis.size == 32)
        val payload = genesis.copyOf(10) + seed
        val out = StringBuilder()
        var bits = 0
        var value = 0
        for (b in payload) {
            value = (value shl 8) or (b.toInt() and 0xff)
            bits += 8
            while (bits >= 5) {
                bits -= 5
                out.append(ALPHABET[(value ushr bits) and 31])
            }
        }
        if (bits > 0) out.append(ALPHABET[(value shl (5 - bits)) and 31])
        return out.chunked(4).joinToString("-")
    }

    /**
     * Reads what [encode] wrote, accepting lower case, spaces, hyphens and the usual look-alikes.
     */
    fun decode(text: String): Decoded? {
        var bits = 0
        var value = 0
        val out = java.io.ByteArrayOutputStream()
        for (raw in text.trim().uppercase()) {
            if (raw == ' ' || raw == '-' || raw == '\n' || raw == '\r') continue
            val c =
                when (raw) {
                    'O' -> '0'
                    'I',
                    'L' -> '1'
                    else -> raw
                }
            val v = ALPHABET.indexOf(c)
            if (v < 0) return null
            value = (value shl 5) or v
            bits += 5
            if (bits >= 8) {
                bits -= 8
                out.write((value ushr bits) and 0xff)
            }
        }
        // Any leftover bits must be zero padding, as encode writes them.
        if (bits >= 5 || (value and ((1 shl bits) - 1)) != 0) return null
        val payload = out.toByteArray()
        if (payload.size != 42) return null
        return Decoded(payload.copyOfRange(10, 42), payload.copyOfRange(0, 10))
    }
}

/** A state chain read from the unauthenticated recovery service. */
data class RecoveryChain(
    val accountId: String,
    val accountPublicKey: ByteArray,
    val pin: DeviceSecurity.Pin,
    val chain: List<DeviceSecurity.State>,
    val phase: GetRecoveryContextResponse.Phase,
    val readyMs: Long,
    val recoveryId: String,
    val proofs: Map<ULong, ByteArray> = emptyMap(),
) {
    val head: DeviceSecurity.State
        get() = chain.last()

    companion object {
        fun parse(
            accountId: String,
            accountPublicKey: ByteArray,
            states: List<SecurityState>,
            phase: GetRecoveryContextResponse.Phase,
            readyMs: Long,
            recoveryId: String,
            expectedGenesis: ByteArray? = null,
            now: Long = System.currentTimeMillis(),
        ): RecoveryChain {
            if (states.isEmpty()) throw DeviceSecurity.Failure("untrusted")
            if (accountPublicKey.size != 32 || deviceId(accountPublicKey) != accountId)
                throw DeviceSecurity.Failure("untrusted")
            val chain = states.map { DeviceSecurity.parseState(it.state.toByteArray()) }
            if (chain.size > DeviceSecurity.MAX_STATES) throw DeviceSecurity.Failure("untrusted")
            if (chain[0].core.account != accountId) throw DeviceSecurity.Failure("untrusted")
            val pin = DeviceSecurity.pinOf(chain[0])
            DeviceSecurity.verifyGenesis(pin, chain[0])
            if (expectedGenesis != null) {
                // The code carries the domain's genesis prefix; a relay cannot
                // swap in a forged chain for the same account.
                if (
                    expectedGenesis.size != 10 ||
                        !pin.genesis.copyOf(10).contentEquals(expectedGenesis)
                )
                    throw DeviceSecurity.Failure("untrusted")
            }
            val proofs = HashMap<ULong, ByteArray>()
            chain.forEachIndexed { i, s ->
                DeviceSecurity.verifyState(pin, s)
                DeviceSecurity.requireNotFromFuture(s, now.toULong())
                if (i > 0) {
                    val raw = states[i].recovery.takeIf { it.size() > 0 }?.toByteArray()
                    val proof = raw?.let { DeviceSecurity.parseRecoveryStatement(it) }
                    DeviceSecurity.verifyTransition(chain[i - 1], s, proof)
                    if (raw != null) proofs[s.core.epoch] = raw
                }
            }
            return RecoveryChain(
                accountId,
                accountPublicKey,
                pin,
                chain,
                phase,
                readyMs,
                recoveryId,
                proofs,
            )
        }
    }
}

/** A recovery awaiting the authority phone's veto. */
data class PendingRecoveryInfo(val readyMs: Long)

/** A veto/rotation state signed locally; its new code is shown before this is published. */
data class PreparedRecovery(
    val code: String,
    val state: DeviceSecurity.State,
    val newRecoverySeed: ByteArray,
)

/** What a submission produced: shown once, then only the new code remains in the user's hands. */
data class RecoverySubmission(
    val recoveryId: String,
    val readyMs: Long,
    val newCode: String,
    val deviceSeed: ByteArray,
    val deviceId: String,
    val xPrivate: ByteArray,
    val accountId: String,
    val accountPublicKey: ByteArray,
    /** Hash of the recovery state this submission signed; the published head must be it. */
    val stateHash: ByteArray,
    /** First 10 bytes of the genesis the code was issued under. */
    val genesisPrefix: ByteArray,
)

/**
 * The new phone's side of an authority recovery. It needs an Ed25519 identity of its own (the same
 * seed it will keep) and the old recovery code; it never receives the old content key.
 */
object Recovery {
    suspend fun context(
        server: String,
        http: OkHttpClient,
        recoveryEd: ByteArray,
        signerSeed: ByteArray,
        recoveryId: String = "",
        expectedGenesis: ByteArray? = null,
    ): RecoveryChain {
        require(signerSeed.size == 32)
        val api = Api(server, http)
        val challenge =
            api.recovery
                .getRecoveryChallenge(GetRecoveryChallengeRequest.getDefaultInstance(), emptyMap())
                .value()
                .challenge
                .toByteArray()
        var ed = ByteArray(32)
        recoveryEd.copyInto(ed, 0, 0, minOf(32, recoveryEd.size))
        val signed =
            "ra3/recovery-context\u0000".toByteArray() + challenge + ed + recoveryId.toByteArray()
        val signature = SigningKey(signerSeed).sign(signed)
        val res =
            try {
                api.recovery
                    .getRecoveryContext(
                        GetRecoveryContextRequest.newBuilder()
                            .setRecoveryEd(
                                if (recoveryEd.isEmpty()) com.google.protobuf.ByteString.EMPTY
                                else com.google.protobuf.ByteString.copyFrom(recoveryEd),
                            )
                            .setRecoveryId(recoveryId)
                            .setChallenge(com.google.protobuf.ByteString.copyFrom(challenge))
                            .setSignature(com.google.protobuf.ByteString.copyFrom(signature))
                            .build(),
                        emptyMap(),
                    )
                    .value()
            } catch (e: com.connectrpc.ConnectException) {
                if (e.code == com.connectrpc.Code.NOT_FOUND) throw PairException("恢复码不对，或者没有待处理的恢复")
                throw e
            }
        return RecoveryChain.parse(
            res.accountId,
            res.accountPublicKey.toByteArray(),
            res.statesList,
            res.phase,
            res.readyMs,
            res.recoveryId,
            expectedGenesis = expectedGenesis,
        )
    }

    suspend fun resume(
        http: OkHttpClient,
        saved: RecoveryPublication,
        persist: (RecoveryPublication) -> Unit,
    ): RecoverySubmission {
        if (saved.submission.recoveryId.isNotEmpty()) return saved.submission
        val request =
            SubmitRecoveryRequest.newBuilder()
                .setStatement(com.google.protobuf.ByteString.copyFrom(saved.statement))
                .setState(com.google.protobuf.ByteString.copyFrom(saved.state))
                .build()
        val response = Api(saved.server, http).recovery.submitRecovery(request, emptyMap()).value()
        val submission =
            saved.submission.copy(recoveryId = response.recoveryId, readyMs = response.readyMs)
        persist(RecoveryPublication(saved.server, submission, saved.statement, saved.state))
        return submission
    }

    /**
     * Builds and submits the recovery state, and generates the new recovery code. The caller must
     * show [RecoverySubmission.newCode] once; it is not stored.
     */
    suspend fun submit(
        server: String,
        http: OkHttpClient,
        oldRecoverySeed: ByteArray,
        deviceSeed: ByteArray,
        genesisPrefix: ByteArray,
        now: Long = System.currentTimeMillis(),
        draft: RecoveryPublication? = null,
        persist: (RecoveryPublication) -> Unit = { throw DeviceSecurity.Failure("storage") },
    ): RecoverySubmission {
        if (oldRecoverySeed.size != 32 || deviceSeed.size != 32 || genesisPrefix.size != 10)
            throw PairException("恢复码无效")
        val prepared =
            draft
                ?: run {
                    val recoveryEd = SigningKey(oldRecoverySeed).publicKey
                    val chain =
                        context(
                            server,
                            http,
                            recoveryEd,
                            oldRecoverySeed,
                            expectedGenesis = genesisPrefix,
                        )
                    // A completed or vetoed row is no longer pending: the current code may
                    // submit a fresh recovery (B1).
                    if (
                        chain.phase != GetRecoveryContextResponse.Phase.PHASE_UNSPECIFIED &&
                            chain.phase != GetRecoveryContextResponse.Phase.PHASE_PENDING &&
                            chain.phase != GetRecoveryContextResponse.Phase.PHASE_READY
                    )
                        throw PairException("恢复状态无法识别")

                    val signer = SigningKey(deviceSeed)
                    val (newRecoverySeed, newRecoveryEd) = Authority.newRecovery()
                    val newCode = RecoveryCode.encode(newRecoverySeed, chain.pin.genesis)
                    val x = randomBytes(32)
                    val member = Authority.newMember(signer, x, 2u)
                    val old = chain.head
                    val members =
                        old.core.members.filterNot { it.id == old.core.authorityID } + member
                    val secret = Authority.newSecret()
                    val (state, statement) =
                        Authority.recovery(
                            old,
                            member,
                            members,
                            secret,
                            signer,
                            oldRecoverySeed,
                            newRecoveryEd,
                            now,
                        )
                    RecoveryPublication(
                        server,
                        RecoverySubmission(
                            "",
                            0,
                            newCode,
                            deviceSeed,
                            member.id,
                            x,
                            chain.accountId,
                            chain.accountPublicKey,
                            DeviceSecurity.stateHash(state),
                            chain.pin.genesis.copyOf(10),
                        ),
                        statement.marshal(),
                        DeviceSecurity.stateBytes(state),
                    )
                }
        require(prepared.server == server)
        require(prepared.submission.genesisPrefix.contentEquals(genesisPrefix))
        persist(prepared)
        val api = Api(server, http)
        val res =
            try {
                api.recovery
                    .submitRecovery(
                        SubmitRecoveryRequest.newBuilder()
                            .setStatement(
                                com.google.protobuf.ByteString.copyFrom(prepared.statement),
                            )
                            .setState(com.google.protobuf.ByteString.copyFrom(prepared.state))
                            .build(),
                        emptyMap(),
                    )
                    .value()
            } catch (e: com.connectrpc.ConnectException) {
                throw PairException(recoveryWords(e))
            }
        val submitted = prepared.submission.copy(recoveryId = res.recoveryId, readyMs = res.readyMs)
        persist(RecoveryPublication(prepared.server, submitted, prepared.statement, prepared.state))
        return submitted
    }

    /** Completes after 48 hours and returns the account to store. */
    suspend fun complete(
        server: String,
        http: OkHttpClient,
        recoveryId: String,
        accountId: String,
        deviceSeed: ByteArray,
        xPrivate: ByteArray,
        expectedState: ByteArray,
        now: Long = System.currentTimeMillis(),
    ): Pair<Account, DeviceSecurity.Record> {
        val signer = SigningKey(deviceSeed)
        val signature =
            signer.sign("ra3/recovery-complete\u0000".toByteArray() + recoveryId.toByteArray())
        val api = Api(server, http)
        val res =
            try {
                api.recovery
                    .completeRecovery(
                        CompleteRecoveryRequest.newBuilder()
                            .setRecoveryId(recoveryId)
                            .setSignature(com.google.protobuf.ByteString.copyFrom(signature))
                            .build(),
                        emptyMap(),
                    )
                    .value()
            } catch (e: com.connectrpc.ConnectException) {
                throw PairException(recoveryWords(e))
            }
        if (res.deviceId != deviceId(signer.publicKey)) throw PairException("服务器返回了意外的设备 ID")
        // The published chain is available by recovery ID, signed by the new
        // device key; the new phone opens its own grant.
        val chain = context(server, http, ByteArray(0), deviceSeed, recoveryId = recoveryId)
        if (
            chain.accountId != accountId ||
                chain.phase != GetRecoveryContextResponse.Phase.PHASE_PUBLISHED
        )
            throw PairException("服务器发布的恢复链与提交的不一致")
        if (chain.head.core.authorityID != res.deviceId) throw PairException("恢复链的管理端不是这台设备")
        if (!DeviceSecurity.stateHash(chain.head).contentEquals(expectedState))
            throw PairException("服务器发布的恢复状态与提交的不一致")
        val record =
            DeviceSecurity.Record(
                chain.pin,
                chain.chain,
                res.deviceId,
                xPrivate,
                proofs = chain.proofs,
            )
        return Account(
            server,
            ByteArray(0),
            deviceSeed,
            res.deviceId,
            accountId,
            res.accountPublicKey.toByteArray(),
        ) to record
    }

    private fun recoveryWords(e: com.connectrpc.ConnectException): String =
        when (e.code) {
            com.connectrpc.Code.FAILED_PRECONDITION -> "还不能完成，或者恢复已被否决"
            com.connectrpc.Code.PERMISSION_DENIED -> "恢复码不对，或者恢复声明已被拒绝"
            com.connectrpc.Code.RESOURCE_EXHAUSTED -> "试得太频繁了，稍等一分钟再试"
            com.connectrpc.Code.NOT_FOUND -> "找不到这次恢复，它可能已经完成或被否决"
            else -> errorText(e)
        }
}
