package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.proto.DeviceKind
import okhttp3.OkHttpClient

/**
 * The phone-side authority: creates the genesis, renews the lease and signs membership changes with
 * the device's existing Ed25519 key under the state-sign label. The recovery statement is the one
 * thing it cannot sign; that uses the recovery code (see [Recovery]).
 */
object Authority {
    /** A fresh random content secret for a new content epoch. */
    fun newSecret(): ByteArray = randomBytes(32)

    /** A fresh recovery seed and its Ed25519 public key (32 bytes). */
    fun newRecovery(): Pair<ByteArray, ByteArray> {
        val seed = randomBytes(32)
        return seed to SigningKey(seed).publicKey
    }

    fun renewAfter(now: Long): Long = now + DeviceSecurity.LEASE

    private val DAY_MS = 24uL * 60uL * 60uL * 1000uL
    private val HALF_DAY_MS = 12uL * 60uL * 60uL * 1000uL

    fun needsRenewal(lease: HeadLease?, now: Long): Boolean {
        if (lease == null) return true
        val at = now.coerceAtLeast(1).toULong()
        if (at < lease.issuedAt) return false
        val remaining = if (at >= lease.notAfter) 0uL else lease.notAfter - at
        return remaining < DAY_MS && at - lease.issuedAt >= HALF_DAY_MS
    }

    fun leaseValid(record: DeviceSecurity.Record, now: Long): Boolean = runCatching {
        record.lease?.valid(record.head, maxOf(now.coerceAtLeast(1).toULong(), record.floor))
            ?: throw DeviceSecurity.Failure("lease")
    }
        .isSuccess

    fun sorted(members: List<DeviceSecurity.Member>): List<DeviceSecurity.Member> =
        members.sortedBy {
            it.id
        }

    fun newMember(ed: SigningKey, xPrivate: ByteArray, kind: UInt): DeviceSecurity.Member {
        check(xPrivate.size == 32)
        val x = X25519.publicKey(xPrivate)
        return DeviceSecurity.Member(deviceId(ed.publicKey), ed.publicKey, x, kind)
    }

    fun memberWithX(edPublic: ByteArray, xPrivate: ByteArray, kind: UInt): DeviceSecurity.Member {
        check(edPublic.size == 32 && xPrivate.size == 32)
        return DeviceSecurity.Member(deviceId(edPublic), edPublic, X25519.publicKey(xPrivate), kind)
    }

    fun genesis(
        account: String,
        domain: ByteArray,
        member: DeviceSecurity.Member,
        recoveryEd: ByteArray,
        secret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(domain.size == 32 && secret.size == 32 && recoveryEd.size == 32)
        val core =
            DeviceSecurity.Core(
                account,
                domain,
                member.id,
                recoveryEd,
                ByteArray(32),
                1uL,
                1uL,
                now.coerceAtLeast(1).toULong(),
                listOf(member),
                emptyList(),
            )
        return build(core, secret, ByteArray(0), signer)
    }

    /** A normal state with a new content key: add, remove or X rotation. */
    fun membership(
        parent: DeviceSecurity.State,
        members: List<DeviceSecurity.Member>,
        removed: List<String>,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(oldSecret.size == 32 && newSecret.size == 32)
        val prev = DeviceSecurity.sealPrevSecret(newSecret, oldSecret)
        return buildState(parent, members, removed, newSecret, prev, signer, now)
    }

    /** Adds one candidate and seals the old content key for the existing members. */
    fun pair(
        parent: DeviceSecurity.State,
        candidate: DeviceSecurity.Member,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(parent.core.member(candidate.id) == null && candidate.id !in parent.core.removed)
        return membership(
            parent,
            sorted(parent.core.members + candidate),
            parent.core.removed,
            oldSecret,
            newSecret,
            signer,
            now,
        )
    }

    /** Removes members; the authority client cannot remove itself. */
    fun remove(
        parent: DeviceSecurity.State,
        targets: List<String>,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(targets.isNotEmpty() && targets.none { it == parent.core.authorityID })
        val victims = targets.toSet()
        check(victims.all { parent.core.member(it) != null })
        val members = parent.core.members.filterNot { it.id in victims }
        check(members.isNotEmpty())
        val removed = (parent.core.removed + victims).distinct().sorted()
        return membership(parent, members, removed, oldSecret, newSecret, signer, now)
    }

