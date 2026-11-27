package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.*
import okhttp3.OkHttpClient

private const val REGISTER_CONTEXT = "remote-agent/v1/register\u0000"
private const val PAIR_APPROVAL_CONTEXT = "remote-agent/v1/pair-approval\u0000"

/** Test helper: logs in using the host [SecurityStoreV4] device Ed25519 key. */
internal suspend fun loginWithStore(api: TestApi, deviceId: String, store: SecurityStoreV4): LoginResponse {
    val ch = api.unary(
        "/remoteagent.v1.AuthService/GetChallenge",
        GetChallengeRequest.newBuilder().setDeviceId(deviceId).build(),
        GetChallengeResponse::parseFrom,
    )
    val sig = store.signKey(
        io.github.lewin671.remoteagent.core.proto.SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
        "",
        loginMessage(ch.challenge.toByteArray()),
    )
    return api.unary(
        "/remoteagent.v1.AuthService/Login",
        LoginRequest.newBuilder()
            .setProtocolVersion(RELAY_PROTOCOL_VERSION)
            .setDeviceId(deviceId)
            .setChallenge(ch.challenge)
            .setSignature(ByteString.copyFrom(sig))
            .build(),
        LoginResponse::parseFrom,
    )
}

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
    val api = TestApi(server, http)
    val res = api.unary(
        "/remoteagent.v1.AuthService/RegisterDevice",
        RegisterDeviceRequest.newBuilder()
            .setRegistration(ByteString.copyFrom(reg))
            .setSignature(ByteString.copyFrom(sig))
            .setSetupToken(setupToken)
            .setProtocolVersion(RELAY_PROTOCOL_VERSION)
            .build(),
        RegisterDeviceResponse::parseFrom,
    )
    check(res.deviceId == deviceId) { "registered deviceId mismatch" }

    val phone = Account(server, deviceId, accountId)
    val (fixture, genesis) = TestSecurity.initialize(phone, edSeed = edSeed, rootSeed = root, recovery = recoverySeed)
    val token = loginWithStore(api, phone.deviceId, fixture.stores.getValue(phone.deviceId)).token
    api.unary(
        "/remoteagent.v1.ClientService/PinSecurityDomain",
        PinSecurityDomainRequest.newBuilder()
            .setGenesisState(ByteString.copyFrom(genesis))
            .build(),
        PinSecurityDomainResponse::parseFrom,
        token,
    )
    val record = fixture.record(phone.deviceId)
    api.unary(
        "/remoteagent.v1.ClientService/PutSecurityState",
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(record.lease))
            .build(),
        PutSecurityStateResponse::parseFrom,
        token,
    )
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
    val api = TestApi(authority.server, http)
    val authToken = loginWithStore(api, authority.deviceId, security.stores.getValue(authority.deviceId)).token
    api.unary(
        "/remoteagent.v1.ClientService/PutSecurityState",
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(record.lease))
            .addStates(SecurityState.newBuilder().setState(ByteString.copyFrom(state)).build())
            .build(),
        PutSecurityStateResponse::parseFrom,
        authToken,
    )

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
    val regRes = api.unary(
        "/remoteagent.v1.AuthService/RegisterDevice",
        RegisterDeviceRequest.newBuilder()
            .setRegistration(ByteString.copyFrom(reg))
            .setApproval(ByteString.copyFrom(approval))
            .setApprovalSignature(ByteString.copyFrom(approvalSig))
            .setProtocolVersion(RELAY_PROTOCOL_VERSION)
            .build(),
        RegisterDeviceResponse::parseFrom,
    )
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
    val api = TestApi(phone.server, http)
    val token = loginWithStore(api, phone.deviceId, security.stores.getValue(phone.deviceId)).token
    api.unary(
        "/remoteagent.v1.ClientService/PutSecurityState",
        PutSecurityStateRequest.newBuilder()
            .setLease(ByteString.copyFrom(updated.lease))
            .addStates(SecurityState.newBuilder().setState(ByteString.copyFrom(nextState)).build())
            .build(),
        PutSecurityStateResponse::parseFrom,
        token,
    )
    security.syncStoresForAll()
}
