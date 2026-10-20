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

private func settings(timeout: Int32 = 300, model: String = "") -> PBDaemonSettings {
    .with {
        $0.approvalTimeoutSeconds = timeout
        $0.defaultModel = model
    }
}

private func reply(_ s: PBDaemonSettings, upload: Int64? = nil) -> PBGetSettingsResponse {
    .with {
        $0.settings = s
        if let upload { $0.imageStorage = .with { $0.uploadBytes = upload } }
    }
}

@MainActor
@Suite struct MachineSettingsModelTests {
    private let reads = Script<PBGetSettingsResponse>()
    private let updates = Script<PBUpdateSettingsResponse>()
    private let sent = Mail<(PBDaemonSettings, Bool)>()

    private func model() -> MachineSettingsModel {
        MachineSettingsModel(calls: MachineSettingsCalls(
            read: { [reads] in try reads.next() },
            update: { [updates, sent] s, clean in
                sent.put((s, clean))
                return try updates.next()
            }))
    }

    @Test func readsTheSettingsAndTheImageSizes() async {
        let m = model()
        reads.then(reply(settings(timeout: 600), upload: 1 << 20))
        await m.read()
        #expect(m.settings?.approvalTimeoutSeconds == 600)
        #expect(m.imageStorage?.uploadBytes == 1 << 20)
        #expect(!m.reading && m.readError.isEmpty)
    }