    /** Replaces one member's X25519 key, keeping its Ed identity and kind. */
    fun rotateX(
        parent: DeviceSecurity.State,
        target: String,
        newXPublic: ByteArray,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        val old = parent.core.member(target) ?: throw DeviceSecurity.Failure()
        check(target !in parent.core.removed)
        val members =
            parent.core.members.map {
                if (it.id == target) it.copy(x = newXPublic.copyOf()) else it
            }
        check(newXPublic.size == 32 && !newXPublic.contentEquals(old.x))
        return membership(parent, members, parent.core.removed, oldSecret, newSecret, signer, now)
    }

    /** A veto: any new state, with a new recovery key the user saves. */
    fun veto(
        parent: DeviceSecurity.State,
        newRecoveryEd: ByteArray,
        secret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(newRecoveryEd.size == 32 && secret.size == 32)
        val core =
            parent.core.copy(
                parent = DeviceSecurity.stateHash(parent),
                epoch = parent.core.epoch + 1uL,
            )
        val issued = maxOf(now.coerceAtLeast(1).toULong(), core.issuedAt)
        return build(
            core.copy(
                recoveryEd = newRecoveryEd,
                issuedAt = issued,
            ),
            secret,
            ByteArray(0),
            signer,
        )
    }

    /** Normal phone replacement keeps the backward key walk. */
    fun handoff(
        parent: DeviceSecurity.State,
        incoming: String,
        recoveryEd: ByteArray,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(
            incoming != parent.core.authorityID &&
                parent.core.member(incoming)?.kind == 2u &&
                recoveryEd.size == 32 &&
                !recoveryEd.contentEquals(parent.core.recoveryEd),
        )
        val core =
            parent.core.copy(
                parent = DeviceSecurity.stateHash(parent),
                epoch = parent.core.epoch + 1uL,
                contentEpoch = parent.core.contentEpoch + 1uL,
                authorityID = incoming,
                recoveryEd = recoveryEd,
                issuedAt = maxOf(now.coerceAtLeast(1).toULong(), parent.core.issuedAt),
                members = parent.core.members.filterNot { it.id == parent.core.authorityID },
                removed = (parent.core.removed + parent.core.authorityID).sorted(),
            )
        return build(core, newSecret, DeviceSecurity.sealPrevSecret(newSecret, oldSecret), signer)
    }

    /**
     * The recovery state: new authority member, new content key, no PrevSecret, old authority
     * removed and never re-added.
     */
    fun recovery(
        parent: DeviceSecurity.State,
        newMember: DeviceSecurity.Member,
        members: List<DeviceSecurity.Member>,
        secret: ByteArray,
        newSigner: SigningKey,
        oldRecoverySeed: ByteArray,
        newRecoveryEd: ByteArray,
        now: Long,
    ): Pair<DeviceSecurity.State, DeviceSecurity.RecoveryStatement> {
        check(
            newMember.kind == 2u &&
                deviceId(newSigner.publicKey) == newMember.id &&
                newRecoveryEd.size == 32 &&
                secret.size == 32 &&
                members.none { it.id == parent.core.authorityID },
        )
        val core =
            parent.core.copy(
                parent = DeviceSecurity.stateHash(parent),
                epoch = parent.core.epoch + 1uL,
                contentEpoch = parent.core.contentEpoch + 1uL,
                authorityID = newMember.id,
                recoveryEd = newRecoveryEd,
                issuedAt = maxOf(now.coerceAtLeast(1).toULong(), parent.core.issuedAt),
                members = sorted(members),
                removed = (parent.core.removed + parent.core.authorityID).distinct().sorted(),
            )
        val state = build(core, secret, ByteArray(0), newSigner)
        val statement =
            DeviceSecurity.RecoveryStatement(
                core.account,
                core.domain,
                DeviceSecurity.stateHash(parent),
                newMember.id,
                newRecoveryEd,
                newMember,
                core.issuedAt,
                DeviceSecurity.stateHash(state),
                randomBytes(32),
                ByteArray(0),
            )
        return state to DeviceSecurity.signRecoveryStatement(statement, oldRecoverySeed)
    }

