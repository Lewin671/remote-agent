import Foundation
import Testing
@testable import RACore

@Suite struct ChatDiagnosticsTests {
    @Test func wraparoundKeepsOrderAndFiveSecondCountersSurviveEviction() {
        let ring = ChatDiagnosticRing(capacity: 3, timeOrigin: 0)
        for n in 1...10 { ring.record(.content, at: UInt64(n) * 1_000_000_000, Double(n)) }
        let snapshot = ring.snapshot(at: 10_000_000_000)
        #expect(snapshot.total == 10 && snapshot.capacity == 3)
        #expect(snapshot.events.map(\.sequence) == [8, 9, 10])
        #expect(snapshot.events.map(\.a) == [8, 9, 10])
        #expect(snapshot.counterStartSecond == 6)
        #expect(snapshot.counters.first { $0.path == ChatDiagnosticPath.content.rawValue }?.count == 5)
        #expect(ring.snapshot(at: 20_000_000_000).counters.allSatisfy { $0.count == 0 })
    }

    @Test func exportedTimesAreRelativeToThisProcessRing() {
        let ring = ChatDiagnosticRing(capacity: 8, timeOrigin: 50_000_000_000)
        ring.record(.content, at: 50_000_000_100, 1)
        var reports: [ChatStallReport] = []
        var recorder = ChatStallRecorder(ring: ring) { reports.append($0) }
        let ping = recorder.poll(at: 51_000_000_000, unixMilliseconds: 1000)!
        _ = recorder.poll(at: 53_100_000_000, unixMilliseconds: 3100)
        recorder.acknowledge(ping, at: 54_000_000_000)
        #expect(reports[0].startedAt == 1_000_000_000 && reports[0].detectedAt == 3_100_000_000)
        #expect(reports[0].snapshot.events[0].time == 100)
        #expect(reports[1].recoveredAt == 4_000_000_000 && reports[1].duration == 3_000_000_000)
    }

    @MainActor @Test func nestedPublicLayoutScopesRestoreTheirParent() {
        let ring = ChatDiagnosticRing(capacity: 20)
        let outer = ring.beginLayout(.content)
        ring.record(.content, 1)
        let inner = ring.beginLayout(.row, entity: 1)
        ring.record(.row, entity: 1, 2)
        ring.endLayout(inner)
        ring.record(.content, 3)
        ring.endLayout(outer)
        ring.record(.floor, 4)
        let entries = ring.snapshot().events
        #expect(entries.map(\.layout) == [1, 1, 2, 2, 2, 1, 1, 0])
        #expect(entries[2].entity == 1)
    }

    @Test func snapshotsCannotTearConcurrentAppends() async {
        let ring = ChatDiagnosticRing(capacity: 128)
        await withTaskGroup(of: Void.self) { group in
            group.addTask {
                for n in 0..<50_000 { ring.record(.row, entity: UInt64(n), Double(n), -Double(n), Double(n * 2)) }
            }
            group.addTask {
                for _ in 0..<300 {
                    let captured = ring.snapshot()
                    #expect(captured.events.count <= 128)
                    for event in captured.events {
                        #expect(event.a == Double(event.entity) && event.b == -event.a && event.c == 2 * event.a)
                        #expect(event.time <= captured.time)
                    }
                    #expect(zip(captured.events, captured.events.dropFirst()).allSatisfy { $1.sequence == $0.sequence + 1 && $1.time >= $0.time })
                }
            }
        }
        #expect(ring.snapshot().total == 50_000)
    }

    @Test func concurrentLayoutScopesDoNotLeaveAMainLayoutOpen() async {
        let ring = ChatDiagnosticRing(capacity: 4096)
        await withTaskGroup(of: Void.self) { group in
            for n in 0..<4 {
                group.addTask {
                    for _ in 0..<100 { ring.measure(.tableSize, entity: UInt64(n)) { ring.record(.tableViewport, entity: UInt64(n), 200) } }
                }
            }
        }
        let snapshot = ring.snapshot()
        #expect(snapshot.mainLayout == 0)
        let begin = snapshot.events.filter { $0.path == ChatDiagnosticPath.layoutBegin.rawValue }.map(\.layout).sorted()
        let end = snapshot.events.filter { $0.path == ChatDiagnosticPath.layoutEnd.rawValue }.map(\.layout).sorted()
        #expect(begin.count == 400 && begin == end)
    }

