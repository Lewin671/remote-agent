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
