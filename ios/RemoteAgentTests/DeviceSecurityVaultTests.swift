import Foundation
import RACore
import Testing

@testable import RemoteAgent

/// Public synthetic vectors, never the user's Keychain service or account.
@Suite struct DeviceSecurityVaultTests {
    let vault = Vault(service: "io.github.lewin671.remoteagent.security-tests.\(UUID().uuidString)")
    private func record() throws -> DeviceSecurity.Record {
        let state = try DeviceSecurity.parseState(
            Data(
                base64Encoded:
                    "AAABtAAAAAIAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYREREREREREREREREREREREREREREREREREREREREREQAAACA0OGNjYTk3Zjg5OTNmZmFlYmNhYzk3MjhkN2Y5NGY3MduZX+JRadFByrm7upK6oB+fLh7OffTLKsBRkPN/zB+dEyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HKqkl9YtkzHiMuG0FTcFeLsH6OPWyeLGMjiIt4GObCowgAAAAAAAAACAAAAAAAAAAIAAAACAAAAIDI0YWEyYTU1ODllZGNiNTdmZWEwYmUzNTUyZjA2NWRlSAdaWX5yGhVuLgeZ3lzAxTJNxufq8c3UYlCGjsUyFd1Z2SJUc0Ue//5rNtvK79v3sYld5iCEUJp/W1i/AdBkGAAAAAIAAAAgNDhjY2E5N2Y4OTkzZmZhZWJjYWM5NzI4ZDdmOTRmNzGIS4hX9OqhYTxhUE2zTUvq80ZReg4x3jzd1Nm0IB2dCw+qaE7SiGe5f0pqLe5d+M6XTna3AY4/IqHEzyZ4Vw8gAAAAAQAAAAAAAAACAAAAIDI0YWEyYTU1ODllZGNiNTdmZWEwYmUzNTUyZjA2NWRlAAAAIBrcHM12/2sawLI/GOJC5dG2X5aUrJlzeBGrR20R8SAgAAAAMMYznI83qZkHBoN/97w0ZUkeFxrbQ1cJ5SPV1BNBbzfmqLdrN3rRRqZ8dwCZxz8XDgAAACA0OGNjYTk3Zjg5OTNmZmFlYmNhYzk3MjhkN2Y5NGY3MQAAACAcPttHOpm95eDd52A0QRc+Zr01E+MRKmHNLzmJAIRafwAAADCKEDzlhgIBD8aXwIN+K5Nw1qJSdn53sHMvAW0lgFnLXh7YjGTbNnoRdO9gVXtJ6R8AAABAK+MTq6jvnrE7nWP32ALKDtayJL9LP0gth/oOwtLIhu7BAIJZqZtbBj8j8B0h0eDJspThfW0XlzYGvAtQ8bxMCw=="
            )!)
        let c = state.core
        let pin = DeviceSecurity.Pin(
            account: c.account, domain: c.domain,
            genesis: Data(base64Encoded: "qpJfWLZMx4jLhtBU3BXi7B+jj1snixjI4iLeBjmwqMI=")!,
            authorityID: c.authorityID, authorityEd: c.authorityEd, authorityX: c.authorityX)
        return try DeviceSecurity.Record(
            pin: pin, state: state, memberID: "48cca97f8993ffaebcac9728d7f94f71",
            xPrivate: Data(repeating: 0x22, count: 32))
    }
    @Test func counterAndNotifyRepairUseTheProtectedCurrentState() throws {
        defer { try? vault.clear() }
        let initial = try record()
        try vault.initializeSecurity(initial)
        let hash = try DeviceSecurity.stateHash(initial.state)
        #expect(try vault.reserveSecurity(expectedState: hash).counter == 1)
        let reopened = Vault(service: vault.service)
        #expect(try reopened.readSecurity().counter == 1)
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
}
