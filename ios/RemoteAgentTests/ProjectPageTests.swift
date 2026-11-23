import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What a project's page decides apart from how it draws it: which answer its summary may show, what a
/// start is made of, and the changes to the project's preferences that wait for one another.
@MainActor
@Suite struct ProjectPageTests {
    @Test func modelControlsPinOnlyWithRoomForTwoRowsAndPadding() {
        #expect(shouldPinModelControls(bodyHeight: 228, controlsHeight: 132))
        #expect(!shouldPinModelControls(bodyHeight: 227, controlsHeight: 132))
        #expect(!shouldPinModelControls(bodyHeight: 228, controlsHeight: 132, rowHeight: 52))
        #expect(!shouldPinModelControls(bodyHeight: 228, controlsHeight: 132, rowHeight: 0))
    }

    @Test func modelToolWidthsDropCLINameBeforeTruncatingModel() {
        let full = NewTaskToolsLayout.widths(available: 300, cli: 100, compactCLI: 36, model: 180)
        #expect(full.cli == 100 && full.model == 180)
        let compact = NewTaskToolsLayout.widths(available: 250, cli: 100, compactCLI: 36, model: 180)
        #expect(compact.cli == 36 && compact.model == 180)
        let ellipsized = NewTaskToolsLayout.widths(available: 200, cli: 100, compactCLI: 36, model: 180)
        #expect(ellipsized.cli == 36 && ellipsized.model == 162)
        let single = NewTaskToolsLayout.widths(available: 200, cli: 0, compactCLI: 0, model: 280)
        #expect(single.cli == 0 && single.model == 200)
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
            img.expiresMs = Int64.max
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
        await eventually { !draft.running }
        #expect(sessions == ["s-new"] && used == ["a", "b"])
        #expect(computer.sent.map(\.createSession.imageIds) == [["on-mac-a", "on-mac-b"]])
    }

    private let lost = CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行", outcome: .unknown)

    /// A start, awaited; what it reported when it made a session.
    private func start(_ draft: NewSessionDraft, _ cfg: NewConfig, _ images: ImageDraft? = nil) async -> NewConfig? {
        var used: NewConfig?
        draft.start(computer, cfg, images: images) { _, with, _ in used = with }
        await eventually { !draft.running }
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
        // The same picked image has been uploaded again after its deadline. The unknown request stays frozen.
        attached.images[0].imageId = "renewed-upload"
        // The same start once more is the same request, which the computer answers from its log.
        computer.answer(lost)
        _ = await start(draft, cfg, attached)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2", "r2"] && computer.forwarded.last?.retry == true)
        #expect(computer.sent.count == 2, "no third command was sealed")
        // Another CLI or another machine is another start too.
        var claude = cfg
        claude.agent = .claude
        computer.answer(lost)
        _ = await start(draft, claude, attached)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2", "r2", "r3"])
        #expect(computer.sent.map(\.createSession.agent) == [.codex, .codex, .claude])
        // And so is another message; this one arrives, and reports what it was sent with.
        draft.text = "看这张图"
        #expect(await start(draft, claude, attached) == claude)
        #expect(computer.forwarded.map(\.id).last == "r4" && !draft.frozen && draft.text.isEmpty)
    }

    // A new task has no approval mode: the request leaves it to the machine's default, whatever the configuration holds.
    @Test func aStartLeavesTheApprovalModeUnspecified() async {
        computer.result(started("s-new"))
        let draft = NewSessionDraft(cfg, settled: true)
        draft.text = "say hi"
        var full = cfg
        full.mode = .full
        _ = await start(draft, full)
        #expect(computer.sent.map(\.createSession.mode) == [.unspecified])
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
}
