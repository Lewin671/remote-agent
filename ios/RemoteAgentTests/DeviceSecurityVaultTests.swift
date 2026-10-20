import CryptoKit
import Foundation
import RAProto
import Security
import RACore
import Testing

@testable import RemoteAgent

/// Public synthetic vectors, never the user's Keychain service or account.
@Suite struct DeviceSecurityVaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.security-tests.\(UUID().uuidString)")
    private func record() throws -> DeviceSecurity.Record {
        // The protocol-10 genesis vector (internal/deviceauth/testdata/domain-v3.json).
        let state = try DeviceSecurity.parseState(
            Data(
                base64Encoded:
                    "AAABNAAAAAMAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYFAvSJtM6+UuJeI4qr8ib0xXPPaLAbM69N+g2DKYc77AAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YqCapfR6Z1mAL/lV+NwtKhSlyZ0jvpf4ZBJ/+Tg0VaTwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAABAAABi8/laAAAAAABAAAAIDEwYmE2ODJjOGFkMTM1MTM5NzFlOGI1Njg4MWFhYjhi0EqyMnQrtKs6E2i9RhXk5tAiSrcaAWuvhSCjMsl3hzcMy1pMEx1AAEdimAVZF13nwrO9ybDBSzGI+pbT8c5JDQAAAAIAAAAAAAAAAQAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YgAAACD0tG7uoaqb8Zw8ZNseegb3LyVgCArL5O0QbCq+aBxaQQAAADA2DpES664liUfvMhj8qTr5bSydcmStTCgB502c9+TqfXeZjA5jV01FHa0L5i/mWmoAAAAAAAAAQFf1kKriAgtRH7OT8xnd6BiXDqO5OPTH6tMYZFsIEMZA2wL+qiBSxL24T8DFjzF0hSNwtNRuWgDX3iwRdn1tIw0="
            )!)
        let pin = DeviceSecurity.Pin(
            account: "0123456789abcdef0123456789abcdef",
            domain: Data(
                hex: "0502f489b4cebe52e25e238aabf226f4c573cf68b01b33af4dfa0d8329873bec")!,
            genesis: Data(
                hex: "52fb306d06ee6fe8fa2e3165ad3a10650fdd46288a35dbd38d0f7a37bf69437f")!)
        let x = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: Data(
                hex: "7268259908c56878cd0168407c6940971ddc77b6905e89bb2be57325683ecadd")!)
        let secret = try DeviceSecurity.openGrant(
            pin, state, recipient: state.core.authorityID, x: x)
        return try DeviceSecurity.Record(
            pin: pin, states: [1: state], memberID: state.core.authorityID,
            xPrivate: x.rawRepresentation, member: true, secret: secret)
    }
    @Test func timeFloorAndNotifyRepairUseTheProtectedCurrentState() throws {
        defer { try? vault.clear() }
        let initial = try record()
        try vault.initializeSecurity(initial)
        let hash = try DeviceSecurity.stateHash(initial.state)
        var next = initial
        next.floor = 4096
        _ = try vault.writeSecurity(expectedHead: hash, next)
        let reopened = Vault(service: vault.service)
        // The persisted time floor survives a reopen.
        #expect(try reopened.readSecurity().floor == 4096)
        try vault.write(.notifyKey, Data([0]))
        _ = try reopened.readSecurity()
        let notifyBytes = try #require(try vault.notifyKey())
        let repaired = try DeviceSecurity.NotifyRecord.parse(notifyBytes)
        #expect(try DeviceSecurity.stateHash(repaired.state) == hash)
        #expect(try repaired.bytes() != initial.bytes())
    }
    @Test func staleSameHeadWritesPreserveTheCommittedTimeFloor() throws {
        defer { try? vault.clear() }
        let initial = try record()
        try vault.initializeSecurity(initial)
        let hash = try DeviceSecurity.stateHash(initial.state)
        var newer = initial; newer.floor = 9000; newer.floorSaved = 9000
        _ = try vault.writeSecurity(expectedHead: hash, newer)
        var stale = initial; stale.floor = 1000
        _ = try vault.writeSecurity(expectedHead: hash, stale)
        #expect(try vault.readSecurity().floor == 9000)
    }
    @Test func anErasedRegistrationReceiptCannotRestoreAccountKeys() throws {
        defer { try? vault.clear() }
        let stored = try record()
        let account = Account(server: "https://relay.example.com", root: Data(), deviceSeed: Data(repeating: 2, count: 32), deviceId: stored.memberID, accountId: stored.pin.account)
        #expect(throws: DeviceSecurity.Failure.self) {
            try vault.saveRegistrationBootstrap(account, record: stored)
        }
        #expect(try vault.load() == nil)
        #expect(try vault.read(.securityState) == nil)
    }
    @Test func aCorruptExistingRecordCannotBecomeFreshSetup() throws {
        defer { try? vault.clear() }
        let parts = AppModel.Parts()
        let account = Account(
            server: "https://relay.example.com", root: Data(repeating: 1, count: 32),
            deviceSeed: Data(repeating: 2, count: 32), deviceId: "00112233445566778899aabbccddeeff")
        #expect(try !parts.securityReady(vault, account))
        try vault.write(.securityState, Data([0]))
        #expect(throws: (any Error).self) { try vault.readSecurity() }
        #expect(throws: (any Error).self) { try parts.securityReady(vault, account) }
        #expect(throws: DeviceSecurity.Failure.self) { try vault.initializeSecurity(record()) }
        #expect(try vault.read(.securityState) == Data([0]))
    }

    @Test func aPairingJoinedAccountWithoutARootStillOpens() throws {
        defer { try? vault.clear() }
        let stored = try record()
        try vault.initializeSecurity(stored)
        let account = Account(
            server: "https://relay.example.com", root: Data(),
            deviceSeed: Data(repeating: 2, count: 32), deviceId: stored.memberID,
            accountId: stored.pin.account)
        // A pairing payload carries the account key, not the root; the next
        // launch must not try to derive it.
        #expect(try AppModel.Parts().securityReady(vault, account))
        let wrong = Account(
            server: account.server, root: Data(), deviceSeed: account.deviceSeed,
            deviceId: account.deviceId, accountId: "ffeeddccbbaa99887766554433221100")
        #expect(throws: DeviceSecurity.Failure.self) {
            try AppModel.Parts().securityReady(vault, wrong)
        }
    }
    @Test func publicProfileSurvivesPairingReceiptAndUsesUnlockedLocalKeychainProtection() throws {
        defer { try? vault.clear() }
        let root = Data(repeating: 31, count: 32)
        let pub = try Keys.derive(root: root).account.publicKey
        let signer = try SigningKey(seed: Data(repeating: 1, count: 32))
        let seed = Data(repeating: 2, count: 32), x = Data(repeating: 12, count: 32)
        let incoming = try Authority.newMember(signer: SigningKey(seed: seed), xPrivate: x, kind: .client)
        let a = Authority()
        let g = try a.genesis(account: deviceId(publicKey: pub), domain: Data(repeating: 50, count: 32),
            member: Authority.newMember(signer: signer, xPrivate: Data(repeating: 11, count: 32), kind: .client),
            recoveryEd: signer.publicKey, secret: Data(repeating: 40, count: 32), signer: signer, now: 1000)
        let secret = Data(repeating: 41, count: 32)
        let next = try a.pair(parent: g, candidate: incoming, oldSecret: Data(repeating: 40, count: 32), newSecret: secret, signer: signer, now: 1001)
        let pin = try DeviceSecurity.pin(for: g)
        let record = try DeviceSecurity.Record(pin: pin, states: [1: g, 2: next], memberID: incoming.id,
            xPrivate: x, member: true, secret: secret)
        let account = Account(server: "https://relay.example.com", root: Data(), deviceSeed: seed, deviceId: incoming.id, accountId: pin.account)
        var registration = PBDeviceRegistration()
        registration.accountPublicKey = pub; registration.devicePublicKey = incoming.ed; registration.kind = .client
        var request = PBRegisterDeviceRequest()
        request.registration = try registration.serializedData()
        let approval = try makePairApproval(accountID: pin.account, pairingID: "0123456789abcdef0123456789abcdef",
            signer: signer, candidate: incoming.ed, kind: .client, expires: 300000)
        request.approval = approval.approval; request.approvalSignature = approval.signature
        let intent = try RegistrationIntent(account: account, record: record, request: request)
        try vault.saveRegistrationIntent(intent)
        try vault.saveRegistrationBootstrap(account, record: record)
        try vault.clearRegistrationIntent(intent)
        #expect(try vault.loadRegistrationIntent() == nil)
        let profile = try #require(try Vault(service: vault.service).accountPublicProfile(pin: pin))
        #expect(profile.publicKey == pub)
        let query: [CFString: Any] = [kSecClass: kSecClassGenericPassword, kSecAttrService: vault.service,
            kSecAttrAccount: profile.storageKey, kSecAttrSynchronizable: false, kSecReturnAttributes: true]
        var out: CFTypeRef?
        #expect(SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess)
        let attrs = try #require(out as? [CFString: Any])
        #expect(attrs[kSecAttrAccessible] as? String == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String)
        #expect((attrs[kSecAttrSynchronizable] as? NSNumber)?.boolValue != true)
        try vault.clear()
        #expect(try vault.accountPublicProfile(pin: pin) == nil)
    }

    @Test func creationAndRecoveryProfilesPersistSeparatelyAndRejectWrongBinding() throws {
        defer { try? vault.clear() }
        let root = Data(repeating: 31, count: 32)
        let pub = try Keys.derive(root: root).account.publicKey
        let signer = try SigningKey(seed: Data(repeating: 1, count: 32))
        let g = try Authority().genesis(account: deviceId(publicKey: pub), domain: Data(repeating: 50, count: 32),
            member: Authority.newMember(signer: signer, xPrivate: Data(repeating: 11, count: 32), kind: .client),
            recoveryEd: signer.publicKey, secret: Data(repeating: 40, count: 32), signer: signer, now: 1000)
        let created = try AccountPublicProfile.created(root: root, genesis: g)
        try vault.saveAccountPublicProfile(created, pin: created.pin)
        let recovered = try AccountPublicProfile.recovery(publicKey: pub, states: [g], proofs: [nil], genesisPrefix: created.pin.genesis.prefix(10))
        try vault.saveAccountPublicProfile(recovered, pin: created.pin)
        #expect(try vault.accountPublicProfile(pin: created.pin) == created)
        #expect(try vault.read(.account) == nil)
        #expect(try vault.read(.securityState) == nil)
        var wrong = created.pin; wrong.domain = Data(repeating: 51, count: 32)
        #expect(throws: (any Error).self) { try vault.saveAccountPublicProfile(created, pin: wrong) }
        #expect(throws: (any Error).self) { try vault.accountPublicProfile(pin: wrong) }
        try vault.writeNamed(created.storageKey, Data([0]))
        #expect(throws: (any Error).self) { try vault.accountPublicProfile(pin: created.pin) }
    }

}
