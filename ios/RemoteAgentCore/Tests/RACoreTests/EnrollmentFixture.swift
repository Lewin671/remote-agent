import Foundation
import RAProto

@testable import RACore

/// A synthetic phone authority: the fixture builds and signs the chain, and
/// the real server stores it. No iOS product code signs membership states.
final class SyntheticPhone: @unchecked Sendable {
    let account: Account
    let accountPublicKey: Data
    let pin: DeviceSecurity.Pin
    let server: String
    let http: HTTP
    let api: Api
    let token: String
    let fixture: TestSecurity

    private init(
        account: Account, accountPublicKey: Data, pin: DeviceSecurity.Pin, server: String,
        http: HTTP, api: Api, token: String, fixture: TestSecurity
    ) {
        self.account = account
        self.accountPublicKey = accountPublicKey
        self.pin = pin
        self.server = server
        self.http = http
        self.api = api
        self.token = token
        self.fixture = fixture
    }

    /// Creates the account's first device as a client, pins its genesis state,
    /// and returns the phone authority the tests use.
    static func create(root: Data, server: String, http: HTTP, setupToken: String) async throws
        -> SyntheticPhone
    {
        let seed = randomBytes(32)
        let acct = try await joinWithRoot(
            root: root, server: server, http: http, setupToken: setupToken, kind: .client,
            deviceSeed: seed)
        let api = try Api(server: server, http: http)
        let token = try await login(api: api, account: acct)
        let (fixture, genesis) = try TestSecurity.initialize(acct)
        var request = PBPinSecurityDomainRequest()
        request.genesisState = genesis
        _ = try await api.client.pinSecurityDomain(
            request: request, headers: Api.headers(token: token)
        ).value()
        guard let record = try fixture.store(acct.deviceId)?.readSecurity() else {
            throw TestFailure("synthetic phone record missing")
        }
        var leaseRequest=PBPutSecurityStateRequest();leaseRequest.lease=try record.lease!.bytes()
        _ = try await api.client.putSecurityState(request:leaseRequest,headers:Api.headers(token:token)).value()
        return SyntheticPhone(
            account: acct, accountPublicKey: try Keys.derive(root: root).account.publicKey,
            pin: record.pin, server: server, http: http, api: api, token: token, fixture: fixture)
    }

    func approve(_ seed: Data, kind: PBDeviceKind, pairingID: String) throws -> (Data, Data) {
        let candidate = try SigningKey(seed: seed).publicKey
        return try makePairApproval(
            account: account, pairingId: pairingID, candidate: candidate, kind: kind,
            expires: Int64(Date().timeIntervalSince1970 * 1000) + 5 * 60_000)
    }

    /// Adds a device whose Ed/X private keys are known here, registers it with
    /// a phone-signed approval, and returns its account and stored record.
    func enroll(seed: Data, kind: PBDeviceKind) async throws -> (Account, DeviceSecurity.Record) {
        let reply = try fixture.call(
            "enroll", ["Seed": seed.base64EncodedString(), "Kind": kind.rawValue])
        guard let raw = reply["Record"] else { throw TestFailure("missing synthetic record") }
        let record = try TestSecurity.record(JSONSerialization.data(withJSONObject: raw))
        try await upload(fixture.bytes(reply, "State"))
        let pairingID = randomHex(16)
        let (approval, signature) = try approve(seed, kind: kind, pairingID: pairingID)
        let acct = try await registerJoined(
            server: server, http: http, deviceSeed: seed, accountPublicKey: accountPublicKey,
            kind: kind,
            enrollment: Enrollment(
                seed: seed, pairingId: pairingID, approval: approval, signature: signature))
        fixture.install(record)
        return (acct, record)
    }

    /// Adds a device whose keys stay with the joiner (the pairing test), and
    /// returns the whole chain for the encrypted pairing payload.
    func enrollExternal(edPublic: Data, xPublic: Data, kind: PBDeviceKind) async throws -> (
        state: Data, chain: [(state: Data, recovery: Data)]
    ) {
        let reply = try fixture.call(
            "enroll",
            [
                "EdPublic": edPublic.base64EncodedString(),
                "XPublic": xPublic.base64EncodedString(), "Kind": kind.rawValue,
            ])
        let state = try fixture.bytes(reply, "State")
        return (state, try await chain())
    }

