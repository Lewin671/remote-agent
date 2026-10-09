import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app ChatTest.
@Suite struct ChatTests {
    private func rows(_ events: [PBEvent], _ d: Derived, textOf: (String) -> String? = { _ in nil }) -> [ChatItem] {
        buildChat(events, d: d, machine: "MacBook", textOf: textOf)
    }

    @Test func retainsAcknowledgedStartUntilHistoryArrivesAndShowsFailuresHonestly() throws {
        var d = Derived()
        #expect(pendingStartPrompt("first", events: [], d: d, localIds: [])?.text == "first")
        d.inputs["later-local"] = .failed
        d.inputs["start"] = .queued
        // Waiting: the pending bubble shows it, not a second copy here.
        #expect(pendingStartPrompt("first", events: [], d: d, localIds: ["later-local"]) == nil)
        d.inputs["start"] = .submitted
        #expect(pendingStartPrompt("first", events: [], d: d, localIds: ["later-local"])?.caption == "已送达电脑")
        let recorded = [PBEvent.with { $0.userMessage = .with { $0.text = "first" } }]
        #expect(pendingStartPrompt("first", events: recorded, d: d, localIds: ["later-local"]) == nil)
        d.inputs["start"] = .canceled
        #expect(pendingStartPrompt("first", events: [], d: d, localIds: ["later-local"]) == nil)
        d.inputs["start"] = .failed
        d.failures["start"] = "CLI 没认出图片"
        let failed = try #require(pendingStartPrompt("first", events: [], d: d, localIds: ["later-local"]))
        #expect(failed.warning)
        #expect(failed.caption == "没能发送：CLI 没认出图片")
        d.inputs["start"] = .unconfirmed
        #expect(pendingStartPrompt("first", events: [], d: d, localIds: ["later-local"])?.warning == true)
        #expect(pendingStartPrompt(nil, events: [], d: d, localIds: []) == nil)
    }

    @Test func marksWhereAForkLeavesItsSource() throws {
        let zone = Mk.shanghai
        let now = Mk.at(2026, 10, 4, 18, 0)
        let at = Mk.at(2026, 10, 4, 14, 6) / 1000
        let events = [
            Mk.ev(1, Mk.user("登录接口偶尔超时")),
            Mk.ev(2, Mk.agent("原因是限流器")),
            Mk.ev(3, .sessionForked(.with { $0.sourceSessionID = "src"; $0.sourceTitle = "登录接口偶尔超时" }), sec: at),
            Mk.ev(4, Mk.user("换一种做法")),
        ]
        let items = buildChat(events, d: derive(events), machine: "MacBook", now: now, zone: zone)
        #expect(items.count == 4)
        let want = ChatItem.Fork(key: "e3", time: "14:06", sourceId: "src", sourceTitle: "登录接口偶尔超时")
        #expect(items[2] == .fork(want))
        // A message after the fork appends without losing the line.
        var projection = ChatProjection()
        _ = projection.update(Array(events.prefix(3)), d: derive(events.prefix(3)), machine: "MacBook", now: now, zone: zone)
        #expect(projection.update(events, d: derive(events), machine: "MacBook", now: now, zone: zone)[2] == .fork(want))

        // The source as this client has it now; else the title the event recorded. A source not held
        // here may be on a page not fetched yet: opening it asks for it by its ID.
        let renamed = Mk.session("src") { $0.title = "登录超时：限流" }
        #expect(forkSource(want, sessions: ["src": renamed]) == "登录超时：限流")
        #expect(forkSource(want, sessions: ["src": SessionView(id: "src", machineId: "m")]) == "登录接口偶尔超时")
        #expect(forkSource(want, sessions: [:]) == "登录接口偶尔超时")
        var untitled = want
        untitled.sourceTitle = ""
        #expect(forkSource(untitled, sessions: [:]) == "另一个会话")
    }

    private func tool(_ title: String, _ kind: PBToolKind = .command, _ more: @escaping (inout PBToolCall) -> Void = { _ in }) -> PBEvent.OneOf_Kind {
        .toolCall(.with { $0.kind = kind; $0.title = title; more(&$0) })
    }

