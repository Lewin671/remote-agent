import Foundation
import Testing

@testable import RACore

/// Both protocol versions (relay and peer) are kept in five places; this check verifies they agree.
@Suite(.timeLimit(.minutes(2))) struct VersionTests {
    private func number(in path: String, matching pattern: String) throws -> UInt32 {
        let text = try String(contentsOf: repoRoot.appendingPathComponent(path), encoding: .utf8)
        let match = try #require(text.firstMatch(of: try Regex(pattern)), "no version in \(path)")
        return try #require(UInt32(String(match.output[1].substring ?? "")))
    }

    @Test func speaksThePeerProtocolVersionOfTheOtherEnds() throws {
        #expect(try number(in: "internal/protocol/protocol.go", matching: #"(?m)^const PeerVersion = (\d+)$"#) == peerProtocolVersion)
        #expect(try number(in: "clientcore/sync/types.go", matching: #"(?m)^\s*PeerVersion\s+uint32\s*=\s*(\d+)$"#) == peerProtocolVersion)
        #expect(try number(in: "web/src/protocol.ts", matching: #"(?m)^export const PEER_PROTOCOL_VERSION = (\d+);$"#) == peerProtocolVersion)
        #expect(try number(
            in: "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Version.kt",
            matching: #"(?m)^const val PEER_PROTOCOL_VERSION = (\d+)$"#) == peerProtocolVersion)
    }

    @Test func speaksTheRelayProtocolVersionOfTheOtherEnds() throws {
        #expect(try number(in: "internal/relayproto/relayproto.go", matching: #"(?m)^const Version = (\d+)$"#) == relayProtocolVersion)
        #expect(try number(in: "clientcore/sync/types.go", matching: #"(?m)^\s*RelayVersion\s+uint32\s*=\s*(\d+)$"#) == relayProtocolVersion)
        #expect(try number(in: "web/src/protocol.ts", matching: #"(?m)^export const RELAY_PROTOCOL_VERSION = (\d+);$"#) == relayProtocolVersion)
        #expect(try number(
            in: "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Version.kt",
            matching: #"(?m)^const val RELAY_PROTOCOL_VERSION = (\d+)$"#) == relayProtocolVersion)
    }

    @Test func keepsTheEnvelopeVersionOfStoredData() throws {
        #expect(try number(in: "internal/e2e/e2e.go", matching: #"(?m)^\s*(?:const )?Version\s*=\s*(\d+)"#) == envelopeVersion)
    }
}