    /// Uploads a state the pairing approved. Doing this before the joiner has
    /// fetched its payload would cancel the pairing on an honest relay.
    func commit(_ state: Data) async throws {
        try await upload(state)
    }

    func chain() async throws -> [(state: Data, recovery: Data)] {
        let reply = try fixture.call("chain")
        guard let states = reply["States"] as? [String],
            let proofs = reply["Proofs"] as? [Any], states.count == proofs.count
        else { throw TestFailure("malformed synthetic chain") }
        var out: [(Data, Data)] = []
        for (i, raw) in states.enumerated() {
            guard let state = Data(base64Encoded: raw) else { throw TestFailure("bad state") }
            let recovery =
                (proofs[i] as? String).flatMap { Data(base64Encoded: $0) } ?? Data()
            out.append((state, recovery))
        }
        return out
    }

    func remove(_ targets: [String]) async throws {
        let reply = try fixture.call("remove", ["Targets": targets])
        try await upload(fixture.bytes(reply, "State"))
    }

    private func upload(_ state: Data) async throws {
        var request = PBPutSecurityStateRequest()
        var value = PBSecurityState()
        value.state = state
        request.states = [value]
        let chainReply=try fixture.call("chain")
        request.lease = try fixture.bytes(chainReply,"Lease")
        _ = try await api.client.putSecurityState(
            request: request, headers: Api.headers(token: token)
        ).value()
    }
}

func randomHex(_ bytes: Int) -> String { randomBytes(bytes).hex }

/// One pairing started by the synthetic phone. The phone side has no QR UI
/// in these tests; it uses the same server calls the Android app does.
final class PhonePairing: @unchecked Sendable {
    let phone: SyntheticPhone
    let pairingID: String
    let code: String
    let link: PairLink
    private let commit: Data
    private let trust: Data
    private let ephemeral: PairKey
    private let expiresMs: Int64
    private var revealed: PBGetPairingResponse?
    private var sent: PBApprovePairingRequest?

    private init(
        phone: SyntheticPhone, pairingID: String, code: String, commit: Data, trust: Data,
        ephemeral: PairKey, expiresMs: Int64
    ) {
        self.phone = phone
        self.pairingID = pairingID
        self.code = code
        self.commit = commit
        self.trust = trust
        self.ephemeral = ephemeral
        self.expiresMs = expiresMs
        link = PairLink(code: code, server: phone.server, commit: commit)
    }

    static func start(_ phone: SyntheticPhone) async throws -> PhonePairing {
        let key = try PairKey()
        let trust = try DeviceSecurity.pairTrustDescriptor(
            phone.pin, approver: phone.account.deviceId)
        let commit = try DeviceSecurity.pairDomainCommit(ephemeral: key.publicKey, trust: trust)
        var request = PBCreatePairingRequest()
        request.commit = commit
        let created = try await phone.api.pairing.createPairing(
            request: request, headers: Api.headers(token: phone.token)
        ).value()
        return PhonePairing(
            phone: phone, pairingID: created.pairingID, code: created.code, commit: commit,
            trust: trust, ephemeral: key, expiresMs: created.expiresMs)
    }