    private fun buildState(
        parent: DeviceSecurity.State,
        members: List<DeviceSecurity.Member>,
        removed: List<String>,
        secret: ByteArray,
        prevSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(secret.size == 32)
        val core =
            parent.core.copy(
                parent = DeviceSecurity.stateHash(parent),
                epoch = parent.core.epoch + 1uL,
                contentEpoch = parent.core.contentEpoch + 1uL,
                issuedAt = maxOf(now.coerceAtLeast(1).toULong(), parent.core.issuedAt),
                members = sorted(members),
                removed = removed.sorted(),
            )
        return build(core, secret, prevSecret, signer)
    }

    internal fun build(
        core: DeviceSecurity.Core,
        secret: ByteArray,
        prevSecret: ByteArray,
        signer: SigningKey,
    ): DeviceSecurity.State {
        val state = DeviceSecurity.State(core, emptyList(), prevSecret, ByteArray(0))
        // Grants bind the hash of the canonical core, which DeviceSecurity computes itself.
        val ch = sha256("ra3/core\u0000".toByteArray(), DeviceSecurity.coreBytes(core))
        val grants = core.members.map { DeviceSecurity.sealGrant(it, core, ch, secret) }
        val signed = state.copy(grants = grants)
        return signed.copy(signature = signer.sign(DeviceSecurity.signBytes(signed)))
    }
}

/** X25519 helpers, using the same Bouncy Castle parameters as pairing. */
object X25519 {
    fun publicKey(privateKey: ByteArray): ByteArray {
        require(privateKey.size == 32)
        return org.bouncycastle.crypto.params
            .X25519PrivateKeyParameters(privateKey.copyOf(), 0)
            .generatePublicKey()
            .encoded
    }
}

/** What creating an account produced: the account, its store and the recovery code to show once. */
data class CreatedAccount(
    val account: Account,
    val record: DeviceSecurity.Record,
    val recoveryCode: String,
    /** The relay needs its exported genesis pinned offline before it will accept this domain. */
    val needsOfflinePin: Boolean = false,
    val registration: ByteArray = byteArrayOf(),
) {
    /** Validated against the operation read while holding the vault transaction. */
    fun checkSecurityProgressFrom(old: CreatedAccount) {
        if (record.floor < old.record.floor) throw DeviceSecurity.Failure("stale")
        val hash = DeviceSecurity.stateHash(record.head)
        val certificate = record.lease?.takeIf { it.head.contentEquals(hash) }
        if (
            hash.contentEquals(DeviceSecurity.stateHash(old.record.head)) &&
                old.record.leaseExpiration() != 0L &&
                certificate == null
        )
            throw DeviceSecurity.Failure("stale")
        old.record
            .touched(record.floor)
            .installed(
                record.chain.drop(old.record.chain.size),
                record.chain.drop(old.record.chain.size).map { record.proofs[it.core.epoch] },
                certificate,
            )
    }

    companion object {
        fun readOperationMaterial(b: ByteArray): CreatedAccount =
            readOperationMaterial(b) { s ->
                val server = s.readUTF()
                val root = s.blob()
                val seed = s.blob()
                val id = s.readUTF()
                val accountId = s.readUTF()
                val pub = s.blob()
                val record = DeviceSecurity.Record.parse(s.blob())
                val registration = s.blob()
                require(
                    root.size == 32 &&
                        seed.size == 32 &&
                        deviceId(SigningKey(seed).publicKey) == id &&
                        record.memberID == id &&
                        record.pin.account == accountId,
                )
                CreatedAccount(
                    Account(server, root, seed, id, accountId, pub),
                    record,
                    "",
                    registration = registration,
                )
            }
    }
}

/**
 * Creates a new account in the app: the phone generates the root key, its device identity and a
 * recovery code, signs the genesis, registers the first device and pins the state on the relay.
 */