    @Test func foldsToolRunsAndNotesCompaction() throws {
        let events = [
            Mk.ev(1, Mk.user("hi")),
            Mk.ev(2, tool("ls")),
            Mk.ev(3, tool("pwd")),
            Mk.ev(4, Mk.agent("done")),
            Mk.ev(5, .tokenUsage(.with { $0.totalTokens = 1000; $0.lastTotalTokens = 860; $0.contextWindow = 1000 })),
            Mk.ev(6, .settingsChanged(.with { $0.compacted = true })),
            Mk.ev(7, .tokenUsage(.with { $0.totalTokens = 1100; $0.lastTotalTokens = 140; $0.contextWindow = 1000 })),
            Mk.ev(8, Mk.turnEnded(durationMs: 252_000)),
        ]
        let items = rows(events, derive(events))
        guard case .user = items[0], case .tools(let folded) = items[1], case .agent(let said) = items[2], case .note(let note) = items[3],
            case .turnEnd(let end) = items[4]
        else {
            Issue.record("unexpected rows: \(items)")
            return
        }
        #expect(folded.calls.count == 2)
        #expect(said.text == "done")
        #expect(note.text == "上下文已压缩 · 86% → 14%")
        #expect(end.text == "4 分 12 秒 · 100 tokens")
    }

    // Ported from "approval records" in web/src/model.test.ts.
    private func approval(_ id: String, _ command: String, _ digest: String, json: String? = nil) -> PBEvent.OneOf_Kind {
        .approvalRequested(
            .with {
                $0.approvalID = id
                $0.toolName = "Bash"
                $0.command = command
                $0.toolInputJson = json ?? "{\"command\":\"\(command)\"}"
                $0.inputDigest = digest
            })
    }

    private func resolved(_ id: String, _ d: PBApprovalDecision) -> PBEvent.OneOf_Kind {
        .approvalResolved(.with { $0.approvalID = id; $0.decision = d })
    }

    private func call(_ title: String, _ callId: String, _ digest: String, status: String = "completed", tool: String = "Bash", kind: PBToolKind = .command) -> PBEvent.OneOf_Kind {
        .toolCall(.with { $0.title = title; $0.callID = callId; $0.itemType = tool; $0.inputDigest = digest; $0.kind = kind; $0.status = status })
    }

    private func lines(_ list: PBEvent.OneOf_Kind...) -> [String] {
        let events = list.enumerated().map { Mk.ev(UInt64($0.offset + 1), $0.element) }
        return buildChat(events, d: derive(events, agent: .claude), machine: "M").map {
            switch $0 {
            case .note(let n): return "record " + [n.text, n.meta].filter { !$0.isEmpty }.joined(separator: " ")
            case .tool(let t): return "tool " + t.call.title + (t.decision.isEmpty ? "" : " " + t.decision)
            case .tools: return "tools"
            default: return "other"
            }
        }
    }

