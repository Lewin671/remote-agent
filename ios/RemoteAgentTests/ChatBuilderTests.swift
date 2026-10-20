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
        // Unverified legacy history cannot provide the plan body.
        snapshot.unverifiedLegacy = true
        snapshot.verifiedEvents = PagedList(Array(list.prefix(1)))
        await model.show(snapshot)
        #expect(model.derived.plan?.text == screen)
        snapshot.verifiedEvents = PagedList(list)
        await model.show(snapshot)
        #expect(model.derived.plan?.text == markdown)
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
}
