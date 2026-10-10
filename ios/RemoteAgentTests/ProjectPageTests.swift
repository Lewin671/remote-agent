import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What a project's page decides apart from how it draws it: which answer its summary may show, what a
/// start is made of, and the changes to the project's preferences that wait for one another.
@MainActor
@Suite struct ProjectPageTests {
    /// Waits until `done` holds, or two seconds.
    private func until(_ done: @MainActor () -> Bool) async {
        for _ in 0..<200 where !done() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    private let cfg = NewConfig(machineId: "mac", cwd: "/work/app")
    private let computer = FakeCommands()

    /// The images of a draft, and one of them as it stands once it is on a machine.
    private func images() -> ImageDraft { ImageDraft(key: "project:/work/app", files: VolatileFiles(), saves: nil) }

    private func image(_ id: String, on machine: String? = "mac") -> PendingImage {
        let img = PendingImage(id: id, uri: nil)
        if let machine {
            img.state = .ready
            img.machineId = machine
            img.imageId = "on-\(machine)-\(id)"
        } else {
            img.state = .uploading(progress: 0.4)
        }
        return img
    }

    private func started(_ id: String) -> PBCommandResult {
        .with { $0.createSession = .with { $0.session = .with { $0.id = id } } }
    }

    // 重试 after a failed start comes to the same place as ↑, whose dimming is not what holds a start back.
    @Test func nothingStartsWithoutSomethingToSendOrBeforeEveryImageIsOnTheMachine() async {
        computer.result(started("s-new"))
        let draft = NewSessionDraft(cfg, settled: true)
        var sessions: [String] = []
        // The text was cleared after a start that failed: there is no session without a first message.
        draft.text = "  \n"
        draft.start(computer, cfg) { sid, _, _ in sessions.append(sid) }
        #expect(!draft.running)
        // An image still on its way: the start waits for it instead of leaving it out.
        draft.text = "看这两张图"
        let attached = images()
        attached.images = [image("a"), image("b", on: nil)]
        draft.start(computer, cfg, images: attached) { sid, _, _ in sessions.append(sid) }
        #expect(!draft.running)
        // One that is on another machine than the session starts on is not there either.
        attached.images = [image("a"), image("b", on: "vps")]
        draft.start(computer, cfg, images: attached) { sid, _, _ in sessions.append(sid) }
        #expect(!draft.running && computer.sent.isEmpty && sessions.isEmpty)
        // All of them there: it goes, with all of them; images alone are a first message too.
        draft.text = ""
        attached.images = [image("a"), image("b")]
        var used: [String] = []
        draft.start(computer, cfg, images: attached, onImagesUsed: { used = $0.map(\.id) }) { sid, _, _ in sessions.append(sid) }
        await until { !draft.running }
        #expect(sessions == ["s-new"] && used == ["a", "b"])
        #expect(computer.sent.map(\.createSession.imageIds) == [["on-mac-a", "on-mac-b"]])
    }

    private let lost = CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行")

    /// A start, awaited; what it reported when it made a session.
    private func start(_ draft: NewSessionDraft, _ cfg: NewConfig, _ images: ImageDraft? = nil) async -> NewConfig? {
        var used: NewConfig?
        draft.start(computer, cfg, images: images) { _, with, _ in used = with }
        await until { !draft.running }
        return used
    }

    @Test func anUnknownStartIsOnlyRepeatedWhileItIsStillTheSameStart() async {
        computer.result(started("s-new"))
        let draft = NewSessionDraft(cfg, settled: true)
        let attached = images()
        // Images alone, and no answer: the text is the same nothing before and after.
        attached.images = [image("a")]
        computer.answer(lost)
        #expect(await start(draft, cfg, attached) == nil)
        #expect(draft.frozen)
        // Another image in its place is another first message: it is not image A's request again.
        attached.images = [image("b")]
        computer.answer(lost)
        _ = await start(draft, cfg, attached)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2"])
        #expect(computer.forwarded.map(\.retry) == [false, false])
        #expect(computer.sent.map(\.createSession.imageIds) == [["on-mac-a"], ["on-mac-b"]])
        // The same start once more is the same request, which the computer answers from its log.
        computer.answer(lost)
        _ = await start(draft, cfg, attached)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2", "r2"] && computer.forwarded.last?.retry == true)
        #expect(computer.sent.count == 2, "no third command was sealed")
        // Another mode, another CLI or another machine is another start too.
        var asked = cfg
        asked.mode = .ask
        computer.answer(lost)
        _ = await start(draft, asked, attached)
        var claude = asked
        claude.agent = .claude
        computer.answer(lost)
        _ = await start(draft, claude, attached)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2", "r2", "r3", "r4"])
        #expect(computer.sent.map(\.createSession.mode) == [.unspecified, .unspecified, .ask, .ask])
        #expect(computer.sent.map(\.createSession.agent) == [.codex, .codex, .codex, .claude])
        // And so is another message; this one arrives, and reports what it was sent with.
        draft.text = "看这张图"
        #expect(await start(draft, claude, attached) == claude)
        #expect(computer.forwarded.map(\.id).last == "r5" && !draft.frozen && draft.text.isEmpty)
    }

    @Test func aRepeatedStartReportsTheSettingsItWasSentWith() async {
        computer.result(started("s-new"))
        let draft = NewSessionDraft(cfg, settled: true)
        draft.text = "say hi"
        computer.answer(lost)
        #expect(await start(draft, cfg) == nil)
        // The same start again: what the project stores of it is what the request carried.
        #expect(await start(draft, cfg) == cfg)
        #expect(computer.forwarded.map(\.id) == ["r1", "r1"])
    }

    /// The server as the project preferences are written to it: it holds each write until the test lets it end.
    @MainActor
    private final class PrefsServer {
        private(set) var asked: [PrefsOp] = []
        private var waiting: [CheckedContinuation<Void, Error>] = []

        func write(_ op: PrefsOp) async throws {
            asked.append(op)
            try await withCheckedThrowingContinuation { waiting.append($0) }
        }

        /// Ends the oldest write that is still open.
        func answer(_ error: Error? = nil) {
            let c = waiting.removeFirst()
            if let error { c.resume(throwing: error) } else { c.resume() }
        }

        var open: Int { waiting.count }
    }

    @Test func repeatingTheOnlyPendingChangeDoesNotStartAnotherWorker() async {
        let server = PrefsServer()
        let changes = PrefsChanges()
        let path = "/work/app"
        let pin = PrefsOp.pin(path: path)
        let settings = PrefsOp.setSettings(.init(path: path, machineId: "mac"))
        var done: [String] = []
        changes.run(pin, onDone: { done.append("pin") }, write: server.write)
        await until { server.open == 1 }
        changes.run(pin, onDone: { done.append("again") }, write: server.write)
        changes.run(settings, onDone: { done.append("settings") }, write: server.write)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(server.asked == [pin] && server.open == 1)
        guard server.asked == [pin], server.open == 1 else {
            while server.open > 0 { server.answer(CommandError("unavailable", "连接超时")) }
            return
        }
        server.answer()
        await until { server.asked.count == 2 }
        #expect(server.asked == [pin, settings] && changes.states[JvmKey(path)] == .saving(settings))
        server.answer()
        await until { changes.states[JvmKey(path)] == nil }
        #expect(done == ["pin", "settings"])
    }

    @Test func aChangeMadeWhileAnotherIsOnItsWayIsWrittenAfterItNotDropped() async {
        let server = PrefsServer()
        let changes = PrefsChanges()
        let path = "/work/app"
        let settings = PrefsOp.setSettings(.init(path: path, machineId: "mac", sets: [.with { $0.agent = .codex; $0.mode = .ask }]))
        var done: [String] = []
        changes.run(.pin(path: path), onDone: { done.append("pin") }, write: server.write)
        // 完成 in 项目设置 while the pin is still on its way.
        changes.run(settings, onDone: { done.append("settings") }, write: server.write)
        // A second tap on the same thing is not a second change.
        changes.run(settings, onDone: { done.append("again") }, write: server.write)
        await until { server.open == 1 }
        #expect(changes.states[JvmKey(path)] == .saving(.pin(path: path)) && server.asked == [.pin(path: path)])
        server.answer()
        await until { server.asked.count == 2 }
        // The page says its settings are being saved until they are.
        #expect(changes.states[JvmKey(path)] == .saving(settings) && changes.states[JvmKey(path)]?.ofSettings == true)
        #expect(done == ["pin"])
        server.answer()
        await until { changes.states[JvmKey(path)] == nil }
        #expect(server.asked == [.pin(path: path), settings] && done == ["pin", "settings"])
        // Another project's changes do not wait for this one's.
        changes.run(.pin(path: path), write: server.write)
        changes.run(.pin(path: "/work/other"), write: server.write)
        await until { server.open == 2 }
        server.answer()
        server.answer()
        await until { changes.states.isEmpty }
    }

    @Test func repeatingTheOnlyInFlightChangeDoesNotStartAnotherWriter() async {
        let server = PrefsServer()
        let changes = PrefsChanges()
        let path = "/work/app"
        let pin = PrefsOp.pin(path: path)
        let settings = PrefsOp.setSettings(.init(path: path, machineId: "mac"))
        var done: [String] = []
        changes.run(pin, onDone: { done.append("pin") }, write: server.write)
        await until { server.open == 1 }
        changes.run(pin, onDone: { done.append("duplicate") }, write: server.write)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(server.asked == [pin] && server.open == 1)
        guard server.open == 1 else {
            while server.open > 0 { server.answer(URLError(.cancelled)) }
            return
        }
        changes.run(settings, onDone: { done.append("settings") }, write: server.write)
        changes.run(.hide(path: path), onDone: { done.append("hide") }, write: server.write)
        server.answer()
        await until { server.asked.count == 2 }
        #expect(server.asked == [pin, settings] && server.open == 1 && done == ["pin"])
        server.answer()
        await until { server.asked.count == 3 }
        #expect(server.open == 1 && done == ["pin", "settings"])
        server.answer()
        await until { changes.states.isEmpty }
        #expect(done == ["pin", "settings", "hide"])
    }

    @Test func codeUnitDistinctProjectsHaveIndependentPreferenceQueues() async {
        let server = PrefsServer()
        let changes = PrefsChanges()
        let a = "/work/caf\u{e9}"
        let b = "/work/cafe\u{301}"
        var done = 0
        changes.run(.setSettings(.init(path: a, machineId: "mac")), onDone: { done += 1 }, write: server.write)
        await until { server.open == 1 }
        #expect(!changes.settingsWaiting(b))
        changes.run(.setSettings(.init(path: b, machineId: "mac")), onDone: { done += 1 }, write: server.write)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(server.open == 2 && changes.states.count == 2)
        #expect(server.asked.map { JvmKey($0.path) } == [JvmKey(a), JvmKey(b)])
        guard changes.states.count == 2 else {
            while server.open > 0 { server.answer(URLError(.cancelled)) }
            return
        }
        server.answer()
        await until { !changes.settingsWaiting(a) }
        #expect(changes.settingsWaiting(b) && done == 1)
        server.answer()
        await until { changes.states.isEmpty }
        #expect(done == 2)
    }

    @Test func closingThePreferenceQueueDiscardsCallbacksAndLateAnswers() async {
        let server = PrefsServer()
        let changes = PrefsChanges()
        let path = "/work/app"
        var done = false
        changes.run(.pin(path: path), onDone: { done = true }, write: server.write)
        changes.run(.hide(path: path), onDone: { done = true }, write: server.write)
        await until { server.open == 1 }
        changes.close()
        #expect(changes.states.isEmpty && !changes.settingsWaiting(path))
        server.answer()
        try? await Task.sleep(for: .milliseconds(30))
        #expect(!done && changes.states.isEmpty && server.asked == [.pin(path: path)])
        changes.run(.hide(path: path), write: server.write)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(server.open == 0 && changes.states.isEmpty)
    }

    @Test func aChangeThatFailsKeepsThoseBehindItUntilItIsTriedAgain() async {
        struct Down: LocalizedError { var errorDescription: String? { "连接超时" } }
        let server = PrefsServer()
        let changes = PrefsChanges()
        let path = "/work/app"
        let settings = PrefsOp.setSettings(.init(path: path, machineId: "mac"))
        var done: [String] = []
        changes.run(.pin(path: path), onDone: { done.append("pin") }, write: server.write)
        changes.run(settings, onDone: { done.append("settings") }, write: server.write)
        await until { server.open == 1 }
        server.answer(Down())
        await until { changes.states[JvmKey(path)]?.saving == false }
        #expect(changes.states[JvmKey(path)] == .failed(.pin(path: path), "连接超时"))
        // The project's page says so too: its settings are not saved while they wait behind the pin.
        #expect(changes.settingsWaiting(path) && !changes.settingsWaiting("/work/other"))
        try? await Task.sleep(for: .milliseconds(30))
        #expect(server.asked == [.pin(path: path)], "the settings wait behind the change that failed")
        // 重试: the pin again, once, and then the settings that waited.
        changes.run(.pin(path: path), write: server.write)
        await until { server.open == 1 }
        server.answer()
        await until { server.asked.count == 3 }
        server.answer()
        await until { changes.states[JvmKey(path)] == nil }
        #expect(server.asked == [.pin(path: path), .pin(path: path), settings] && done == ["pin", "settings"])
        #expect(!changes.settingsWaiting(path))
        // A change made after a failure takes the failed one along, in the order they were made.
        changes.run(.unpin(path: path), write: server.write)
        await until { server.open == 1 }
        server.answer(Down())
        await until { changes.states[JvmKey(path)]?.saving == false }
        changes.run(.hide(path: path), write: server.write)
        await until { server.open == 1 }
        #expect(changes.states[JvmKey(path)] == .saving(.unpin(path: path)))
        server.answer()
        await until { server.open == 1 && changes.states[JvmKey(path)] == .saving(.hide(path: path)) }
        server.answer()
        await until { changes.states[JvmKey(path)] == nil }
        #expect(server.asked.suffix(3) == [.unpin(path: path), .unpin(path: path), .hide(path: path)])
    }

    @Test func aModelListThatArrivesAfterAnotherCliWasPickedIsNotShown() async {
        let models = SummaryModels()
        let codex = SummaryModels.Key(machine: "mac", agent: .codex)
        let claude = SummaryModels.Key(machine: "mac", agent: .claude)
        func list(_ model: String) -> PBListModelsResponse { .with { $0.defaultModel = model } }
        // Codex's list is asked for first and is slow; the page's task for it is cancelled, the request is not.
        var release: CheckedContinuation<Void, Never>?
        let first = Task {
            await models.load(codex) { _ in
                await withCheckedContinuation { release = $0 }
                return list("gpt-5")
            }
        }
        await until { release != nil }
        await models.load(claude) { _ in list("opus") }
        #expect(models.list?.defaultModel == "opus")
        first.cancel()
        release?.resume()
        await first.value
        #expect(models.list?.defaultModel == "opus", "the summary is about Claude Code now")
        // Asking again starts from nothing, and no machine shows nothing.
        await models.load(codex) { _ in list("gpt-5") }
        #expect(models.list?.defaultModel == "gpt-5")
        await models.load(nil) { _ in list("x") }
        #expect(models.list == nil)
    }
}
