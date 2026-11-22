package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.*
import okhttp3.OkHttpClient

private const val REGISTER_CONTEXT = "remote-agent/v1/register\u0000"
private const val PAIR_APPROVAL_CONTEXT = "remote-agent/v1/pair-approval\u0000"

/** Creates a new account with its authority client and pins the genesis, as the app does. */
suspend fun newAuthorityAccount(
    root: ByteArray,
    server: String,
    http: OkHttpClient,
    setupToken: String,
    recoverySeed: ByteArray? = null,
): Account {
    val accountPub = Corebind.ed25519PublicKey(root)
    val accountId = deviceIdForEdPublic(accountPub)
    val edSeed = randomBytes(32)
    val edPub = Corebind.ed25519PublicKey(edSeed)
    val deviceId = deviceIdForEdPublic(edPub)
    val reg = DeviceRegistration.newBuilder()
        .setAccountPublicKey(ByteString.copyFrom(accountPub))
        .setDevicePublicKey(ByteString.copyFrom(edPub))
        .setKind(DeviceKind.DEVICE_KIND_CLIENT)
        .setCreatedMs(System.currentTimeMillis())
        .build()
        .toByteArray()
    val sig = Corebind.ed25519Sign(root, REGISTER_CONTEXT.toByteArray() + reg)
    val api = Api(server, http)
    val res = api.auth.registerDevice(
        RegisterDeviceRequest.newBuilder()
            .setRegistration(ByteString.copyFrom(reg))
            .setSignature(ByteString.copyFrom(sig))
            .setSetupToken(setupToken)
            .setProtocolVersion(RELAY_PROTOCOL_VERSION)
            .build(),
        emptyMap(),
    ).value()
    check(res.deviceId == deviceId) { "registered deviceId mismatch" }

    val phone = Account(server, deviceId, accountId)
    val (fixture, genesis) = TestSecurity.initialize(phone, edSeed = edSeed, rootSeed = root, recovery = recoverySeed)
    val token = loginWithStore(api, phone.deviceId, fixture.stores.getValue(phone.deviceId)).token
    api.client.pinSecurityDomain(
        PinSecurityDomainRequest.newBuilder()
            .setGenesisState(ByteString.copyFrom(genesis))
            .build(),
        Api.headers(token),
    ).value()
    val record = fixture.record(phone.deviceId)
    api.client.putSecurityState(
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(record.lease))
            .build(),
        Api.headers(token),
    ).value()
    fixture.syncStoresForAll()
    return phone
}

/**
 * Adds a member with the Go security fixture, uploads the state, registers the device with the
 * phone's approval, and keeps the chain for the new device.
 */
suspend fun approvedClient(
    authority: Account,
    http: OkHttpClient,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    seed: ByteArray = randomBytes(32),
): Account = fixtureMember(authority, http, kind, seed)

/** Adds a member through the Go fixture, so tests can seal with its real endpoint. */
suspend fun fixtureMember(
    authority: Account,
    http: OkHttpClient,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    seed: ByteArray = randomBytes(32),
): Account {
    val security = TestSecurity.get(authority.server)
    val authoritySeed = security.deviceSeeds.getValue(authority.deviceId)
    val authorityPub = Corebind.ed25519PublicKey(authoritySeed)
    val rootSeed = security.accountRoots[authority.accountId]
    val accountPub = rootSeed?.let { Corebind.ed25519PublicKey(it) } ?: ByteArray(0)
    val (record, state) = security.enroll(seed, kind)
    val api = Api(authority.server, http)
    val authToken = loginWithStore(api, authority.deviceId, security.stores.getValue(authority.deviceId)).token
    api.client.putSecurityState(
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(record.lease))
            .addStates(SecurityState.newBuilder().setState(ByteString.copyFrom(state)).build())
            .build(),
        Api.headers(authToken),
    ).value()

    val now = System.currentTimeMillis()
    val candPub = Corebind.ed25519PublicKey(seed)
    val reg = DeviceRegistration.newBuilder()
        .setAccountPublicKey(ByteString.copyFrom(accountPub))
        .setDevicePublicKey(ByteString.copyFrom(candPub))
        .setKind(kind)
        .setCreatedMs(now)
        .build()
        .toByteArray()
    val approval = PairApproval.newBuilder()
        .setAccountId(authority.accountId)
        .setPairingId(newRequestId())
        .setApproverDeviceId(authority.deviceId)
        .setApproverPublicKey(ByteString.copyFrom(authorityPub))
        .setCandidateDevicePublicKey(ByteString.copyFrom(candPub))
        .setCandidateKind(kind)
        .setExpiresMs(now + 10 * 60_000)
        .setNonce(ByteString.copyFrom(randomBytes(16)))
        .build()
        .toByteArray()
    val approvalSig = Corebind.ed25519Sign(authoritySeed, PAIR_APPROVAL_CONTEXT.toByteArray() + approval)
    val regRes = api.auth.registerDevice(
        RegisterDeviceRequest.newBuilder()
            .setRegistration(ByteString.copyFrom(reg))
            .setApproval(ByteString.copyFrom(approval))
            .setApprovalSignature(ByteString.copyFrom(approvalSig))
            .setProtocolVersion(RELAY_PROTOCOL_VERSION)
            .build(),
        emptyMap(),
    ).value()
    check(regRes.deviceId == record.member) { "member id mismatch" }

    security.seedMemberStore(record, seed)
    security.syncStoresForAll()
    return Account(authority.server, record.member, authority.accountId)
}

/** Signs a removal as the authority client and uploads it, as the device page does. */
suspend fun removeAsAuthority(phone: Account, target: String, http: OkHttpClient) {
    val security = TestSecurity.get(phone.server)
    val nextState = security.remove(listOf(target))
    val updated = security.record(phone.deviceId)
    val api = Api(phone.server, http)
    val token = loginWithStore(api, phone.deviceId, security.stores.getValue(phone.deviceId)).token
    api.client.putSecurityState(
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(updated.lease))
            .addStates(SecurityState.newBuilder().setState(ByteString.copyFrom(nextState)).build())
            .build(),
        Api.headers(token),
    ).value()
    security.syncStoresForAll()
}
