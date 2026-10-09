import Foundation
import Testing

@testable import RACore

/// The protocol version is one number kept in four places; this is the one check that they agree.
@Suite struct VersionTests {
    private func number(in path: String, matching pattern: String) throws -> UInt32 {
        let text = try String(contentsOf: repoRoot.appendingPathComponent(path), encoding: .utf8)
        let match = try #require(text.firstMatch(of: try Regex(pattern)), "no version in \(path)")
        return try #require(UInt32(String(match.output[1].substring ?? "")))
    }

    @Test func speaksTheProtocolVersionOfTheOtherEnds() throws {
        #expect(try number(in: "internal/protocol/protocol.go", matching: #"(?m)^const Version = (\d+)$"#) == protocolVersion)
        #expect(try number(in: "web/src/protocol.ts", matching: #"PROTOCOL_VERSION\s*=\s*(\d+)"#) == protocolVersion)
        #expect(try number(
            in: "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Version.kt",
            matching: #"const val PROTOCOL_VERSION = (\d+)"#) == protocolVersion)
    }

    @Test func keepsTheEnvelopeVersionOfStoredData() throws {
        #expect(try number(in: "internal/e2e/e2e.go", matching: #"(?m)^\s*(?:const )?Version\s*=\s*(\d+)"#) == envelopeVersion)
    }
}