    @Test func putsDecisionsOnlyOnCallsThatSurelyWereTheirs() {
        // Claude Code runs tools in parallel: approvals wait, an unrelated call
        // finishes first, then the approved ones, out of order.
        let got = lines(
            Mk.turnStarted,
            approval("A", "mkdir x", "dA"),
            approval("B", "rm y", "dB"),
            approval("C", "npm test", "dC"),
            approval("D", "npm test", "dC"),
            resolved("B", .deny),
            resolved("A", .allow),
            resolved("C", .allow),
            resolved("D", .allow),
            call("Read a.go", "r", "dR", tool: "Read", kind: .other),
            call("rm y", "d", "", status: "failed"),  // no digest
            call("mkdir x", "m", "dA"),
            call("npm test", "t1", "dC"),
            call("npm test", "t2", "dC"))
        #expect(
            got == [
                // B has no call it can surely be tied to, and C and D two each.
                "record rm y 已拒绝",
                "record npm test 已允许",
                "record npm test 已允许",
                "tool Read a.go",
                "tool rm y",
                // A's call carries A's result, and A's own line goes.
                "tool mkdir x 已允许",
                "tools",
            ])
    }

    @Test func matchesByTheDaemonsDigestsOnly() {
        let got = lines(
            // Input that decodes differently here than in Go: only the daemon's
            // digest says which call it is.
            approval("A", "a", "db", json: "{\"command\":\"a\",\"COMMAND\":\"b\"}"),
            resolved("A", .allow),
            // A request without a digest (Codex), next to a call
            // whose digest its input would give.
            approval("B", "ls", ""),
            resolved("B", .allow),
            call("b", "1", "db"),
            call("ls", "2", "c7b68ac37f364473e922936708e7f43c293dd07b295171566c07ff5fe024fab9"))
        #expect(got == ["record ls 已允许", "tool b 已允许", "tool ls"])
    }

    @Test func keepsCodexApprovalsAsTheirOwnLines() {
        let got = lines(
            .approvalRequested(.with { $0.approvalID = "A"; $0.toolName = "shell"; $0.command = "mkdir x" }),
            resolved("A", .allow),
            .toolCall(.with { $0.title = "mkdir x"; $0.kind = .command; $0.status = "completed" }))
        #expect(got == ["record mkdir x 已允许", "tool mkdir x"])
    }

    @Test func foldsOnlyCompletedCalls() {
        let events = [
            Mk.ev(1, tool("a")),
            Mk.ev(2, tool("b") { $0.exitCode = 1 }),
            Mk.ev(3, tool("c")),
            Mk.ev(4, tool("d", .fileChange)),
        ]
        let items = rows(events, derive(events))
        guard case .tool(let a) = items[0], case .tool(let b) = items[1], case .tools(let rest) = items[2] else {
            Issue.record("unexpected rows: \(items)")
            return
        }
        #expect(a.call.title == "a")
        #expect(b.call.title == "b")
        #expect(toolsSummary(rest.calls) == "完成了 2 个工具调用")
    }

    @Test func detailsKeepTheOriginalCommand() {
        let c = PBToolCall.with { $0.kind = .command; $0.title = "ls -la /Users/me/Code/foo/"; $0.input = "ls -la /Users/me/Code/foo/" }
        #expect(toolTitle(c, cwd: "/Users/me/Code/foo", home: "/Users/me") == "ls -la .")
        // The row is shortened; the details show the command as it ran.
        #expect(toolRaw(c, cwd: "/Users/me/Code/foo", home: "/Users/me") == "ls -la /Users/me/Code/foo/")
        // Nothing was shortened and the input is the title: nothing more to show.
        var same = c
        same.title = "go test ./..."
        same.input = "go test ./..."
        #expect(toolRaw(same, cwd: "/Users/me/Code/foo", home: "/Users/me") == "")
        var other = same
        other.input = "{\"command\":\"go test ./...\",\"timeout\":60}"
        #expect(toolRaw(other, cwd: "", home: "") == other.input)
    }

    @Test func rawCodexItemTypesAreNotToolTitles() {
        let generic = PBToolCall.with { $0.kind = .other; $0.itemType = "ToolCall"; $0.title = "ToolCall" }
        #expect(toolTitle(generic, cwd: "") == "工具")
        #expect(toolRaw(generic, cwd: "", home: "") == "")
        let command = PBToolCall.with { $0.kind = .command; $0.itemType = "CommandExecution"; $0.title = "CommandExecution" }
        #expect(toolTitle(command, cwd: "") == "命令")
    }

    @Test func namesARunningCallAsWhenItIsDone() throws {
        let events = [
            Mk.ev(1, .toolStarted(.with { $0.title = "/private/tmp/w/hello.txt"; $0.callID = "c"; $0.kind = .fileChange; $0.itemType = "Write" }))
        ]
        let tools = derive(events).tools
        let running = try #require(tools.count == 1 ? tools[0] : nil)
        #expect(toolTitle(running, cwd: "/tmp/w") == "编辑 hello.txt")
        let done = PBToolCall.with { $0.title = "/private/tmp/w/hello.txt"; $0.kind = .fileChange; $0.itemType = "Write" }
        #expect(toolTitle(running, cwd: "/tmp/w") == toolTitle(done, cwd: "/tmp/w"))
    }

    @Test func showsMessagesTheSessionEndedBeforeSending() {
        // The daemon says why; the user's own cancel is not one of these.
        let events = [
            Mk.ev(1, Mk.inputState("mine", .queued)), Mk.ev(2, Mk.inputState("other", .queued)), Mk.ev(3, Mk.inputState("gone", .queued)),
            Mk.ev(4, Mk.inputState("gone", .canceled)), Mk.ev(5, Mk.inputState("mine", .canceled, reason: "exited")),
            Mk.ev(6, Mk.inputState("other", .canceled, reason: "exited")), Mk.ev(7, Mk.exited),
        ]
        let d = derive(events)
        #expect(d.unsent.keys == ["mine", "other"])
        let items = buildChat(events, d: d, machine: "M", textOf: { $0 == "mine" ? "补一个测试" : nil })
        let unsent = items.compactMap { item -> ChatItem.Unsent? in if case .unsent(let u) = item { return u } else { return nil } }
        #expect(unsent == [.init(key: "umine", requestId: "mine", text: "补一个测试"), .init(key: "uother", requestId: "other", text: nil)])
    }

    // --- not in Android's tests ---

    @Test func theWordsOfARowFollowWhoDidWhat() {
        let other = PBOrigin.with { $0.kind = .client; $0.deviceID = "d2" }
        let me = PBOrigin.with { $0.kind = .client; $0.deviceID = "me" }
        let events = [
            Mk.ev(1, .approvalRequested(.with { $0.approvalID = "a"; $0.command = "rm -rf /Users/me/Code/foo/build" })),
            Mk.ev(2, .approvalResolved(.with { $0.approvalID = "a"; $0.decision = .allowAlways; $0.message = "Yes, and don't ask again for commands that start with `rm`" }), origin: other),
            Mk.ev(3, .questionAnswered(.with { $0.questionID = "q"; $0.answers = [.with { $0.answers = ["甲", "乙"] }] }), origin: me),
            Mk.ev(4, .settingsChanged(.with { $0.model = "sonnet"; $0.effort = "high" }), origin: other),
            Mk.ev(5, .settingsChanged(.with { $0.mode = .fullAccess })),
            Mk.ev(6, Mk.turnEnded(.aborted, durationMs: 3_725_000), origin: me),
            Mk.ev(7, .reasoning(.with { $0.text = " \u{3000}\n" })),
            Mk.ev(8, .imageGenerated(.with { $0.revisedPrompt = "a cat"; $0.images = [.with { $0.imageID = "g" }] })),
            Mk.ev(9, Mk.exited, sec: Mk.at(2026, 10, 3, 18, 44) / 1000),
        ]
        let items = buildChat(
            events, d: derive(events), machine: "MacBook", cwd: "/Users/me/Code/foo", now: Mk.at(2026, 10, 4, 9, 0), zone: Mk.shanghai,
            who: { o in o.map { $0.deviceID == "me" ? "本客户端" : "Chrome · a8f2" } })
        #expect(
            items == [
                .note(.init(key: "e1", text: "rm -rf build", icon: .check, tone: 1, meta: "已允许 · `rm` 开头的命令以后不再问 · Chrome · a8f2")),
                .note(.init(key: "e3", text: "已回答：甲、乙", icon: .check, tone: 1)),
                .note(.init(key: "e4", text: "Chrome · a8f2 将模型切到 sonnet · high")),
                .note(.init(key: "e5", text: "审批模式切到了 完全放行")),
                .turnEnd(.init(key: "e6", text: "已中断（本客户端） · 1 小时 2 分")),
                .generated(.init(key: "e8", seq: 8, prompt: "a cat", images: [.with { $0.imageID = "g" }])),
                .turnEnd(.init(key: "e9", text: "10月3日 18:44 结束")),
            ])
        #expect(duration(59_999) == "59 秒")
        #expect(toolsSummary([.with { $0.kind = .command; $0.images = [PBImageRef(), PBImageRef()] }, .with { $0.kind = .command }]) == "运行了 2 条命令 · 2 张截图")
        #expect(toolsSummary([.with { $0.kind = .fileChange }]) == "编辑了 1 个文件")
    }
}