    /// Waits for a device to join, reveals this phone's ephemeral key and
    /// trust descriptor, and returns the emoji and the joining device.
    func reveal(sleep: Sleeper = realSleep) async throws -> (emoji: [String], response: PBGetPairingResponse)
    {
        var ask = PBGetPairingRequest()
        ask.pairingID = pairingID
        let until = Date().addingTimeInterval(10)
        var st: PBGetPairingResponse
        while true {
            st = try await phone.api.pairing.getPairing(
                request: ask, headers: Api.headers(token: phone.token)
            ).value()
            if st.state == .joined { break }
            if st.state == .canceled { throw TestFailure("pairing canceled") }
            if Date() > until { throw TestFailure("pairing did not join") }
            try await sleep(50)
        }
        guard st.devicePublicKey.count == 32, st.deviceHpkePublicKey.count == 32 else {
            throw TestFailure("bad candidate identity")
        }
        var reveal = PBRevealPairingRequest()
        reveal.pairingID = pairingID
        reveal.initiatorPublic = ephemeral.publicKey
        reveal.trust = trust
        _ = try await phone.api.pairing.revealPairing(
            request: reveal, headers: Api.headers(token: phone.token)
        ).value()
        let transcript = Pairing.domainTranscript(
            pairingId: pairingID, initiatorPublic: ephemeral.publicKey,
            joinerPublic: st.joinerPublic, devicePublic: st.devicePublicKey,
            hpkePublic: st.deviceHpkePublicKey, joinerKind: st.joinerKind.rawValue, trust: trust)
        let secrets = try ephemeral.derive(otherPublic: st.joinerPublic, transcript: transcript)
        revealed = st
        return (secrets.emoji, st)
    }

    /// Adds the joined candidate to the signed chain and hands over the chain
    /// in the pairing payload. Safe to call again with the same approval.
    func approve(sleep: Sleeper = realSleep) async throws {
        if let sent {
            _ = try await phone.api.pairing.approvePairing(
                request: sent, headers: Api.headers(token: phone.token)
            ).value()
            _ = try? await waitDelivered(sleep: sleep)
            return
        }
        guard let st = revealed else { throw TestFailure("pairing was not revealed") }
        let kind = st.joinerKind
        let added = try await phone.enrollExternal(
            edPublic: st.devicePublicKey, xPublic: st.deviceHpkePublicKey, kind: kind)
        let chain = added.chain
        // The documented phone order: sign the joining state, upload it, then
        // hand the chain over in the encrypted pairing payload.
        try await phone.commit(added.state)
        let transcript = Pairing.domainTranscript(
            pairingId: pairingID, initiatorPublic: ephemeral.publicKey,
            joinerPublic: st.joinerPublic, devicePublic: st.devicePublicKey,
            hpkePublic: st.deviceHpkePublicKey, joinerKind: kind.rawValue, trust: trust)
        let secrets = try ephemeral.derive(otherPublic: st.joinerPublic, transcript: transcript)
        let (approval, signature) = try makePairApproval(
            account: phone.account, pairingId: pairingID, candidate: st.devicePublicKey,
            kind: kind, expires: expiresMs)
        var payload = PBPairingPayload()
        payload.server = phone.server
        payload.approval = approval
        payload.approvalSignature = signature
        payload.accountPublicKey = phone.accountPublicKey
        payload.lease = try phone.fixture.bytes(phone.fixture.call("chain"),"Lease")
        payload.securityStates = chain.map {
            var value = PBSecurityState()
            value.state = $0.state
            value.recovery = $0.recovery
            return value
        }
        var header = PBHeader()
        header.kind = .pairing
        header.requestID = pairingID
        header.sender = phone.account.deviceId
        var request = PBApprovePairingRequest()
        request.pairingID = pairingID
        request.payload = try Envelopes.seal(key: secrets.key, header: header, message: payload)
        request.approval = approval
        request.approvalSignature = signature
        sent = request
        _ = try await phone.api.pairing.approvePairing(
            request: request, headers: Api.headers(token: phone.token)
        ).value()
        _ = try? await waitDelivered(sleep: sleep)
    }

    /// Waits for the candidate to confirm receipt, as the app's flow does.
    /// Returns false rather than failing when the acknowledgement was lost.
    private func waitDelivered(sleep: Sleeper) async throws -> Bool {
        var ask = PBGetPairingRequest()
        ask.pairingID = pairingID
        let until = Date().addingTimeInterval(10)
        while true {
            let done = try await phone.api.pairing.getPairing(
                request: ask, headers: Api.headers(token: phone.token)
            ).value()
            if done.state == .delivered { return true }
            if done.state == .canceled { return false }
            if Date() > until { return false }
            try await sleep(50)
        }
    }
}

