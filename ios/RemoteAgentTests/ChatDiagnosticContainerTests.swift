import Foundation
import RACore
import Testing

@Suite struct ChatDiagnosticContainerTests {
    @Test func theAppDeclaresLocalElapsedTimeMeasurements() throws {
        let url = try #require(Bundle.main.url(forResource: "PrivacyInfo", withExtension: "xcprivacy"))
        let plist = try #require(PropertyListSerialization.propertyList(from: Data(contentsOf: url), format: nil) as? [String: Any])
        let entries = try #require(plist["NSPrivacyAccessedAPITypes"] as? [[String: Any]])
        let boot = try #require(entries.first { $0["NSPrivacyAccessedAPIType"] as? String == "NSPrivacyAccessedAPICategorySystemBootTime" })
        #expect(boot["NSPrivacyAccessedAPITypeReasons"] as? [String] == ["35F9.1"])
    }

    @Test func defaultFilesLiveInsideThisAppsDataContainer() throws {
        let files = ChatStallFiles()
        let home = URL(fileURLWithPath: NSHomeDirectory(), isDirectory: true)
        let expected = home.appendingPathComponent("Library/Application Support/Diagnostics", isDirectory: true)
        #expect(files.directory.standardizedFileURL == expected.standardizedFileURL)
        let wall = UInt64(Date().timeIntervalSince1970 * 1000)
        var written: URL?
        var recorder = ChatStallRecorder(ring: ChatDiagnosticRing(capacity: 8, timeOrigin: 0)) { report in
            do {
                try files.save(report)
                written = files.directory.appendingPathComponent("chat-stall-\(report.createdAtUnixMilliseconds)-\(report.startedAt).json")
            } catch { Issue.record(error) }
        }
        defer { if let written { try? FileManager.default.removeItem(at: written) } }
        _ = recorder.poll(at: 1, unixMilliseconds: wall)
        _ = recorder.poll(at: 2_100_000_001, unixMilliseconds: wall)
        let url = try #require(written)
        let report = try JSONDecoder().decode(ChatStallReport.self, from: Data(contentsOf: url))
        #expect(report.startedAt == 1 && report.recoveredAt == nil)
        #expect(try files.directory.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup == true)
    }
}