/// Android's app ApprovedBackgroundTest.
@Suite struct ApprovedBackgroundTests {
    @Test func anApprovedCallThatWentToTheBackgroundStaysUnconfirmed() throws {
        let out = "Command running in background with ID: b1."
        let events = [
            Mk.ev(1, Mk.turnStarted),
            Mk.ev(2, .approvalRequested(.with { $0.approvalID = "a"; $0.toolName = "Bash"; $0.inputDigest = "d" })),
            Mk.ev(3, .approvalResolved(.with { $0.approvalID = "a"; $0.decision = .allow })),
            Mk.ev(4, .toolStarted(.with { $0.callID = "c"; $0.itemType = "Bash"; $0.inputDigest = "d"; $0.title = "npm run dev" })),
            Mk.ev(
                5,
                .toolCall(
                    .with {
                        $0.callID = "c"
                        $0.itemType = "Bash"
                        $0.inputDigest = "d"
                        $0.kind = .command
                        $0.title = "npm run dev"
                        $0.output = out
                        $0.status = "completed"
                        $0.background = .requested
                    })),
        ]
        let tools = buildChat(events, d: derive(events, agent: .claude), machine: "M").compactMap { item -> ChatItem.Tool? in
            if case .tool(let t) = item { return t } else { return nil }
        }
        let tool = try #require(tools.count == 1 ? tools[0] : nil)
        #expect(tool.background)
        #expect(tool.decision == "已允许")
    }
}

