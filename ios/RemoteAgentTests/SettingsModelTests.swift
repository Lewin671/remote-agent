import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What a test tells a fake command to answer, and what it was asked.
private final class Script<Answer: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [Result<Answer, Error>] = []
    private var count = 0

    func then(_ answer: Answer) { lock.withLock { answers.append(.success(answer)) } }
    func thenFail(_ error: Error) { lock.withLock { answers.append(.failure(error)) } }
    var calls: Int { lock.withLock { count } }

    func next() throws -> Answer {
        let answer = lock.withLock { () -> Result<Answer, Error>? in
            count += 1
            return answers.isEmpty ? nil : answers.removeFirst()
        }
        guard let answer else { throw UserError("the test scripted no answer") }
        return try answer.get()
    }
}

@MainActor
@Suite struct ConnectionCheckModelTests {
    @Test func theServerIsTheMiddleOfThree() async {
        let samples = Script<Int64>()
        for ms in [900, 80, 120] as [Int64] { samples.then(ms) }
        let check = ConnectionCheckModel(probes: ConnectionProbes(server: { try samples.next() }))
        await check.measure()
        // One slow first request does not make the line slow.
        #expect(check.server == .ok(ms: 120))
        #expect(!check.busy)
    }

    @Test func measuringAgainStartsOver() async {
        let fail = Script<Int64>()
        fail.thenFail(UserError("服务器返回 502"))
        for ms in [40, 50, 60] as [Int64] { fail.then(ms) }
        let check = ConnectionCheckModel(probes: ConnectionProbes(server: { try fail.next() }))
        await check.measure()
        #expect(check.server == .fail(error: "服务器返回 502"))
        await check.measure()
        #expect(check.server == .ok(ms: 50))
    }

    @Test func itIsBusyWhileItMeasures() async {
        let gate = AsyncStream<Void>.makeStream()
        let check = ConnectionCheckModel(probes: ConnectionProbes(server: {
            for await _ in gate.stream { break }
            return 10
        }))
        let running = Task { await check.measure() }
        await eventually("busy") { check.busy }
        #expect(check.server == .wait)
        for _ in 0..<3 { gate.continuation.yield() }
        gate.continuation.finish()
        await running.value
        #expect(!check.busy)
        #expect(check.server == .ok(ms: 10))
    }

    @Test func slowIsCalledFromTheThresholdOn() {
        #expect(slowServerMs == 500)
        #expect(median([900, 80, 120]) == 120)
        #expect(formatLatencyMs(1250) == "1,250")
    }
}

@MainActor
@Suite struct ImageStorageModelTests {
    private let root = FileManager.default.temporaryDirectory.appending(path: "ra-settings-\(UUID().uuidString)", directoryHint: .isDirectory)
    private let account = Account(
        server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")

    private func cache() throws -> CipherStore {
        try CipherStore.open(root: root.appending(path: "cache"), account: CipherStore.accountKey(server: account.server, deviceId: account.deviceId))
    }

    private func pending() -> PendingStore {
        PendingStore(
            directory: root.appending(path: "pending"),
            vault: Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)")
        )
    }

    @Test func nothingStoredIsZeroNotAnError() async throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try cache()
        let model = ImageStorageModel(files: .stores(cache: store, pending: pending()))
        await model.load()
        #expect(model.pending == 0 && model.cache == 0 && model.error.isEmpty)
        try store.close()
    }

    @Test func withoutAnOpenCacheThereIsNothingToCountOrClear() async {
        let model = ImageStorageModel(files: .stores(cache: nil, pending: pending()))
        await model.load()
        #expect(model.pending == 0 && model.cache == 0)
        await model.clear()
        #expect(model.error.isEmpty && !model.clearing)
    }

    @Test func itCountsAndClearsThroughTheStoreItself() async throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try cache()
        await store.putImage(key: "s1/a", envelope: Data(count: 3000))
        await store.putImage(key: "s1/b", envelope: Data(count: 2000))
        #expect(store.imageBytes == 5000)
        let model = ImageStorageModel(files: .stores(cache: store, pending: pending()))
        await model.load()
        #expect(model.cache == 5000)

        await model.clear()
        #expect(model.cache == 0 && !model.clearing && model.error.isEmpty)
        #expect(store.imageBytes == 0)
        // The store goes on working: a cleared image is simply not there, and a new one is kept.
        #expect(await store.image(key: "s1/a") == nil)
        await store.putImage(key: "s1/c", envelope: Data(count: 100))
        #expect(await store.image(key: "s1/c")?.count == 100)
        try store.close()
    }

    @Test func clearingKeepsTheImagesWaitingToBeSent() async throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let store = try cache()
        await store.putImage(key: "s1/one", envelope: Data(count: 4096))
        let waiting = pending()
        defer { try? waiting.clearAll() }
        let written = waiting.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data(count: 1234))
        #expect(written.ok)
        let model = ImageStorageModel(files: .stores(cache: store, pending: waiting))
        await model.load()
        #expect(model.cache == 4096)
        let pendingBefore = try #require(model.pending)
        #expect(pendingBefore >= 1234)
        await model.clear()
        #expect(model.cache == 0)
        #expect(model.pending == pendingBefore)
        #expect(waiting.read(opID: 2, bucket: .draft, key: "draft:session:s1").data.count == 1234)
        try store.close()
    }

    @Test func aClearThatFailsSaysSoAndReadsAgain() async {
        struct Refused: LocalizedError { var errorDescription: String? { "磁盘只读" } }
        let model = ImageStorageModel(files: LocalImageFiles(pendingUsed: { 1 }, cacheUsed: { 77 }, clearCache: { throw Refused() }))
        await model.load()
        await model.clear()
        #expect(model.error.contains("磁盘只读"))
        #expect(model.cache == 77 && !model.clearing)
    }
}

@Suite struct ClientIconTests {
    @Test func aClientIsDrawnAsWhatItIs() {
        #expect(clientIcon(description: "Safari on iOS") == "globe")
        #expect(clientIcon(description: "Chrome on Linux") == "globe")
        #expect(clientIcon(description: "iOS 27.0 · iPad") == "ipad")
        #expect(clientIcon(description: "iOS 27.0 · iPhone") == "iphone")
        #expect(clientIcon(description: "Android 16 · Pixel 9") == "iphone")
        #expect(clientIcon(description: "") == "iphone")
    }
}