suspend fun createAccount(
    server: String,
    http: OkHttpClient,
    setupToken: String = "",
    root: ByteArray = randomBytes(32),
    deviceSeed: ByteArray = randomBytes(32),
    now: Long = System.currentTimeMillis(),
    draft: CreatedAccount? = null,
    persistDraft: (CreatedAccount) -> Unit = { throw DeviceSecurity.Failure("storage") },
): CreatedAccount {
    var prepared =
        draft
            ?: run {
                val keys = Keys.derive(root)
                val id = deviceId(keys.account.publicKey)
                val signer = SigningKey(deviceSeed)
                val x = randomBytes(32)
                val phone = Authority.newMember(signer, x, 2u)
                val (seed, pub) = Authority.newRecovery()
                val g =
                    Authority.genesis(
                        id,
                        randomBytes(32),
                        phone,
                        pub,
                        Authority.newSecret(),
                        signer,
                        now,
                    )
                val record = DeviceSecurity.Record(DeviceSecurity.pinOf(g), listOf(g), phone.id, x)
                CreatedAccount(
                    Account(server, root, deviceSeed, phone.id, id, keys.account.publicKey),
                    record,
                    RecoveryCode.encode(seed, record.pin.genesis),
                )
            }
    require(prepared.account.server == server)
    // Durable local keys and genesis precede every remote side effect.
    persistDraft(prepared)
    val api = Api(server, http)
    val token =
        try {
            login(api, prepared.account)
        } catch (e: ConnectException) {
            if (e.code != Code.PERMISSION_DENIED && e.code != Code.NOT_FOUND) throw e
            suspend fun register(proof: io.github.lewin671.remoteagent.proto.SignupProof? = null) =
                joinWithRoot(
                    prepared.account.root,
                    server,
                    http,
                    setupToken = setupToken,
                    kind = DeviceKind.DEVICE_KIND_CLIENT,
                    seed = prepared.account.deviceSeed,
                    now = prepared.record.chain.first().core.issuedAt.toLong(),
                    signupProof = proof,
                    request =
                        prepared.registration
                            .takeIf { it.isNotEmpty() }
                            ?.let {
                                io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
                                    .parseFrom(it)
                                    .toBuilder()
                                    .setSetupToken(setupToken)
                                    .clearSignupProof()
                                    .apply { proof?.let { setSignupProof(it) } }
                                    .build()
                            },
                    beforeRegister = { request ->
                        prepared = prepared.copy(registration = request.toByteArray())
                        persistDraft(prepared)
                    },
                )
            try {
                register()
            } catch (registration: ConnectException) {
                if (registration.code != Code.FAILED_PRECONDITION) throw registration
                register(solveSignup(api))
            }
            login(api, prepared.account)
        }
    api.client
        .pinSecurityDomain(
            io.github.lewin671.remoteagent.proto.PinSecurityDomainRequest.newBuilder()
                .setGenesisState(
                    com.google.protobuf.ByteString.copyFrom(
                        DeviceSecurity.stateBytes(prepared.record.chain.first()),
                    ),
                )
                .build(),
            Api.headers(token),
        )
        .value()
    // A previous response may have been lost after pinning or code rotation.
    // Re-upload exactly the saved states, then rotate to a code shown now.
    if (prepared.record.chain.size > 1) {
        val request = io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
        prepared.record.chain.drop(1).forEach {
            request.addStates(
                io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                    .setState(
                        com.google.protobuf.ByteString.copyFrom(DeviceSecurity.stateBytes(it)),
                    ),
            )
        }
        api.client.putSecurityState(request.build(), Api.headers(token)).value()
    }
    if (prepared.recoveryCode.isEmpty()) {
        val (seed, pub) = Authority.newRecovery()
        val r = prepared.record
        val next =
            Authority.veto(
                r.head,
                pub,
                r.contentKey(r.head.core.contentEpoch)!!,
                SigningKey(prepared.account.deviceSeed),
                maxOf(now.coerceAtLeast(1).toULong(), r.floor, r.lease?.issuedAt ?: 0uL).toLong(),
            )
        prepared =
            prepared.copy(
                record = r.installed(listOf(next), listOf(null)),
                recoveryCode = RecoveryCode.encode(seed, r.pin.genesis),
            )
        persistDraft(prepared)
        api.client
            .putSecurityState(
                io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                    .addStates(
                        io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                            .setState(
                                com.google.protobuf.ByteString.copyFrom(
                                    DeviceSecurity.stateBytes(next),
                                ),
                            ),
                    )
                    .build(),
                Api.headers(token),
            )
            .value()
    }
    val lease =
        HeadLease.sign(
            prepared.record.head,
            SigningKey(prepared.account.deviceSeed),
            now,
            prepared.record.floor,
        )
    prepared = prepared.copy(record = prepared.record.installed(emptyList(), emptyList(), lease))
    persistDraft(prepared)
    api.client
        .putSecurityState(
            io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                .setLease(com.google.protobuf.ByteString.copyFrom(lease.bytes()))
                .build(),
            Api.headers(token),
        )
        .value()

    return prepared
}

