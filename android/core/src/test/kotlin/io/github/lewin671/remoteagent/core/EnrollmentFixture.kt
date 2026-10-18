package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import okhttp3.OkHttpClient

/** Synthetic fixture identities approve enrollment through the real server. */
suspend fun approvedClient(authorizer: Account, http: OkHttpClient, kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT): Account {
    val api = Api(authorizer.server, http)
    val headers = Api.headers(login(api, authorizer))
    val a = PairKey(); val b = PairKey()
    val seed = randomBytes(32); val pub = SigningKey(seed).publicKey
    val created = api.pairing.createPairing(CreatePairingRequest.newBuilder().setCommit(ByteString.copyFrom(Pairing.commit(a.public))).build(), headers).value()
    val joined = api.pairing.joinPairing(JoinPairingRequest.newBuilder().setCode(created.code).setJoinerPublic(ByteString.copyFrom(b.public)).setJoinerKind(kind).setDevicePublicKey(ByteString.copyFrom(pub)).build(), emptyMap()).value()
    api.pairing.revealPairing(RevealPairingRequest.newBuilder().setPairingId(created.pairingId).setInitiatorPublic(ByteString.copyFrom(a.public)).build(), headers).value()
    val tr = Pairing.identityTranscript(created.pairingId, a.public, b.public, pub, kind.number)
    val shared = a.derive(b.public, tr)
    check(shared.key.contentEquals(b.derive(a.public, tr).key))
    val (approval, sig) = makePairApproval(authorizer, created.pairingId, pub, kind, created.expiresMs)
    val payload = PairingPayload.newBuilder().setRoot(ByteString.copyFrom(authorizer.root)).setServer(authorizer.server).setApproval(ByteString.copyFrom(approval)).setApprovalSignature(ByteString.copyFrom(sig)).build()
    val env = Envelopes.seal(shared.key, Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_PAIRING).setRequestId(created.pairingId).setSender(authorizer.deviceId).build(), payload)
    api.pairing.approvePairing(ApprovePairingRequest.newBuilder().setPairingId(created.pairingId).setPayload(env).setApproval(ByteString.copyFrom(approval)).setApprovalSignature(ByteString.copyFrom(sig)).build(), headers).value()
    val result = joinWithRoot(authorizer.root, authorizer.server, http, kind = kind, enrollment = Enrollment(seed, created.pairingId, approval, sig))
    api.pairing.getPairing(GetPairingRequest.newBuilder().setPairingId(created.pairingId).setCandidateToken(joined.candidateToken).setReceived(true).build(), emptyMap()).value()
    return result
}