    @Test func aReadThatFailsSaysWhyAndTheNextOneClearsIt() async {
        let m = model()
        reads.thenFail(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        await m.read()
        #expect(m.settings == nil)
        #expect(m.readError == "电脑不在线，指令没有发出")
        reads.then(reply(settings()))
        await m.read()
        #expect(m.settings != nil && m.readError.isEmpty)
    }

    @Test func aRefreshThatFailsKeepsWhatWasShown() async {
        let m = model()
        reads.then(reply(settings(timeout: 120)))
        await m.read()
        reads.thenFail(UserError("没有回应"))
        await m.read()
        #expect(m.settings?.approvalTimeoutSeconds == 120)
        #expect(m.readError == "没有回应")
    }

    /// A model whose update waits until the test lets it go, then answers with `outcome`, or what it was sent.
    private func held(_ outcome: Result<PBUpdateSettingsResponse, Error>? = nil) -> (MachineSettingsModel, AsyncStream<Void>.Continuation) {
        let (held, release) = AsyncStream.makeStream(of: Void.self)
        let m = MachineSettingsModel(calls: MachineSettingsCalls(
            read: { [reads] in try reads.next() },
            update: { s, _ in
                for await _ in held { break }
                return try outcome?.get() ?? .with { $0.settings = s }
            }))
        return (m, release)
    }

    @Test func aReadAskedForDuringASaveWaitsForItAndDoesNotLeaveThePageSaving() async {
        let (m, release) = held()
        reads.then(reply(settings()))
        await m.read()
        let saving = Task { await m.save(settings(timeout: 600)) }
        while !m.saving { await Task.yield() }
        // A pull to refresh, or the computer coming back, while the change is on its way: the computer
        // might answer it with the settings from before the change, so it is not sent yet.
        await m.read()
        #expect(m.saving && reads.calls == 1)
        // Read after the change has ended, the settings are the computer's own word on it.
        reads.then(reply(settings(timeout: 590)))
        release.yield()
        await saving.value
        #expect(!m.saving && !m.reading)
        #expect(reads.calls == 2)
        #expect(m.settings?.approvalTimeoutSeconds == 590)
    }

    @Test func aSaveIsShownWhateverWasAskedForMeanwhile() async {
        let (m, release) = held()
        reads.then(reply(settings()))
        await m.read()
        let saving = Task { await m.save(settings(timeout: 600)) }
        while !m.saving { await Task.yield() }
        await m.read()
        // The read made afterwards fails: what the save came to stays.
        reads.thenFail(UserError("没有回应"))
        release.yield()
        await saving.value
        #expect(!m.saving)
        #expect(m.settings?.approvalTimeoutSeconds == 600)
        #expect(m.readError == "没有回应" && m.saveError.isEmpty)
    }

    @Test func aSaveThatFailsIsSaidWhateverWasAskedForMeanwhile() async {
        let (m, release) = held(.failure(CommandError("invalid_argument", "审批超时要在 30 秒以上", fromDaemon: true)))
        reads.then(reply(settings(timeout: 300)))
        await m.read()
        let saving = Task { await m.save(settings(timeout: 1)) }
        while !m.saving { await Task.yield() }
        await m.read()
        reads.then(reply(settings(timeout: 300)))
        release.yield()
        await saving.value
        #expect(!m.saving)
        #expect(m.saveError == "审批超时要在 30 秒以上")
        #expect(m.settings?.approvalTimeoutSeconds == 300)
    }

    @Test func aReadASaveOvertookIsMadeAgainAfterIt() async {
        let (held, release) = AsyncStream.makeStream(of: Void.self)
        let entered = Mail<Void>()
        let m = MachineSettingsModel(calls: MachineSettingsCalls(
            read: { [reads] in
                entered.put(())
                // The second read is slow: a change is sent while it is on its way.
                if entered.all.count == 2 { for await _ in held { break } }
                return try reads.next()
            },
            update: { s, _ in .with { $0.settings = s } }))
        reads.then(reply(settings()))
        await m.read()
        let reading = Task { await m.read() }
        while entered.all.count < 2 { await Task.yield() }
        // The read made after the change says what the computer has now.
        reads.then(reply(settings(timeout: 590)))
        await m.save(settings(timeout: 600))
        #expect(entered.all.count == 3)
        #expect(m.settings?.approvalTimeoutSeconds == 590)
        // Asked before the change, the slow one answers after it with the settings from before: dropped.
        reads.then(reply(settings(timeout: 300)))
        release.yield()
        await reading.value
        #expect(m.settings?.approvalTimeoutSeconds == 590)
        #expect(!m.reading && !m.saving)
    }

    @Test func aSaveShowsWhatTheComputerAnswered() async {
        let m = model()
        reads.then(reply(settings()))
        await m.read()
        // The daemon may round what it was sent: its answer is what is shown.
        updates.then(.with { $0.settings = settings(timeout: 590) })
        await m.save(settings(timeout: 600))
        #expect(sent.all.count == 1 && sent.all[0].0.approvalTimeoutSeconds == 600 && sent.all[0].1 == false)
        #expect(m.settings?.approvalTimeoutSeconds == 590)
        #expect(!m.saving && m.saveError.isEmpty && !m.saveUnknown)
    }

    @Test func nothingIsSavedBeforeTheSettingsAreRead() async {
        let m = model()
        await m.save(settings(timeout: 600))
        #expect(sent.all.isEmpty)
    }

    @Test func aRefusedSaveKeepsTheOldSettingsAndSaysWhy() async {
        let m = model()
        reads.then(reply(settings(timeout: 300)))
        await m.read()
        updates.thenFail(CommandError("invalid_argument", "审批超时要在 30 秒以上", fromDaemon: true))
        await m.save(settings(timeout: 1))
        #expect(m.settings?.approvalTimeoutSeconds == 300)
        #expect(m.saveError == "审批超时要在 30 秒以上")
        #expect(!m.saveUnknown)
        // It was the daemon's own word: nothing is read again.
        #expect(reads.calls == 1)
    }

    @Test func anUnknownOutcomeIsSettledByReadingAgain() async {
        let m = model()
        reads.then(reply(settings(timeout: 300)))
        await m.read()
        // The answer was lost, and the computer had applied the change.
        updates.thenFail(CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行"))
        reads.then(reply(settings(timeout: 600)))
        await m.save(settings(timeout: 600))
        #expect(m.settings?.approvalTimeoutSeconds == 600)
        #expect(m.saveError.isEmpty && !m.saveUnknown)
    }

    @Test func anUnknownOutcomeThatWasNotAppliedSaysSo() async {
        let m = model()
        reads.then(reply(settings(timeout: 300)))
        await m.read()
        updates.thenFail(CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行"))
        reads.then(reply(settings(timeout: 300)))
        await m.save(settings(timeout: 600))
        #expect(m.settings?.approvalTimeoutSeconds == 300)
        #expect(m.saveError.contains("没有保存上"))
        #expect(!m.saveUnknown)
    }

    @Test func anOutcomeThatStaysUnknownIsNeverCalledFailed() async {
        let m = model()
        reads.then(reply(settings(timeout: 300)))
        await m.read()
        updates.thenFail(CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行"))
        reads.thenFail(CommandError(CommandError.unavailable, "电脑不在线，指令没有发出"))
        await m.save(settings(timeout: 600))
        #expect(m.saveUnknown)
        #expect(m.saveError.contains("不确定"))
        #expect(!m.saveError.contains("没能保存"))
        // What is shown is still the last thing the computer said.
        #expect(m.settings?.approvalTimeoutSeconds == 300)
        // Reading again settles it and takes the notice away.
        reads.then(reply(settings(timeout: 600)))
        await m.read()
        #expect(!m.saveUnknown && m.saveError.isEmpty)
        #expect(m.settings?.approvalTimeoutSeconds == 600)
    }

    @Test func cleaningIsAskedForWithTheSettingsAsTheyAre() async {
        let m = model()
        reads.then(reply(settings(timeout: 300), upload: 9 << 20))
        await m.read()
        updates.then(.with {
            $0.settings = settings(timeout: 300)
            $0.imageStorage = .with { $0.uploadBytes = 1 << 20 }
        })
        await m.save(settings(timeout: 300), clean: true)
        #expect(sent.all.last?.1 == true)
        #expect(m.imageStorage?.uploadBytes == 1 << 20)
    }
}

/// A thread-safe list of what fakes were called with.
private final class Mail<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [T] = []
    func put(_ item: T) { lock.withLock { items.append(item) } }
    var all: [T] { lock.withLock { items } }
}

@Suite struct DaemonSettingsEditTests {
    @Test func aClisDefaultsAreItsOwn() {
        var s = PBDaemonSettings()
        s = s.with(model: "gpt-6-astra", effort: "high", for: .codex)
        s = s.with(model: "sonnet", effort: "", for: .claude)
        s = s.with(mode: .auto, for: .claude)
        #expect(s.defaultModel == "gpt-6-astra" && s.defaultEffort == "high" && s.defaultMode == .unspecified)
        #expect(s.claude.model == "sonnet" && s.claude.effort.isEmpty && s.claude.mode == .auto)
        #expect(s.defaults(for: .codex).model == "gpt-6-astra")
        #expect(s.defaults(for: .claude).mode == .auto)
        // No mode chosen leaves it to the CLI.
        #expect(s.with(mode: nil, for: .claude).claude.mode == .unspecified)
        #expect(s.with(mode: .ask, for: .codex).defaultMode == .ask)
    }

    @Test func otherFieldsAreLeftAlone() {
        let s = PBDaemonSettings.with {
            $0.approvalTimeoutSeconds = 300
            $0.imageUploadQuotaBytes = 1 << 30
            $0.defaultModel = "m"
        }
        let next = s.with(timeoutMinutes: 10)
        #expect(next.approvalTimeoutSeconds == 600 && next.imageUploadQuotaBytes == 1 << 30 && next.defaultModel == "m")
        #expect(s.with(imageQuota: 256 << 20).approvalTimeoutSeconds == 300)
    }

    @Test func wordsForSizesAndTimes() {
        #expect(timeoutText(seconds: 600) == "10 分钟")
        #expect(timeoutText(seconds: 45) == "45 秒")
        #expect(imageBytes(0) == "0.0 MiB")
        #expect(imageBytes(1) == "不到 0.1 MiB")
        #expect(imageBytes(104_857) == "不到 0.1 MiB")
        #expect(imageBytes(104_858) == "0.1 MiB")
        #expect(imageBytes(24 << 20) == "24.0 MiB")
        #expect(imageBytes(3 << 29) == "1.5 GiB")
        #expect(wholeBytes(256 << 20) == "256 MiB")
        #expect(wholeBytes(1 << 30) == "1 GiB")
        #expect(wholeBytes(1536 << 20) == "1536 MiB")
        let usage = PBImageStorageUsage.with {
            $0.uploadBytes = 10 << 20
            $0.reclaimableBytes = 2 << 20
        }
        #expect(uploadsText(usage) == "10.0 MiB · 可清理 2.0 MiB")
        #expect(uploadsText(.with { $0.uploadBytes = 1 << 20; $0.reservedBytes = 1 << 20 }) == "1.0 MiB · 正在上传 1.0 MiB")
        #expect(osName("darwin") == "macOS" && osName("linux") == "Linux" && osName("") == "未知" && osName("freebsd") == "freebsd")
    }

    @Test func aComputersStateIsOnlySaidWhileConnected() {
        let now: Int64 = 1_800_000_000_000
        var state = RemoteState()
        state.link = .online
        state.conn = .online
        let on = MachineView(id: "a", online: true, lastSeenMs: now)
        let off = MachineView(id: "b", online: false, lastSeenMs: now - 5 * 60_000)
        let never = MachineView(id: "c", online: false, lastSeenMs: 0)
        #expect(machineStatus(on, state: state, now: now)?.text == "在线")
        #expect(machineStatus(off, state: state, now: now)?.text == "5 分钟前")
        #expect(machineStatus(off, state: state, now: now)?.online == false)
        #expect(machineStatus(never, state: state, now: now)?.text == "离线")
        // Not connected: the cache's word on a computer is not repeated (architecture §9).
        state.link = .noServer
        state.conn = .offline
        #expect(machineStatus(on, state: state, now: now) == nil)
        #expect(machineStatus(off, state: state, now: now) == nil)
    }

    @Test func computersAreListedByName() {
        var state = RemoteState()
        func machine(_ id: String, _ host: String) -> MachineView {
            MachineView(id: id, online: true, lastSeenMs: 0, meta: .with { $0.hostname = host })
        }
        state.machines = ["1": machine("1", "mac-10"), "2": machine("2", "Mac-2"), "3": machine("3", "iMac"), "4": machine("4", "iMac")]
        // By name as people read it (2 before 10, case aside); two of one name by ID, so the order never flips.
        #expect(sortedMachines(state).map(\.id) == ["3", "4", "2", "1"])
    }

    @Test func aComputersQuotaInOneLine() {
        let now: Int64 = 1_800_000_000_000
        let m = MachineView(id: "a", online: true, lastSeenMs: now, meta: .with {
            $0.agents = [.with { $0.agent = .codex }]
            $0.quotas = [.with {
                $0.agent = .codex
                $0.state = .ok
                $0.windows = [
                    .with { $0.name = "primary"; $0.windowMinutes = 300; $0.usedPercent = 62; $0.resetsAtMs = now + 3_600_000 },
                    .with { $0.name = "secondary"; $0.windowMinutes = 10080; $0.usedPercent = 34; $0.resetsAtMs = now + 86_400_000 },
                ]
            }]
        })
        #expect(machineQuotaSummary(m, now: now) == "Codex · 7 天已用 34%")
        let none = MachineView(id: "b", online: true, lastSeenMs: now, meta: .with { $0.agents = [.with { $0.agent = .codex }] })
        #expect(machineQuotaSummary(none, now: now) == nil)
    }
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

    @Test func aServerThatDoesNotAnswerLeavesTheComputersUnmeasured() async {
        let asked = Mail<String>()
        let check = ConnectionCheckModel(probes: ConnectionProbes(
            server: { throw UserError("没有回应") },
            machine: { id in
                asked.put(id)
                return 1
            }))
        await check.measure(online: ["mac", "vps"])
        #expect(check.server == .fail(error: "没有回应"))
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
        #expect(check.machines["vps"] == .fail(error: "电脑不在线，指令没有发出"))
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
        #expect(waiting.get(folder: "session:s1", name: "image")?.count == 1234)
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
        func client(_ platform: String, _ description: String) -> DeviceView {
            DeviceView(id: "c", kind: .client, createdMs: 0, meta: .with { $0.platform = platform; $0.description_p = description })
        }
        #expect(clientIcon(client("web", "Safari on iOS")) == Lucide.globe)
        #expect(clientIcon(client("ios", "iOS 27.0 · iPad")) == Lucide.tablet)
        #expect(clientIcon(client("ios", "iOS 27.0 · iPhone")) == Lucide.smartphone)
        #expect(clientIcon(client("android", "Android 16 · Pixel 9")) == Lucide.smartphone)
        #expect(clientIcon(nil) == Lucide.smartphone)
    }
}