/**
 * Rebuilds the protocol 10 security domain for an existing account upgraded from protocol 9: it
 * keeps the account root and the phone's Ed25519 device identity, starts a fresh domain with the
 * phone as the only member, and tries to pin it. A relay upgraded from layout 5 refuses a network
 * pin and requires the exported genesis offline instead; [CreatedAccount.needsOfflinePin] says that
 * happened, and the local record is still usable for export.
 */
suspend fun rebuildSecurityDomain(
    server: String,
    http: OkHttpClient,
    stored: Account,
    now: Long = System.currentTimeMillis(),
    draft: CreatedAccount? = null,
    persistDraft: (CreatedAccount) -> Unit = { throw DeviceSecurity.Failure("storage") },
): CreatedAccount {
    if (!stored.hasRoot) throw PairException("这台手机没有账户根密钥，不能重建安全域；请用管理端重新配对")
    val keys = Keys.derive(stored.root)
    val accountId = deviceId(keys.account.publicKey)
    if (stored.accountId.isNotEmpty() && stored.accountId != accountId)
        throw PairException("这台手机保存的账户与根密钥不一致")
    val prepared =
        draft
            ?: run {
                val signer = SigningKey(stored.deviceSeed)
                val xPrivate = randomBytes(32)
                val phone = Authority.newMember(signer, xPrivate, 2u)
                val (recoverySeed, recoveryEd) = Authority.newRecovery()
                val genesis =
                    Authority.genesis(
                        accountId,
                        randomBytes(32),
                        phone,
                        recoveryEd,
                        Authority.newSecret(),
                        signer,
                        now,
                    )
                val account =
                    Account(
                        server,
                        stored.root.copyOf(),
                        stored.deviceSeed.copyOf(),
                        stored.deviceId,
                        accountId,
                        keys.account.publicKey,
                    )
                val record =
                    DeviceSecurity.Record(
                        DeviceSecurity.pinOf(genesis),
                        listOf(genesis),
                        phone.id,
                        xPrivate,
                    )
                CreatedAccount(
                    account,
                    record,
                    RecoveryCode.encode(recoverySeed, record.pin.genesis),
                )
            }
    require(
        prepared.account.server == server &&
            prepared.account.deviceId == stored.deviceId &&
            prepared.account.root.contentEquals(stored.root) &&
            prepared.account.deviceSeed.contentEquals(stored.deviceSeed),
    )
    persistDraft(prepared)
    val account = prepared.account
    val genesis = prepared.record.chain.first()
    // The relay may be stopped for the offline pin (as the upgrade guide
    // requires), or it may refuse a network pin for an upgraded account. Both
    // leave a usable local domain that the operator pins offline.
    var offline = false
    fun unreachable(e: ConnectException): Boolean {
        val cause = e.exception
        return e.code in setOf(Code.UNAVAILABLE, Code.DEADLINE_EXCEEDED, Code.CANCELED) ||
            cause is java.net.ConnectException ||
            cause is java.net.UnknownHostException ||
            cause is java.net.SocketTimeoutException ||
            cause is java.net.NoRouteToHostException
    }
    try {
        val api = Api(server, http)
        val token = login(api, account)
        try {
            api.client
                .pinSecurityDomain(
                    io.github.lewin671.remoteagent.proto.PinSecurityDomainRequest.newBuilder()
                        .setGenesisState(
                            com.google.protobuf.ByteString.copyFrom(
                                DeviceSecurity.stateBytes(genesis),
                            ),
                        )
                        .build(),
                    Api.headers(token),
                )
                .value()
        } catch (e: ConnectException) {
            when {
                e.code == Code.FAILED_PRECONDITION && "offline" in e.message.orEmpty() ->
                    offline = true
                unreachable(e) -> offline = true
                else -> throw PairException(e.message ?: "安全域固定失败")
            }
        }
    } catch (e: ConnectException) {
        if (unreachable(e)) offline = true else throw PairException(e.message ?: "连不上服务器")
    }
    return prepared.copy(needsOfflinePin = offline)
}
