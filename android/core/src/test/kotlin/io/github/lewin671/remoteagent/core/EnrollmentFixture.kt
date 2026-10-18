package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import okhttp3.OkHttpClient

/** Synthetic fixture identities approve enrollment through the real server. */
suspend fun approvedClient(
    authorizer: Account,
    http: OkHttpClient,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
): Account {
    val security = TestSecurity.get(authorizer.server)
    val old = security.stores[authorizer.deviceId]!!.readSecurity()
    val descriptor = DeviceSecurityRPC.pairTrustDescriptor(old.pin, authorizer.deviceId)
    val api = Api(authorizer.server, http)
    val headers = Api.headers(login(api, authorizer))
    val a = PairKey()
    val b = PairKey()
    val seed = randomBytes(32)
    val pub = SigningKey(seed).publicKey
    val created =
        api.pairing
            .createPairing(
                CreatePairingRequest.newBuilder()
                    .setCommit(
                        ByteString.copyFrom(
                            DeviceSecurityRPC.pairDomainCommit(a.public, descriptor),
                        ),
                    )
                    .build(),
                headers,
            )
            .value()
    val enrolled =
        security.call(
            "enroll",
            "Approver" to kotlinx.serialization.json.JsonPrimitive(authorizer.deviceId),
            "Pair" to kotlinx.serialization.json.JsonPrimitive(created.pairingId),
            "Seed" to TestSecurity.blob(seed),
            "Kind" to kotlinx.serialization.json.JsonPrimitive(kind.number),
        )
    val record = DeviceSecurity.Record.parse(security.bytes(enrolled, "Record"))
    val candidate = record.state.core.members.first { it.id == deviceId(pub) }
    val joined =
        api.pairing
            .joinPairing(
                JoinPairingRequest.newBuilder()
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setCode(created.code)
                    .setJoinerPublic(ByteString.copyFrom(b.public))
                    .setJoinerKind(kind)
                    .setDevicePublicKey(ByteString.copyFrom(pub))
                    .setDeviceHpkePublicKey(ByteString.copyFrom(candidate.x))
                    .build(),
                emptyMap(),
            )
            .value()
    val contextJSON = enrolled["PairContext"]!!
    val context = DeviceSecurityRPC.pairContextFromJSON(contextJSON)
    api.pairing
        .revealPairing(
            RevealPairingRequest.newBuilder()
                .setPairingId(created.pairingId)
                .setInitiatorPublic(ByteString.copyFrom(a.public))
                .setSecurityContext(
                    ByteString.copyFrom(contextJSON.toString().toByteArray(Charsets.UTF_8)),
                )
                .build(),
            headers,
        )
        .value()
    val tr =
        Pairing.domainTranscript(
            created.pairingId,
            a.public,
            b.public,
            pub,
            candidate.x,
            kind.number,
            descriptor,
        )
    val shared = a.derive(b.public, tr)
    check(shared.key.contentEquals(b.derive(a.public, tr).key))
    val candidateSig = DeviceSecurityRPC.signPairContext(context, SigningKey(seed), "accept")
    api.pairing
        .getPairing(
            GetPairingRequest.newBuilder()
                .setPairingId(created.pairingId)
                .setCandidateToken(joined.candidateToken)
                .setCandidateSignature(ByteString.copyFrom(candidateSig))
                .build(),
            emptyMap(),
        )
        .value()
    val (approval, sig) =
        makePairApproval(authorizer, created.pairingId, pub, kind, created.expiresMs)
    val payload =
        PairingPayload.newBuilder()
            .setRoot(ByteString.copyFrom(authorizer.root))
            .setServer(authorizer.server)
            .setApproval(ByteString.copyFrom(approval))
            .setApprovalSignature(ByteString.copyFrom(sig))
            .setSecurityState(ByteString.copyFrom(security.bytes(enrolled, "State")))
            .build()
    val env =
        Envelopes.seal(
            shared.key,
            Header.newBuilder()
                .setKind(PayloadKind.PAYLOAD_KIND_PAIRING)
                .setRequestId(created.pairingId)
                .setSender(authorizer.deviceId)
                .build(),
            payload,
        )
    api.pairing
        .approvePairing(
            ApprovePairingRequest.newBuilder()
                .setPairingId(created.pairingId)
                .setPayload(env)
                .setApproval(ByteString.copyFrom(approval))
                .setApprovalSignature(ByteString.copyFrom(sig))
                .build(),
            headers,
        )
        .value()
    val result =
        joinWithRoot(
            authorizer.root,
            authorizer.server,
            http,
            kind = kind,
            enrollment = Enrollment(seed, created.pairingId, approval, sig),
        )
    security.stores[result.deviceId] = TestSecurityStore(record)
    api.pairing
        .getPairing(
            GetPairingRequest.newBuilder()
                .setPairingId(created.pairingId)
                .setCandidateToken(joined.candidateToken)
                .setReceived(true)
                .build(),
            emptyMap(),
        )
        .value()
    return result
}
