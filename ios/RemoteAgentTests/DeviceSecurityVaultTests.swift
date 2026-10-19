import CryptoKit
import Foundation
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
                    "AAABPAAAAAMAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYFAvSJtM6+UuJeI4qr8ib0xXPPaLAbM69N+g2DKYc77AAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YqCapfR6Z1mAL/lV+NwtKhSlyZ0jvpf4ZBJ/+Tg0VaTwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAABAAABi8/laAAAAAGL31h8AAAAAAEAAAAgMTBiYTY4MmM4YWQxMzUxMzk3MWU4YjU2ODgxYWFiOGLQSrIydCu0qzoTaL1GFeTm0CJKtxoBa6+FIKMyyXeHN6LXoSEACN+Ts6+ET9lrEQhsbMWDxE9laEnxAsj7l+JvAAAAAgAAAAAAAAABAAAAIDEwYmE2ODJjOGFkMTM1MTM5NzFlOGI1Njg4MWFhYjhiAAAAIHRMRaZRB4R3QDZg6KUqLUo5AahTX7O80LboH1kTLWZrAAAAMO7ILr4QzNoBQqXTiAae4H5kx9GvcwZifYB62aIBpgvA7m6d/v64vRi5eJa3iM0ytQAAAAAAAABAvacEuJNHvGNfLI/vWsuwDcyHFMnDinTQOIsUnAPmk4ipgxUOvElPAk3ERQ7oQREkjb58nveXEumNUUTjTYEODg=="
            )!)
        let pin = DeviceSecurity.Pin(
            account: "0123456789abcdef0123456789abcdef",
            domain: Data(
                hex: "0502f489b4cebe52e25e238aabf226f4c573cf68b01b33af4dfa0d8329873bec")!,
            genesis: Data(
                hex: "62eebdbe6f373799fe13c648eeb1d0d076fb9a21d7586c3fc0803be5032278ff")!)
        let x = try Curve25519.KeyAgreement.PrivateKey(
            rawRepresentation: Data(
                hex: "3e29be55fb1a4d8832fa0a317d609a3fe5a74730d321d0e7085b2de2de386ac7")!)
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
}
