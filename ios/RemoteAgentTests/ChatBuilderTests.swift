import Foundation
import RACore
import RAMarkdown
import RAProto
import Testing

@testable import RemoteAgent

/// Counts how often Markdown is parsed, and what.
private final class Parses: @unchecked Sendable {
    private let lock = NSLock()
    private var texts: [String] = []
    var all: [String] { lock.withLock { texts } }
    func parse(_ text: String) -> [MarkdownBlock] {
        lock.withLock { texts.append(text) }
        return Markdown.parse(text)
    }
}

private func events(_ kinds: [PBEvent.OneOf_Kind]) -> [PBEvent] {
    kinds.enumerated().map { i, kind in
        .with {
            $0.seq = UInt64(i + 1)
            $0.kind = kind
        }
    }
}

private func user(_ text: String) -> PBEvent.OneOf_Kind { .userMessage(.with { $0.text = text }) }
private func reply(_ text: String) -> PBEvent.OneOf_Kind { .agentMessage(.with { $0.text = text }) }
private func started(_ id: String) -> PBEvent.OneOf_Kind {
    .toolStarted(
        .with {
            $0.callID = id
            $0.title = "go test"
            $0.kind = .command
        })
}
private func called(_ id: String) -> PBEvent.OneOf_Kind {
    .toolCall(
        .with {
            $0.callID = id
            $0.title = "go test"
            $0.kind = .command
            $0.status = "completed"
        })
}

@Suite struct ChatBuilderTests {
    @MainActor @Test func aNewestTranscriptDoesNotReduceInputStateAcrossAMissingPrefix() async {
        var tail = PBEvent(); tail.seq = 601; tail.sessionID = "history"
        tail.inputState.requestID = "request"; tail.inputState.status = .submitted
        let model = SessionModel()
        await model.show(SessionEvents(events: [tail], stateEvents: [], historyPartial: true))
        #expect(model.ready && model.historyPartial && !model.loaded)
        #expect(model.derived.inputs.isEmpty)
        await model.show(SessionEvents(events: [tail], loaded: true))
        #expect(model.derived.inputs["request"] == .submitted)
        #expect(model.loaded && !model.historyPartial)
    }

