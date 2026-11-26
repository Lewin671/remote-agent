import Foundation
import RAProto
import SwiftProtobuf

@testable import RACore

private let testRegisterContext = "remote-agent/v1/register\u{0}"
private let testPairApprovalContext = "remote-agent/v1/pair-approval\u{0}"

/// A synthetic authority phone backed by the Go `securityfixture` process and `MemorySecurityStoreV4`.
final class SyntheticPhone: @unchecked Sendable {
    let account: Account
    let accountPublicKey: Data
    let server: String
    let http: HTTP
    let api: TestApi
    let token: String
    let fixture: TestSecurity

    private init(
        account: Account,
        accountPublicKey: Data,
        server: String,
        http: HTTP,
        api: TestApi,
        token: String,
        fixture: TestSecurity
    ) {
        self.account = account
        self.accountPublicKey = accountPublicKey
        self.server = server
        self.http = http
        self.api = api
        self.token = token
        self.fixture = fixture
    }

    static func create(
        root: Data,
        server: String,
        http: HTTP,
        setupToken: String,
        recoverySeed: Data? = nil
    ) async throws -> SyntheticPhone {
        let accountPub = try testEdPublicKey(root)
        let accountId = deviceId(publicKey: accountPub)
        let edSeed = randomBytes(keySize)
        let edPub = try testEdPublicKey(edSeed)
        let devId = deviceId(publicKey: edPub)
        var reg = PBDeviceRegistration()
        reg.accountPublicKey = accountPub
        reg.devicePublicKey = edPub
        reg.kind = .client
        reg.createdMs = Int64(Date().timeIntervalSince1970 * 1000)
        let regBytes = try reg.serializedData()
        let sig = try testEdSign(root, Data(testRegisterContext.utf8) + regBytes)
        let api = try TestApi(server: server, http: http)
        var regReq = PBRegisterDeviceRequest()
        regReq.protocolVersion = relayProtocolVersion
        regReq.registration = regBytes
        regReq.signature = sig
        regReq.setupToken = setupToken
        let regRes = try await api.auth.registerDevice(request: regReq).testValue()
        guard regRes.deviceID == devId else { throw TestFailure("registered deviceId mismatch") }

        let phoneAccount = Account(server: api.server, deviceId: devId, accountId: accountId)
        let (fixture, genesis) = try TestSecurity.initialize(
            phoneAccount,
            edSeed: edSeed,
            rootSeed: root,
            recovery: recoverySeed
        )
        guard let store = fixture.store(devId) else { throw TestFailure("phone store missing") }
        let tok = try await testLoginWithStore(api: api, deviceId: devId, store: store).token
        var pinReq = PBPinSecurityDomainRequest()
        pinReq.genesisState = genesis
        _ = try await api.client.pinSecurityDomain(request: pinReq, headers: TestApi.headers(token: tok)).testValue()
        let rec = try fixture.record(member: devId)
        var leaseReq = PBPutSecurityStateRequest()
        leaseReq.lease = rec.lease
        _ = try await api.client.putSecurityState(request: leaseReq, headers: TestApi.headers(token: tok)).testValue()
        fixture.syncStoresForAll()
        return SyntheticPhone(
            account: phoneAccount,
            accountPublicKey: accountPub,
            server: api.server,
            http: http,
            api: api,
            token: tok,
            fixture: fixture
        )
    }

    @discardableResult
    func enroll(seed: Data, kind: PBDeviceKind) async throws -> (Account, FixtureRecord) {
        let authoritySeed = fixture.deviceSeeds[account.deviceId] ?? Data()
        let authorityPub = try testEdPublicKey(authoritySeed)
        let (rec, state) = try fixture.enroll(seed: seed, kind: kind)
        try await upload(state, lease: rec.lease)

        let now = Int64(Date().timeIntervalSince1970 * 1000)
        let candPub = try testEdPublicKey(seed)
        var reg = PBDeviceRegistration()
        reg.accountPublicKey = accountPublicKey
        reg.devicePublicKey = candPub
        reg.kind = kind
        reg.createdMs = now
        let regBytes = try reg.serializedData()

        var approval = PBPairApproval()
        approval.accountID = account.accountId
        approval.pairingID = randomBytes(16).hex
        approval.approverDeviceID = account.deviceId
        approval.approverPublicKey = authorityPub
        approval.candidateDevicePublicKey = candPub
        approval.candidateKind = kind
        approval.expiresMs = now + 10 * 60_000
        approval.nonce = randomBytes(16)
        let approvalBytes = try approval.serializedData()
        let approvalSig = try testEdSign(authoritySeed, Data(testPairApprovalContext.utf8) + approvalBytes)

        var req = PBRegisterDeviceRequest()
        req.protocolVersion = relayProtocolVersion
        req.registration = regBytes
        req.approval = approvalBytes
        req.approvalSignature = approvalSig
        let res = try await api.auth.registerDevice(request: req).testValue()
        guard res.deviceID == rec.member else { throw TestFailure("enrolled deviceId mismatch") }

        try fixture.seedMemberStore(rec, edSeed: seed)
        fixture.syncStoresForAll()
        return (Account(server: server, deviceId: rec.member, accountId: account.accountId), rec)
    }

    func remove(_ targets: [String]) async throws {
        let nextState = try fixture.remove(targets: targets)
        let rec = try fixture.record(member: account.deviceId)
        try await upload(nextState, lease: rec.lease)
        fixture.syncStoresForAll()
    }

    private func upload(_ state: Data, lease: Data) async throws {
        var req = PBPutSecurityStateRequest()
        req.lease = lease
        var s = PBSecurityState()
        s.state = state
        req.states = [s]
        _ = try await api.client.putSecurityState(request: req, headers: TestApi.headers(token: token)).testValue()
    }
}
