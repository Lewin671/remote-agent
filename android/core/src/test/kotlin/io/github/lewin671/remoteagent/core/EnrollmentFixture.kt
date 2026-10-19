package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import okhttp3.OkHttpClient

/** Creates a new account with its authority phone and pins the genesis, as the app does. */
suspend fun newAuthorityAccount(
    root: ByteArray,
    server: String,
    http: OkHttpClient,
    setupToken: String,
    recoverySeed: ByteArray? = null,
): Account {
    val phone =
        joinWithRoot(
            root,
            server,
            http,
            setupToken = setupToken,
            beforeRegister = {},
            kind = DeviceKind.DEVICE_KIND_CLIENT,
        )
    val (_, genesis) = TestSecurity.initialize(phone, recoverySeed)
    val api = Api(phone.server, http)
    api.client
        .pinSecurityDomain(
            PinSecurityDomainRequest.newBuilder()
                .setGenesisState(ByteString.copyFrom(genesis))
                .build(),
            Api.headers(login(api, phone)),
        )
        .value()
    val record = TestSecurity.get(server).stores.getValue(phone.deviceId).readSecurity()
    api.client
        .putSecurityState(
            PutSecurityStateRequest.newBuilder()
                .setLease(ByteString.copyFrom(record.lease!!.bytes()))
                .build(),
            Api.headers(login(api, phone)),
        )
        .value()
    return phone
}

/**
 * Adds a member with the Kotlin authority builder, uploads the state, registers the device with the
 * phone's approval, and keeps the chain for the new device.
 */
suspend fun approvedClient(
    authority: Account,
    http: OkHttpClient,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    seed: ByteArray = randomBytes(32),
): Account {
    val security = TestSecurity.get(authority.server)
    val phoneStore = security.stores.getValue(authority.deviceId)
    val phone = phoneStore.readSecurity()
    val signer = SigningKey(seed)
    val x = randomBytes(32)
    val member = Authority.newMember(signer, x, kind.number.toUInt())
    val old = phone.contentKey(phone.head.core.contentEpoch) ?: error("no content key")
    val next =
        Authority.pair(
            phone.head,
            member,
            old,
            Authority.newSecret(),
            SigningKey(authority.deviceSeed),
            System.currentTimeMillis(),
        )
    val lease = HeadLease.sign(next, SigningKey(authority.deviceSeed), next.core.issuedAt.toLong())
    val api = Api(authority.server, http)
    api.client
        .putSecurityState(
            PutSecurityStateRequest.newBuilder()
                .setLease(ByteString.copyFrom(lease.bytes()))
                .addStates(
                    SecurityState.newBuilder()
                        .setState(ByteString.copyFrom(DeviceSecurity.stateBytes(next)))
                        .build(),
                )
                .build(),
            Api.headers(login(api, authority)),
        )
        .value()
    for (s in security.stores.values.toList()) {
        val head = s.readSecurity().head
        if (next.core.epoch == head.core.epoch + 1uL) {
            runCatching {
                s.installSecurity(DeviceSecurity.stateHash(head), listOf(next), listOf(null), lease)
            }
        }
    }
    val now = System.currentTimeMillis()
    val (approval, signature) =
        makePairApproval(
            authority,
            newRequestId(),
            signer.publicKey,
            kind,
            now + 10 * 60_000,
        )
    val result =
        registerDevice(
            authority.server,
            http,
            authority.accountPublicKey,
            seed,
            kind,
            now,
            approval = approval,
            approvalSignature = signature,
            accountId = authority.accountId,
            root = if (kind == DeviceKind.DEVICE_KIND_MACHINE) ByteArray(0) else authority.root,
            accountKey = authority.accountPublicKey,
            beforeRegister = {},
        )
    val chain = (phone.chain + next).map { DeviceSecurity.stateBytes(it) }
    security.stores[result.deviceId] =
        TestSecurityStore(
            DeviceSecurity.Record.fromChain(
                    phone.pin.account,
                    phone.pin.domain,
                    phone.pin.genesis,
                    result.deviceId,
                    x,
                    chain,
                    chain.map { null },
                )
                .installed(emptyList(), emptyList(), lease),
        )
    return result
}

/** Adds a member through the Go fixture, so tests can seal with its real endpoint. */
suspend fun fixtureMember(
    authority: Account,
    http: OkHttpClient,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    seed: ByteArray = randomBytes(32),
): Account {
    val security = TestSecurity.get(authority.server)
    val (record, state) = security.enroll(seed, kind)
    val lease = record.lease!!
    val api = Api(authority.server, http)
    api.client
        .putSecurityState(
            PutSecurityStateRequest.newBuilder()
                .setLease(ByteString.copyFrom(lease.bytes()))
                .addStates(
                    SecurityState.newBuilder().setState(ByteString.copyFrom(state)).build(),
                )
                .build(),
            Api.headers(login(api, authority)),
        )
        .value()
    val now = System.currentTimeMillis()
    val (approval, signature) =
        makePairApproval(
            authority,
            newRequestId(),
            SigningKey(seed).publicKey,
            kind,
            now + 10 * 60_000,
        )
    val result =
        registerDevice(
            authority.server,
            http,
            authority.accountPublicKey,
            seed,
            kind,
            now,
            approval = approval,
            approvalSignature = signature,
            accountId = authority.accountId,
            root = if (kind == DeviceKind.DEVICE_KIND_MACHINE) ByteArray(0) else authority.root,
            accountKey = authority.accountPublicKey,
            beforeRegister = {},
        )
    security.stores[result.deviceId] = TestSecurityStore(record)
    // The fixture's chain is the authority's; keep every member's record in step.
    for (id in security.stores.keys.toList()) {
        security.stores[id] = TestSecurityStore(security.record(id))
    }
    return result
}

/** Signs a removal as the authority phone and uploads it, as the device page does. */
suspend fun removeAsAuthority(phone: Account, target: String, http: OkHttpClient) {
    val security = TestSecurity.get(phone.server)
    val store = security.stores.getValue(phone.deviceId)
    val r = store.readSecurity()
    val old = r.contentKey(r.head.core.contentEpoch) ?: error("no content key")
    val next =
        Authority.remove(
            r.head,
            listOf(target),
            old,
            Authority.newSecret(),
            SigningKey(phone.deviceSeed),
            System.currentTimeMillis(),
        )
    val lease = HeadLease.sign(next, SigningKey(phone.deviceSeed), next.core.issuedAt.toLong())
    val api = Api(phone.server, http)
    api.client
        .putSecurityState(
            PutSecurityStateRequest.newBuilder()
                .setLease(ByteString.copyFrom(lease.bytes()))
                .addStates(
                    SecurityState.newBuilder()
                        .setState(ByteString.copyFrom(DeviceSecurity.stateBytes(next)))
                        .build(),
                )
                .build(),
            Api.headers(login(api, phone)),
        )
        .value()
    for (s in security.stores.values.toList()) {
        val head = s.readSecurity().head
        if (next.core.epoch == head.core.epoch + 1uL) {
            runCatching {
                s.installSecurity(DeviceSecurity.stateHash(head), listOf(next), listOf(null), lease)
            }
        }
    }
}