/// Registers a new device of the account with root key `root` and logs nothing secret.
func joinWithRoot(
    root: Data, server: String, http: HTTP, setupToken: String = "", kind: PBDeviceKind = .client,
    now: Int64 = Int64(Date().timeIntervalSince1970 * 1000), enrollment: Enrollment? = nil,
    deviceSeed: Data? = nil
) async throws -> Account {
    let keys = try Keys.derive(root: root)
    let seed = deviceSeed ?? enrollment?.seed ?? randomBytes(32)
    let device = try SigningKey(seed: seed)
    if let enrollment { try verifyPairApproval(enrollment, root: root, kind: kind, now: now) }
    var reg = PBDeviceRegistration()
    reg.accountPublicKey = keys.account.publicKey
    reg.devicePublicKey = device.publicKey
    reg.kind = kind
    reg.createdMs = now
    let regBytes: Data = try reg.serializedData()
    let api = try Api(server: server, http: http)
    var req = PBRegisterDeviceRequest()
    req.protocolVersion = protocolVersion
    req.registration = regBytes
    req.signature = keys.account.sign(registerMessage(regBytes))
    req.setupToken = setupToken
    if let enrollment {
        req.approval = enrollment.approval
        req.approvalSignature = enrollment.signature
    }
    let res = try await api.auth.registerDevice(request: req).value()
    if res.deviceID != deviceId(publicKey: device.publicKey) {
        throw UserError("server 返回了意外的设备 ID")
    }
    return Account(
        server: api.server, root: root, deviceSeed: seed, deviceId: res.deviceID,
        accountId: deviceId(publicKey: keys.account.publicKey))
}

/// Registers a device joining an existing account. The phone-signed approval
/// is the authorization; the account root key is not asked for.
func registerJoined(
    server: String, http: HTTP, deviceSeed: Data, accountPublicKey: Data,
    kind: PBDeviceKind = .client, enrollment: Enrollment,
    now: Int64 = Int64(Date().timeIntervalSince1970 * 1000)
) async throws -> Account {
    guard accountPublicKey.count == 32 else { throw CryptoError("account public key must be 32 bytes") }
    let accountId = deviceId(publicKey: accountPublicKey)
    try verifyPairApproval(
        enrollment, accountId: accountId, deviceSeed: deviceSeed, kind: kind, now: now)
    let device = try SigningKey(seed: deviceSeed)
    var reg = PBDeviceRegistration()
    reg.accountPublicKey = accountPublicKey
    reg.devicePublicKey = device.publicKey
    reg.kind = kind
    reg.createdMs = now
    let regBytes: Data = try reg.serializedData()
    let api = try Api(server: server, http: http)
    var req = PBRegisterDeviceRequest()
    req.protocolVersion = protocolVersion
    req.registration = regBytes
    req.approval = enrollment.approval
    req.approvalSignature = enrollment.signature
    let res = try await api.auth.registerDevice(request: req).value()
    if res.deviceID != deviceId(publicKey: device.publicKey) {
        throw UserError("server 返回了意外的设备 ID")
    }
    return Account(
        server: api.server, root: Data(), deviceSeed: deviceSeed, deviceId: res.deviceID,
        accountId: accountId)
}

func makePairApproval(account: Account, pairingId: String, candidate: Data, kind: PBDeviceKind, expires: Int64) throws -> (Data, Data) {
    let signer = try SigningKey(seed: account.deviceSeed)
    var a = PBPairApproval()
    a.accountID = deviceId(publicKey: try Keys.derive(root: account.root).account.publicKey)
    a.pairingID = pairingId; a.approverDeviceID = account.deviceId
    a.approverPublicKey = signer.publicKey; a.candidateDevicePublicKey = candidate
    a.candidateKind = kind; a.expiresMs = expires; a.nonce = randomBytes(16)
    let raw = try a.serializedData()
    return (raw, signer.sign(Data("remote-agent/v1/pair-approval\0".utf8) + raw))
}