/// Android's app ChatProjectionTest.
@Suite struct ChatProjectionTests {
    private func numbered(_ kinds: [PBEvent.OneOf_Kind]) -> [PBEvent] { kinds.enumerated().map { Mk.ev(UInt64($0.offset + 1), $0.element) } }

    @Test func appendingMessagesAndStateChangesMatchFullBuild() {
        let events = numbered([
            .toolCall(.with { $0.title = "a"; $0.status = "completed" }),
            .toolCall(.with { $0.title = "b"; $0.status = "completed" }),
            Mk.agent("   "),
            Mk.user("hello"),
            Mk.agent("reply"),
            .reasoning(.with { $0.text = "thought" }),
            .settingsChanged(.with { $0.compacted = true }),
            .tokenUsage(.with { $0.contextWindow = 100; $0.lastTotalTokens = 30 }),
            Mk.turnEnded(durationMs: 1000),
        ])
        var p = ChatProjection()
        for n in 0...events.count {
            let prefix = Array(events.prefix(n))
            let d = derive(prefix)
            #expect(p.update(prefix, d: d, machine: "M") == buildChat(prefix, d: d, machine: "M"))
        }
        let shorter = Array(events.prefix(2))
        #expect(p.update(shorter, d: derive(shorter), machine: "M") == buildChat(shorter, d: derive(shorter), machine: "M"))
    }

    @Test func ordinaryAppendRetainsExistingRowObjects() {
        // Rows are values here: what is kept is that the old rows are not built again, so they stay equal and in place.
        let first = Mk.ev(1, Mk.user("hello"))
        let next = Mk.ev(2, Mk.agent("reply"))
        var p = ChatProjection()
        let old = p.update([first], d: derive([first]), machine: "M")
        let result = p.update([first, next], d: derive([first, next]), machine: "M")
        #expect(old[0] == result[0])
        #expect(old.count == 1)
        #expect(result.count == 2)
    }

    @Test func cliMetadataChangeAndMessageAppendReclassifyExistingInput() {
        let before = [Mk.ev(1, Mk.user("hello")), Mk.ev(2, Mk.inputState("i", .submitted))]
        let after = before + [Mk.ev(3, Mk.agent("reply"))]
        func local(_ items: [ChatItem]) -> Bool? { if case .user(let u) = items.first { return u.local } else { return nil } }
        var p = ChatProjection()
        #expect(local(p.update(before, d: derive(before, agent: .codex), machine: "M", agent: .codex)) == true)
        let d = derive(after, agent: .claude)
        #expect(local(buildChat(after, d: d, machine: "M")) == false)
        #expect(p.update(after, d: d, machine: "M", agent: .claude) == buildChat(after, d: d, machine: "M"))
    }

    @Test func renderingContextChangeRebuildsOldNotesDuringAppend() {
        let before = [Mk.ev(1, .turnContext(.with { $0.model = "old" })), Mk.ev(2, .turnContext(.with { $0.model = "new" }))]
        let after = before + [Mk.ev(3, Mk.agent("reply"))]
        var p = ChatProjection()
        _ = p.update(before, d: derive(before), machine: "Old machine")
        #expect(p.update(after, d: derive(after), machine: "New machine") == buildChat(after, d: derive(after), machine: "New machine"))
    }
}
