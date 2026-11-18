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

/// A thread-safe list of what fakes were called with.
private final class Mail<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [T] = []
    func put(_ item: T) { lock.withLock { items.append(item) } }
    var all: [T] { lock.withLock { items } }
}

@MainActor
@Suite struct ConnectionCheckModelTests {
    @Test func theServerIsTheMiddleOfThreeAndTheComputersComeAfter() async {
        let samples = Script<Int64>()
        for ms in [900, 80, 120] as [Int64] { samples.then(ms) }
        let order = Mail<String>()
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: {
                order.put("server")
                return try samples.next()
            },
            machine: { id in
                order.put(id)
                return id == "slow" ? 2140 : 132
            }))
        await check.measure(online: ["mac", "slow"])
        // One slow first request does not make the line slow.
        #expect(check.server == .ok(ms: 120))
        #expect(check.machines == ["mac": .ok(ms: 132), "slow": .ok(ms: 2140)])
        #expect(!check.busy)
        #expect(Array(order.all.prefix(3)) == ["server", "server", "server"])
        #expect(Set(order.all.dropFirst(3)) == ["mac", "slow"])
    }

    @Test func aReachableIndependentPathIsProbedWhenTheServerFails() async {
        let asked = Mail<String>()
        let peer: PeerState = { var peer = PeerState(); peer.relayRequired = false; peer.reachable = true; peer.ready = true; return peer }()
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: { throw UserError("服务器没有回应") },
            machine: { id in asked.put(id); return 12 },
            canProbe: { id, reached in id == "independent" ? peer.canProbe(reached) : reached }))
        await check.measure(online: ["independent", "relay"])
        #expect(check.server == .fail(error: L10n.connectionNoResponse()))
        #expect(check.machines == ["independent": .ok(ms: 12), "relay": .skip])
        #expect(asked.all == ["independent"])
    }

    @Test func aServerThatDoesNotAnswerLeavesTheComputersUnmeasured() async {
        let asked = Mail<String>()
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: { throw UserError("服务器没有回应") },
            machine: { id in
                asked.put(id)
                return 1
            }))
        await check.measure(online: ["mac", "vps"])
        #expect(check.server == .fail(error: L10n.connectionNoResponse()))
        #expect(check.machines == ["mac": .skip, "vps": .skip])
        #expect(asked.all.isEmpty)
        #expect(!check.busy)
    }

    @Test func oneComputerFailingDoesNotStopTheOthers() async {
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: { 50 },
            machine: { id in
                if id == "vps" { throw CommandError(CommandError.unavailable, "电脑不在线，指令没有发出") }
                return 200
            }))
        await check.measure(online: ["mac", "vps"])
        #expect(check.server == .ok(ms: 50))
        #expect(check.machines["mac"] == .ok(ms: 200))
        #expect(check.machines["vps"] == .fail(error: L10n.errorsTheComputerIsOfflineTheCommandWasNotSent()))
    }

    @Test func measuringAgainStartsOver() async {
        let fail = Script<Int64>()
        fail.thenFail(UserError("服务器返回 502"))
        for ms in [40, 50, 60] as [Int64] { fail.then(ms) }
        let check = ConnectionCheckModel(probes: ConnectionProbes(server: { try fail.next() }, machine: { _ in 300 }))
        await check.measure(online: ["mac"])
        #expect(check.server == .fail(error: "服务器返回 502"))
        await check.measure(online: [])
        #expect(check.server == .ok(ms: 50))
        // The computer went offline in between: it is no longer a row that was measured.
        #expect(check.machines.isEmpty)
    }

    @Test func itIsBusyWhileItMeasures() async {
        let gate = AsyncStream<Void>.makeStream()
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: {
                for await _ in gate.stream { break }
                return 10
            },
            machine: { _ in 20 }))
        let running = Task { await check.measure(online: ["mac"]) }
        await eventually("busy") { check.busy }
        #expect(check.server == .wait)
        #expect(check.machines == ["mac": .wait])
        for _ in 0..<3 { gate.continuation.yield() }
        gate.continuation.finish()
        await running.value
        #expect(!check.busy)
        #expect(check.machines == ["mac": .ok(ms: 20)])
    }

    @Test func slowIsCalledFromTheThresholdsOn() {
        #expect(slowServerMs == 500 && slowMachineMs == 1500)
        #expect(median([900, 80, 120]) == 120)
    }
}

@MainActor
@Suite struct ImageStorageModelTests {
    private let root = FileManager.default.temporaryDirectory.appending(path: "ra-settings-\(UUID().uuidString)", directoryHint: .isDirectory)
    private let account = Account(
        server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff")

    private func cache() throws -> CipherStore {
        try CipherStore.open(root: root.appending(path: "cache"), account: CipherStore.accountKey(server: account.server, deviceId: account.deviceId))
    }

    private func pending() -> PendingStore {
        PendingStore(
            directory: root.appending(path: "pending"), vault: Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"),
            defaults: UserDefaults(suiteName: "ra-settings-\(UUID().uuidString)")!)
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
        try waiting.put(folder: "session:s1", name: "image", data: Data(count: 1234))
        let model = ImageStorageModel(files: .stores(cache: store, pending: waiting))
        await model.load()
        #expect(model.cache == 4096)
        let pendingBefore = try #require(model.pending)
        #expect(pendingBefore >= 1234)
        await model.clear()
        #expect(model.cache == 0)
        #expect(model.pending == pendingBefore)
        #expect(try waiting.get(folder: "session:s1", name: "image")?.count == 1234)
        try store.close()
    }

    @Test func draftsLeftIdleTooLongAreGoneBeforeTheSpaceIsCounted() async throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let clock = TestClock()
        let waiting = PendingStore(
            directory: root.appending(path: "pending"), vault: Vault(service: "io.github.lewin671.remoteagent.tests.\(UUID().uuidString)"),
            defaults: UserDefaults(suiteName: "ra-settings-\(UUID().uuidString)")!, now: { clock.now })
        defer { try? waiting.clearAll() }
        try waiting.put(folder: "project:/Users/me/Code/foo", name: "1.meta", data: Data(count: 1234))
        // The app is opened again after more than a week, straight on this page: no draft was read or written since.
        clock.advance(PendingPolicy.draftTTL + 60)
        let model = ImageStorageModel(files: .stores(cache: nil, pending: waiting))
        await model.load()
        #expect(model.pending == 0)
        #expect(waiting.expired == 1)
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
