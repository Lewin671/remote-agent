package io.github.lewin671.remoteagent.core

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

    fun needsRenewal(head: DeviceSecurity.State, now: Long): Boolean {
        val at = now.toULong()
        if (at < head.core.issuedAt) return false
        val remaining = if (at >= head.core.notAfter) 0uL else head.core.notAfter - at
        return remaining < DAY_MS && at - head.core.issuedAt >= HALF_DAY_MS
    }

    fun leaseValid(head: DeviceSecurity.State, now: Long): Boolean = head.core.notAfter >= now.toULong()

    fun sorted(members: List<DeviceSecurity.Member>): List<DeviceSecurity.Member> =
        members.sortedBy { it.id }

    fun newMember(ed: SigningKey, xPrivate: ByteArray, kind: UInt): DeviceSecurity.Member {
        check(xPrivate.size == 32)
        val x = X25519.publicKey(xPrivate)
        return DeviceSecurity.Member(deviceId(ed.publicKey), ed.publicKey, x, kind)
    }

    fun memberWithX(
        edPublic: ByteArray,
        xPrivate: ByteArray,
        kind: UInt,
    ): DeviceSecurity.Member {
        check(edPublic.size == 32 && xPrivate.size == 32)
        return DeviceSecurity.Member(
            deviceId(edPublic),
            edPublic,
            X25519.publicKey(xPrivate),
            kind,
        )
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
                now.toULong(),
                renewAfter(now).toULong(),
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

    /** Removes members; the authority phone cannot remove itself. */
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
        newXPrivate: ByteArray,
        oldSecret: ByteArray,
        newSecret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        val old = parent.core.member(target) ?: throw DeviceSecurity.Failure()
        check(target !in parent.core.removed)
        val members =
            parent.core.members.map {
                if (it.id == target)
                    it.copy(x = X25519.publicKey(newXPrivate))
                else it
            }
        check(members.first { it.id == target }.x != old.x)
        return membership(
            parent,
            members,
            parent.core.removed,
            oldSecret,
            newSecret,
            signer,
            now,
        )
    }

    /** A lease renewal: Epoch + 1, same members and content key, no PrevSecret. */
    fun renewal(
        parent: DeviceSecurity.State,
        secret: ByteArray,
        signer: SigningKey,
        now: Long,
    ): DeviceSecurity.State {
        check(secret.size == 32)
        val core = parent.core.copy(parent = DeviceSecurity.stateHash(parent), epoch = parent.core.epoch + 1uL)
        val issued = maxOf(now.toULong(), core.issuedAt)
        return build(
            core.copy(issuedAt = issued, notAfter = renewAfter(issued.toLong()).toULong()),
            secret,
            ByteArray(0),
            signer,
        )
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
        val core = parent.core.copy(parent = DeviceSecurity.stateHash(parent), epoch = parent.core.epoch + 1uL)
        val issued = maxOf(now.toULong(), core.issuedAt)
        return build(
            core.copy(
                recoveryEd = newRecoveryEd,
                issuedAt = issued,
                notAfter = renewAfter(issued.toLong()).toULong(),
            ),
            secret,
            ByteArray(0),
            signer,
        )
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
                issuedAt = maxOf(now.toULong(), parent.core.issuedAt),
                notAfter = renewAfter(maxOf(now, parent.core.issuedAt.toLong())).toULong(),
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
                core.notAfter,
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
                issuedAt = maxOf(now.toULong(), parent.core.issuedAt),
                notAfter = renewAfter(maxOf(now, parent.core.issuedAt.toLong())).toULong(),
                members = sorted(members),
                removed = removed.sorted(),
            )
        return build(core, secret, prevSecret, signer)
    }

    private fun build(
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
        return org.bouncycastle.crypto.params.X25519PrivateKeyParameters(privateKey.copyOf(), 0)
            .generatePublicKey()
            .encoded
    }
}

/** What creating an account produced: the account, its store and the recovery code to show once. */
data class CreatedAccount(
    val account: Account,
    val record: DeviceSecurity.Record,
    val recoveryCode: String,
)

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
): CreatedAccount {
    val keys = Keys.derive(root)
    val accountId = deviceId(keys.account.publicKey)
    val signer = SigningKey(deviceSeed)
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
        joinWithRoot(
            root,
            server,
            http,
            setupToken = setupToken,
            kind = DeviceKind.DEVICE_KIND_CLIENT,
            seed = deviceSeed,
        )
    val api = Api(server, http)
    api.client
        .pinSecurityDomain(
            io.github.lewin671.remoteagent.proto.PinSecurityDomainRequest.newBuilder()
                .setGenesisState(
                    com.google.protobuf.ByteString.copyFrom(DeviceSecurity.stateBytes(genesis)),
                )
                .build(),
            Api.headers(login(api, account)),
        )
        .value()
    val record =
        DeviceSecurity.Record.fromChain(
            accountId,
            genesis.core.domain,
            DeviceSecurity.stateHash(genesis),
            phone.id,
            xPrivate,
            listOf(DeviceSecurity.stateBytes(genesis)),
            listOf(null),
        )
    return CreatedAccount(account, record, RecoveryCode.encode(recoverySeed))
}