    @Test func oneFilePerSimulatedStallAndRecoveryUpdatesThatFile() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let files = ChatStallFiles(directory: directory)
        let ring = ChatDiagnosticRing(capacity: 16, timeOrigin: 0)
        var writes = 0
        var recorder = ChatStallRecorder(ring: ring) { report in
            writes += 1
            do { try files.save(report) } catch { Issue.record(error) }
        }
        // The fake main queue holds this ping until acknowledge is called.
        let ping = recorder.poll(at: 1_000_000_000, unixMilliseconds: 1000)!
        #expect(recorder.poll(at: 3_000_000_000, unixMilliseconds: 3000) == nil)
        #expect(writes == 0)
        for n in 31...50 { #expect(recorder.poll(at: UInt64(n) * 100_000_000, unixMilliseconds: UInt64(n) * 100) == nil) }
        #expect(writes == 1)
        let urls = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
        #expect(urls.count == 1)
        let before = try JSONDecoder().decode(ChatStallReport.self, from: Data(contentsOf: urls[0]))
        #expect(before.recoveredAt == nil && before.snapshot.events.last?.path == ChatDiagnosticPath.stall.rawValue)
        recorder.acknowledge(ping, at: 6_000_000_000)
        recorder.acknowledge(ping, at: 6_500_000_000)
        #expect(writes == 2)
        #expect(try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) == urls)
        let after = try JSONDecoder().decode(ChatStallReport.self, from: Data(contentsOf: urls[0]))
        #expect(after.recoveredAt == 6_000_000_000 && after.duration == 5_000_000_000)
        #expect(after.snapshot.events == before.snapshot.events, "retain evidence from the stall, not a later layout")
        let next = recorder.poll(at: 7_000_000_000, unixMilliseconds: 7000)!
        recorder.acknowledge(ping, at: 8_000_000_000) // stale response cannot dismiss the new ping
        _ = recorder.poll(at: 9_100_000_000, unixMilliseconds: 9100)
        #expect(writes == 3)
        recorder.acknowledge(next, at: 10_000_000_000)
        #expect(writes == 4)
        #expect(try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil).count == 2)
    }

    @Test func suspendingCancelsAHealthyPendingPingAndClosesARecordedStall() {
        var saved: [ChatStallReport] = []
        var recorder = ChatStallRecorder(ring: ChatDiagnosticRing(timeOrigin: 0)) { saved.append($0) }
        let first = recorder.poll(at: 0, unixMilliseconds: 0)!
        recorder.suspend(at: 1_000_000_000)
        let second = recorder.poll(at: 10_000_000_000, unixMilliseconds: 10_000)!
        recorder.acknowledge(first, at: 11_000_000_000)
        _ = recorder.poll(at: 12_100_000_000, unixMilliseconds: 12_100)
        recorder.suspend(at: 13_000_000_000)
        recorder.acknowledge(second, at: 14_000_000_000)
        #expect(saved.count == 2 && saved.last?.duration == 3_000_000_000)
    }

    @Test func filesKeepOnlyNewestFiveAndValuesAreNumeric() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let files = ChatStallFiles(directory: directory)
        let ring = ChatDiagnosticRing(capacity: 5, timeOrigin: 0)
        ring.record(.content, at: 0, .infinity, -.infinity, .nan, 1)
        #expect(ring.snapshot(at: 0).events[0].nonFinite == 7)
        var recorder = ChatStallRecorder(ring: ring) { report in
            do { try files.save(report) } catch { Issue.record(error) }
        }
        for n in 0..<8 {
            let now = UInt64(n) * 10_000_000_000
            let ping = recorder.poll(at: now, unixMilliseconds: now / 1_000_000)!
            _ = recorder.poll(at: now + 3_000_000_000, unixMilliseconds: now / 1_000_000 + 3000)
            recorder.acknowledge(ping, at: now + 4_000_000_000)
        }
        let urls = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
        #expect(urls.count == 5)
        let reports = try urls.map { try JSONDecoder().decode(ChatStallReport.self, from: Data(contentsOf: $0)) }
        #expect(reports.map(\.startedAt).sorted() == (3..<8).map { UInt64($0) * 10_000_000_000 })
        func numeric(_ value: Any) -> Bool {
            if let dictionary = value as? [String: Any] { return dictionary.values.allSatisfy(numeric) }
            if let array = value as? [Any] { return array.allSatisfy(numeric) }
            return value is NSNumber || value is NSNull
        }
        for url in urls { #expect(try numeric(JSONSerialization.jsonObject(with: Data(contentsOf: url)))) }
    }

    /// Run with -c release for an optimized host measurement; no timing assertion
    /// that would make correctness tests depend on the machine's current load.
    @Test func appendMicrobenchmark() {
        let ring = ChatDiagnosticRing()
        for n in 0..<10_000 { ring.record(.row, entity: 1, Double(n), 200, 390) }
        let count = 300_000
        let started = DispatchTime.now().uptimeNanoseconds
        for n in 0..<count { ring.record(.row, entity: 1, Double(n), 200, 390) }
        let elapsed = DispatchTime.now().uptimeNanoseconds - started
        print("Chat diagnostics append: \(Double(elapsed) / Double(count)) ns/event (\(count) events), \(MemoryLayout<ChatDiagnosticEvent>.stride) bytes/slot")
        #expect(ring.snapshot().total == UInt64(count + 10_000))
    }
}