    @Test func aReplyIsParsedOnceHoweverOftenTheChatIsBuilt() async {
        let parses = Parses()
        let builder = ChatBuilder(parse: parses.parse)
        var list = events([.turnStarted(.init()), user("查一下"), reply("**结果**\n\n- 一\n- 二")])
        let first = await builder.build(PagedList(list), ChatParams())
        #expect(first.rows.map(\.id) == ["e2", "e3"])
        #expect(first.rows[1].blocks == Markdown.parse("**结果**\n\n- 一\n- 二"))
        #expect(first.rows[0].blocks.isEmpty)

        // A tool starts and ends (the rows are built anew by the package), then another reply comes.
        list = events([
            .turnStarted(.init()), user("查一下"), reply("**结果**\n\n- 一\n- 二"), started("c1"),
        ])
        _ = await builder.build(PagedList(list), ChatParams())
        list = events([
            .turnStarted(.init()), user("查一下"), reply("**结果**\n\n- 一\n- 二"), started("c1"),
            called("c1"), reply("好了"),
        ])
        let last = await builder.build(PagedList(list), ChatParams())
        #expect(
            last.rows.map(\.id) == ["e2", "e3", "e4", "e6"],
            "a tool keeps the place where it started")
        #expect(parses.all == ["**结果**\n\n- 一\n- 二", "好了"])
        #expect(last.rows[1] == first.rows[1], "the row before is the same row")
    }

    @Test func whatIsPendingComesWithTheRows() async {
        let builder = ChatBuilder()
        let list = events([
            .turnStarted(.init()), user("装依赖"),
            .approvalRequested(
                .with {
                    $0.approvalID = "A"
                    $0.command = "npm i"
                }),
        ])
        let snapshot = await builder.build(
            PagedList(list),
            ChatParams(
                live: .with {
                    $0.actions = [list[2]]
                    $0.running = true
                }))
        #expect(snapshot.derived.running)
        #expect(snapshot.derived.approvals.keys == ["A"])
        #expect(snapshot.spoke)
        #expect(
            await !builder.build(PagedList(events([.turnStarted(.init())])), ChatParams()).spoke)
    }

    @Test func aMessageQueuedDuringATurnIsPendingByItsRequest() async {
        let builder = ChatBuilder()
        var list = events([.turnStarted(.init()), user("跑一下"), started("c1")])
        _ = await builder.build(PagedList(list), ChatParams(texts: [:]))
        list = events([
            .turnStarted(.init()), user("跑一下"), started("c1"),
            .inputState(
                .with {
                    $0.requestID = "q1"
                    $0.status = .queued
                    $0.stage = .waiting
                    $0.text = "排队的"
                    $0.mode = .queue
                }),
        ])
        let snapshot = await builder.build(PagedList(list), ChatParams(texts: ["q1": "排队的"]))
        #expect(snapshot.derived.inputs["q1"] == .queued)
        #expect(pendingInputs(snapshot.derived).map(\.id) == ["q1"])
    }

    @Test func rowsNameWhoSentAMessageByTheDeviceList() async {
        let builder = ChatBuilder()
        var message = PBEvent.with {
            $0.seq = 1
            $0.kind = user("你好")
        }
        message.origin = .with {
            $0.kind = .client
            $0.deviceID = "aaaa000000001234"
        }
        var params = ChatParams(me: "me")
        func from(_ p: ChatParams) async -> String? {
            guard case .user(let m) = await builder.build(PagedList([message]), p).rows[0].item
            else { return nil }
            return m.from
        }
        // Before the device list is read nothing is known of who it was.
        #expect(await from(params) == "另一客户端")
        // A device the list does not have was removed: no part of its ID is shown.
        params.devices = ["bbbb000000005678": DeviceView(id: "bbbb000000005678", kind: .client, createdMs: 0)]
        #expect(await from(params) == "已移除的设备")
        // The device list arrives: the same history reads differently, so the rows are built again.
        params.devices = [
            "aaaa000000001234": DeviceView(
                id: "aaaa000000001234", kind: .client, createdMs: 0,
                meta: .with { $0.name = "我的 iPad" })
        ]
        #expect(await from(params) == "我的 iPad")
        // What this client sent is not labelled.
        params.me = "aaaa000000001234"
        #expect(await from(params) == nil)
    }

    @Test func aNewSessionsFirstMessageIsShownUntilTheHistoryHasIt() async {
        let builder = ChatBuilder()
        let params = ChatParams(prompt: "建一个 Makefile")
        let none = await builder.build(PagedList<PBEvent>(), params)
        #expect(none.startPrompt == StartPrompt(text: "建一个 Makefile", caption: "已送达电脑"))
        let recorded = await builder.build(PagedList(events([user("建一个 Makefile")])), params)
        #expect(recorded.startPrompt == nil)
    }

    @MainActor @Test func aBackfilledPlanReplacesTheScreenWithTheSameLiveSnapshot() async {
        let model = SessionModel()
        let screen = "终端中的计划预览"
        let markdown = "# 计划正文\n\n- 验证历史回填"
        let list = events([reply("准备计划"), .planProposed(.with { $0.text = markdown }), reply("等待批准")])
        let live = PBLiveSnapshot.with {
            $0.session.id = "s"
            $0.session.planOpen = true
            $0.planEventSeq = 2
            $0.planScreen = screen
            $0.planScreenHash = Data(repeating: 1, count: 32)
        }
        var snapshot = SessionEvents(events: PagedList(Array(list.prefix(1))))
        snapshot.live = live
        await model.show(snapshot)
        #expect(model.derived.plan?.text == screen)
        // No refresh of the live snapshot, and the full history is not done.
        snapshot.events = PagedList(Array(list.prefix(2)))
        await model.show(snapshot)
        #expect(!model.loaded)
        #expect(model.derived.plan?.text == markdown)
        snapshot.events = PagedList(list)
        snapshot.loaded = true
        await model.show(snapshot)
        #expect(model.loaded && model.derived.plan?.text == markdown)

    }

    /// 3 000 events of earlier history, the last of them starting a tool call that completes as the
    /// newest segment begins; the segment's own calls and then `more` replies follow.
    private func joinedHistory(more: Int = 0) -> (all: [PBEvent], newest: [PBEvent]) {
        var kinds: [PBEvent.OneOf_Kind] = []
        for i in 1...999 { kinds += [reply("earlier \(i)"), started("o\(i)"), called("o\(i)")] }
        kinds += [user("one more"), reply("on it"), started("edge")]
        let from = kinds.count + 1
        kinds += [called("edge")]
        for i in 1...20 { kinds += [reply("newest \(i)"), started("n\(i)"), called("n\(i)")] }
        for i in 0..<more { kinds += [reply("after \(i)")] }
        let all = events(kinds)
        return (all, Array(all[(from - 1)..<(all.count - more)]))
    }

    @MainActor @Test func rowsJoiningInFrontAreCountedThoughTheRowsAreKeyedAgain() async {
        let (all, newest) = joinedHistory()
        let model = SessionModel()
        await model.show(SessionEvents(events: newest, stateEvents: [], historyPartial: true))
        let partial = model.rows
        #expect(partial.count == 41 && model.joins == 0)
        await model.show(SessionEvents(events: all, loaded: true))
        // The calls are shown where they started now: the first row and the last have other IDs.
        #expect(!model.rows.contains { $0.id == partial[0].id || $0.id == partial[40].id })
        #expect(model.joins == 1)
        // What is in front is all that is new.
        #expect(model.joined == model.rows.count - partial.count)
    }

    @MainActor @Test func rowsComingAfterAsTheHistoryJoinsAreNotInFront() async {
        let (all, newest) = joinedHistory(more: 2)
        let model = SessionModel()
        await model.show(SessionEvents(events: newest, stateEvents: [], historyPartial: true))
        let partial = model.rows.count
        await model.show(SessionEvents(events: all, loaded: true))
        #expect(model.joins == 1)
        #expect(model.joined == model.rows.count - partial - 2)
    }

    @MainActor @Test func historyWaitsForTheGestureAndAppliesTheLatestSnapshotOnRelease() async throws {
        let (all, newest) = joinedHistory(more: 2)
        let model = SessionModel()
        await model.show(SessionEvents(events: newest, stateEvents: [], historyPartial: true))
        let partial = model.rows
        model.interaction(true)
        await model.show(SessionEvents(events: Array(all.dropLast()), loaded: false))
        await model.show(SessionEvents(events: all, loaded: true))
        #expect(model.rows == partial && model.joins == 0 && model.historyPartial)
        // Domain state is current even while row insertion waits.
        #expect(model.derived == derive(all))
        model.interaction(false)
        // Another drag can begin before the off-main rebuild returns.
        model.interaction(true)
        try await Task.sleep(for: .milliseconds(50))
        #expect(model.rows == partial && model.joins == 0)
        model.interaction(false)
        for _ in 0..<100 where model.joins == 0 { try await Task.sleep(for: .milliseconds(10)) }
        let expected = SessionModel()
        await expected.show(SessionEvents(events: all, loaded: true))
        #expect(model.rows == expected.rows && model.joins == 1 && !model.historyPartial)
        #expect(model.joined == model.rows.count - partial.count - 2)
    }

    @MainActor @Test func aGrowingTailStillUpdatesDuringADrag() async {
        let all = events([user("hello"), reply("first"), reply("second")])
        let model = SessionModel()
        await model.show(SessionEvents(events: Array(all.prefix(2)), loaded: true))
        let before = model.rows
        model.interaction(true)
        await model.show(SessionEvents(events: all, loaded: true))
        #expect(model.rows != before && model.joins == 0)
    }

    @Test func rowsInFrontAreCountedFromTheLastRowBothHave() {
        #expect(rowsInFront(before: [], after: []) == 0)
        func rows(_ ids: [String]) -> [ChatRow] { ids.map { ChatRow(item: .note(.init(key: $0, text: "")), blocks: []) } }
        // Two earlier rows, the first newest one keyed again, and one more at the end.
        #expect(rowsInFront(before: rows(["e9", "e10", "e11"]), after: rows(["e1", "e2", "e8", "e10", "e11", "e12"])) == 2)
        // Rows after the last both have may be keyed again: they are not new.
        #expect(rowsInFront(before: rows(["e9", "x"]), after: rows(["e1", "e9", "y"])) == 1)
        #expect(rowsInFront(before: rows(["e5", "c7"]), after: rows(["e1", "e5", "s6", "e9"])) == 1)
        // None in common leaves what the rows grew by.
        #expect(rowsInFront(before: rows(["x"]), after: rows(["e1", "y"])) == 1)
    }

    @MainActor @Test func aGapFilledAfterTheRowsIsNotAJoin() async {
        let (all, _) = joinedHistory()
        let model = SessionModel()
        await model.show(SessionEvents(events: Array(all.prefix(100)), loaded: false, historyPartial: true))
        await model.show(SessionEvents(events: all, loaded: true))
        #expect(model.joins == 0)
    }

    @MainActor @Test func theModelPublishesRowsAndStateFromASnapshot() async {
        let model = SessionModel()
        #expect(!model.ready)
        await model.show(
            SessionEvents(
                events: events([.turnStarted(.init()), user("你好"), reply("你好。")]), loaded: true))
        #expect(model.ready && model.loaded)
        #expect(model.rows.map(\.id) == ["e2", "e3"])
        #expect(model.derived.running && model.spoke)
        await model.show(
            SessionEvents(
                events: events([
                    .turnStarted(.init()), user("你好"), reply("你好。"),
                    .turnEnded(.with { $0.durationMs = 2_000 }),
                ]), loaded: true, fetchError: "没能读取历史：超时"))
        #expect(!model.derived.running)
        #expect(model.rows.last?.item == .turnEnd(.init(key: "e4", text: "本轮用时 2 秒")))
        #expect(model.fetchError == "没能读取历史：超时")
    }

    /// Two silent scheduled checks and a reply to a person, two hours apart on one day.
    private func scheduledChecks() -> [PBEvent] {
        var out = [PBEvent]()
        func add(_ change: (inout PBEvent) -> Void) {
            var e = PBEvent()
            e.seq = UInt64(out.count + 1)
            change(&e)
            out.append(e)
        }
        for (hour, run) in [(9, "a"), (11, "b")] {
            add {
                $0.time.seconds = 1_793_000_000 + Int64(hour) * 3600
                $0.userMessage.text = "检查"
                $0.origin.scheduledRunID = run
            }
            add { $0.agentMessage.text = "没有新回复" }
            add {
                $0.turnEnded.outcome = .completed
                $0.turnEnded.scheduledRunID = run
            }
        }
        add { $0.userMessage.text = "看一下" }
        add { $0.agentMessage.text = "**好的**" }
        return out
    }

    private func storage(_ rows: [ChatRow]) -> UnsafeRawPointer? {
        rows.withUnsafeBufferPointer { UnsafeRawPointer($0.baseAddress) }
    }

    @Test func theSameHistoryAgainKeepsTheRowsBuiltBefore() async {
        let parses = Parses()
        let builder = ChatBuilder(parse: parses.parse)
        let list = PagedList(scheduledChecks())
        let first = await builder.build(list, ChatParams())
        #expect(first.rows.map(\.id) == ["scheduled-1", "e7", "e8"])
        #expect(first.rows[0].folded.map(\.id) == ["e1", "e2", "e4", "e5"])
        // A live snapshot that says what the history says, then the same snapshot again.
        let idle = ChatParams(live: .with { $0.running = false })
        let again = await builder.build(list, idle)
        let still = await builder.build(list, idle)
        #expect(again.rows == first.rows && still.rows == first.rows)
        #expect(storage(again.rows) == storage(first.rows) && storage(still.rows) == storage(first.rows))
        #expect(parses.all == ["没有新回复", "没有新回复", "**好的**"])
        // Another check after the reply: the rows are built anew.
        var next = scheduledChecks()
        for i in 0..<3 {
            var e = next[i]
            e.seq = UInt64(next.count + 1)
            if i == 0 { e.time.seconds += 4 * 3600; e.origin.scheduledRunID = "c" }
            if i == 2 { e.turnEnded.scheduledRunID = "c" }
            next.append(e)
        }
        let grown = await builder.build(list.appending(Array(next[8...])), idle)
        #expect(grown.rows.map(\.id) == ["scheduled-1", "e7", "e8", "e9", "e10"])
        #expect(storage(grown.rows) != storage(first.rows))
    }

    @Test func aSentTextChangedUnderTheSameIdIsShown() async {
        let builder = ChatBuilder()
        let list = PagedList(
            events([
                user("开始"),
                .inputState(
                    .with {
                        $0.requestID = "q1"
                        $0.status = .canceled
                        $0.reason = "exited"
                    }),
            ]))
        let first = await builder.build(list, ChatParams(texts: ["q1": "原来的"]))
        #expect(first.rows.last?.item == .unsent(.init(key: "uq1", requestId: "q1", text: "原来的")))
        let changed = await builder.build(list, ChatParams(texts: ["q1": "改过的"]))
        #expect(changed.rows.last?.item == .unsent(.init(key: "uq1", requestId: "q1", text: "改过的")))
    }
}
