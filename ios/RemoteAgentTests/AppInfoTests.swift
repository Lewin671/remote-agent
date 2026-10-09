import Foundation
import Testing

@testable import RemoteAgent

@Suite struct AppInfoTests {
    @Test func theBuildCarriesTheSharedVersionAndItsCommit() throws {
        // The simulator reads the Mac's files: version.properties is two folders above this one.
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let properties = try String(contentsOf: root.appending(path: "version.properties"), encoding: .utf8)
        let version = try #require(properties.split(separator: "\n").first { $0.hasPrefix("version=") }?.dropFirst("version=".count))
        #expect(AppInfo.version == String(version))
        #expect(AppInfo.commit != "unknown" && !AppInfo.commit.isEmpty)
        #expect(AppInfo.versionLine.contains(AppInfo.version) && AppInfo.versionLine.contains(AppInfo.commit))
    }
}
