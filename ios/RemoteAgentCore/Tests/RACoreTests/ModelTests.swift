import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core ModelTest.
@Suite struct ModelTests {
    private func s(_ fill: (inout PBSession) -> Void = { _ in }) -> SessionView { Mk.session("s", fill) }

    @Test func statusFollowsThePriorityList() {
        let online = MachineView(id: "m", online: true, lastSeenMs: 0)
        var offline = online
        offline.online = false
        #expect(statusOf(s { $0.status = .exited; $0.pendingApprovals = 1 }, machine: offline) == .exited)
        #expect(statusOf(s { $0.status = .running; $0.pendingApprovals = 1 }, machine: offline) == .offline)
        #expect(statusOf(s { $0.pendingApprovals = 1; $0.pendingQuestions = 1; $0.planOpen = true }, machine: online) == .approval)
        #expect(statusOf(s { $0.pendingQuestions = 1; $0.planOpen = true }, machine: online) == .question)
        #expect(statusOf(s { $0.planOpen = true; $0.status = .blocked }, machine: online) == .plan)
        #expect(statusOf(s { $0.status = .blocked }, machine: online) == .popup)
        #expect(statusOf(s { $0.status = .running }, machine: online) == .running)
        #expect(statusOf(s { $0.status = .idle }, machine: online) == .idle)
        #expect(statusOf(SessionView(id: "s", machineId: "m"), machine: online) == .loading)
    }

    @Test func theStatusesKeepTheirPlacesInThePriorityList() {
        // 需要处理 sorts by these: the order of Android's enum.
        #expect(Status.allCases == [.loading, .exited, .offline, .unlinked, .approval, .question, .plan, .popup, .running, .starting, .idle])
        #expect(Status.allCases.map(\.rawValue) == Array(0...10))
    }

    @Test func renameStartsFromTheTitleAndSendsOnlyAChange() {
        #expect(renameInitial(s { $0.title = " 登录接口偶尔超时 " }) == "登录接口偶尔超时")
        // The "<folder> · <start time>" placeholder is not a title to edit.
        #expect(renameInitial(s { $0.cwd = "/Users/me/Code/billing" }) == "")
        #expect(renameInitial(SessionView(id: "s", machineId: "m")) == "")

        // 60 code points, not UTF-16 units: an emoji counts once and is never cut in half.
        #expect(titleInput(String(repeating: "字", count: 61)) == String(repeating: "字", count: 60))
        #expect(titleInput(String(repeating: "😀", count: 61)) == String(repeating: "😀", count: 60))
        #expect(titleInput(String(repeating: "a", count: 59) + "😀" + "b") == String(repeating: "a", count: 59) + "😀")
        #expect(titleInput("第一行\r\n第二行") == "第一行 第二行")

        #expect(renameTitle(typed: "  登录超时 ", initial: "登录超时") == nil)
        #expect(renameTitle(typed: " 登录超时：限流和重试\u{3000}", initial: "登录超时") == "登录超时：限流和重试")
        // Emptied: back to the automatic title; nothing to do when there was none.
        #expect(renameTitle(typed: "  ", initial: "登录超时") == "")
        #expect(renameTitle(typed: "", initial: "") == nil)
    }

    @Test func aTitleCountsCodePointsWhereSwiftWouldCountClusters() {
        // A family emoji is one Character and seven code points; a flag is two.
        let family = "👨‍👩‍👧‍👦"
        #expect(family.count == 1 && family.unicodeScalars.count == 7)
        let cut = titleInput(String(repeating: family, count: 9))
        #expect(cut.unicodeScalars.count == 60)
        // Runs of line breaks, mixed, are one space each.
        #expect(titleInput("a\n\r\n\rb\nc") == "a b c")
    }

    @Test func forkNeedsAnIdleOrEndedSessionWithAMessage() {
        #expect(forkBlocked(status: .idle, running: false, sent: true) == nil)
        #expect(forkBlocked(status: .exited, running: false, sent: true) == nil)
        for st in [Status.running, .approval, .question] { #expect(forkBlocked(status: st, running: false, sent: true) == "这一轮结束后可用") }
        // The events show a turn before the session's status does.
        #expect(forkBlocked(status: .idle, running: true, sent: true) == "这一轮结束后可用")
        for st in [Status.plan, .popup] { #expect(forkBlocked(status: st, running: false, sent: true) == "处理完等待事项后可用") }
        #expect(forkBlocked(status: .starting, running: false, sent: false) == "启动完成后可用")
        #expect(forkBlocked(status: .idle, running: false, sent: false) == "发过消息后可用")
        #expect(forkBlocked(status: .exited, running: false, sent: false) == "发过消息后可用")
        // What it waits for comes before the missing message.
        #expect(forkBlocked(status: .running, running: true, sent: false) == "这一轮结束后可用")

        let message = [Mk.ev(1, Mk.user("hi"))]
        #expect(sentMessage(s { $0.title = "登录接口偶尔超时" }, events: []))
        #expect(sentMessage(s(), events: message))
        #expect(!sentMessage(s { $0.cwd = "/Users/me/Code/foo" }, events: [Mk.ev(1, .turnContext(.with { $0.model = "m" }))]))
        #expect(!sentMessage(SessionView(id: "s", machineId: "m"), events: []))

        #expect(forked(s { $0.forkedFrom = "a1" }))
        #expect(!forked(s()))
        #expect(!forked(SessionView(id: "s", machineId: "m")))

        let made = PBCommandResult.with { $0.forkSession = .with { $0.session = .with { $0.id = "new" } } }
        #expect(forkedSession(made) == "new")
        #expect(forkedSession(PBCommandResult()) == nil)
        #expect(forkedSession(nil) == nil)
    }

    @Test func aForkCarriesNothingPendingFromItsSource() {
        // The copy ends in the middle of things only if the daemon's own trimming missed something: nothing of it runs here.
        let d = derive([
            Mk.ev(1, Mk.turnStarted),
            Mk.ev(2, .approvalRequested(.with { $0.approvalID = "a" })),
            Mk.ev(3, .toolStarted(.with { $0.callID = "c"; $0.title = "go test" })),
            Mk.ev(4, .sessionForked(.with { $0.sourceSessionID = "src"; $0.sourceTitle = "登录超时" })),
        ])
        #expect(!d.running)
        #expect(d.approvals.isEmpty)
        #expect(d.tools.isEmpty)
        #expect(d.unfinished == [3])
    }

    @Test func derivesPendingItemsAndInputs() {
        let d = derive([
            Mk.ev(1, Mk.turnStarted),
            Mk.ev(2, .approvalRequested(.with { $0.approvalID = "a1"; $0.command = "go test" })),
            Mk.ev(3, .approvalRequested(.with { $0.approvalID = "a2" })),
            Mk.ev(4, .approvalResolved(.with { $0.approvalID = "a1" })),
            Mk.ev(5, Mk.inputState("r1", .submitted)),
            Mk.ev(6, Mk.user("from the phone")),
            Mk.ev(7, Mk.user("typed locally")),
        ])
        #expect(d.approvals.keys == ["a2"])
        #expect(d.resolved["a1"] != nil)
        #expect(d.inputs["r1"] == .submitted)
        #expect(d.typedLocally == [7])
        #expect(d.running)
    }

    @Test func pairsToolsById() {
        let d = derive([
            Mk.ev(1, .toolStarted(.with { $0.title = "a"; $0.callID = "x" })),
            Mk.ev(2, .toolStarted(.with { $0.title = "b"; $0.callID = "y" })),
            Mk.ev(3, .toolCall(.with { $0.callID = "y" })),
        ])
        #expect(d.tools.map(\.title) == ["a"])
    }

    @Test func notesModeChangesMadeInTheTerminal() {
        func context(_ reviewer: String) -> PBEvent.OneOf_Kind {
            .turnContext(.with { $0.model = "m"; $0.effort = "low"; $0.approvalPolicy = "on-request"; $0.approvalsReviewer = reviewer })
        }
        let d = derive([Mk.ev(1, context("user")), Mk.ev(2, context("auto_review"))])
        #expect(d.terminalChanges[2] == "模式切到了 自动")
    }

    // The web's "notes a model changed in the terminal after a switch from here".
    @Test func notesAModelChangedInTheTerminalAfterASwitchFromHere() {
        func context(_ model: String, _ effort: String) -> PBEvent.OneOf_Kind {
            .turnContext(.with { $0.model = model; $0.effort = effort; $0.approvalPolicy = "on-request"; $0.approvalsReviewer = "user" })
        }
        let chosen = PBEvent.OneOf_Kind.settingsChanged(.with { $0.model = "gpt-6-sol"; $0.effort = "high" })
        let d = derive([
            Mk.ev(1, context("gpt-6-astra", "medium")),
            Mk.ev(2, chosen),
            // Changed again at the computer before the next turn.
            Mk.ev(3, context("gpt-6-luna", "low")),
            Mk.ev(4, chosen),
            Mk.ev(5, context("gpt-6-sol", "high")),  // as chosen here: nothing to note
        ])
        #expect(d.terminalChanges == [3: "模型切到了 gpt-6-luna · low"])
    }

    // The web's "notes a switch back made in the terminal after a switch from here".
    @Test func notesASwitchBackMadeInTheTerminalAfterASwitchFromHere() {
        func context(_ model: String) -> PBEvent.OneOf_Kind {
            .turnContext(.with { $0.model = model; $0.effort = "medium"; $0.approvalPolicy = "on-request"; $0.approvalsReviewer = "user" })
        }
        let d = derive([
            Mk.ev(1, context("gpt-6-astra")),
            Mk.ev(2, .settingsChanged(.with { $0.model = "gpt-6-sol"; $0.effort = "medium" })),
            Mk.ev(3, context("gpt-6-astra")),  // switched back at the computer before the next turn
            Mk.ev(4, context("gpt-6-sol")),  // and to Sol again there
        ])
        #expect(d.terminalChanges == [3: "模型切到了 gpt-6-astra · medium", 4: "模型切到了 gpt-6-sol · medium"])
    }

    private func claudeContext(_ permission: String, _ model: String = "claude-haiku-4-5-20251001") -> PBEvent.OneOf_Kind {
        .turnContext(.with { $0.permissionMode = permission; $0.model = model })
    }

    // The web's "notes Claude Code mode changes made in the terminal, not model aliases set here".
    @Test func notesClaudeCodeModesOutsideTheFourAndNotAModelAliasSetHere() {
        let d = derive(
            [
                Mk.ev(1, claudeContext("default")),
                Mk.ev(2, claudeContext("acceptEdits", "")),  // Shift+Tab in the terminal, read off the screen
                Mk.ev(3, .settingsChanged(.with { $0.model = "sonnet" })),  // switched here, by alias
                Mk.ev(4, claudeContext("acceptEdits", "claude-sonnet-5-5")),
                Mk.ev(5, claudeContext("plan", "claude-sonnet-5-5")),
                Mk.ev(6, claudeContext("default", "claude-sonnet-5-5")),
            ], agent: .claude)
        #expect(d.terminalChanges == [2: "模式切到了 accept edits", 5: "模式切到了 计划", 6: "模式切到了 询问"])
    }

    // The web's "notes nothing when a mode-only context carries the switched model".
    @Test func notesNothingWhenAModeOnlyContextCarriesTheSwitchedModel() {
        let d = derive(
            [
                Mk.ev(1, claudeContext("default")),
                Mk.ev(2, .settingsChanged(.with { $0.model = "sonnet"; $0.effort = "medium" })),
                Mk.ev(3, claudeContext("acceptEdits", "claude-sonnet-5-5")),  // mode changed before the next reply
                Mk.ev(4, claudeContext("acceptEdits", "claude-sonnet-5-5")),
                Mk.ev(5, claudeContext("acceptEdits", "claude-opus-5-5")),  // then changed at the computer
                Mk.ev(6, claudeContext("default", "claude-opus-5-5")),  // and the mode back
            ], agent: .claude)
        #expect(d.terminalChanges == [3: "模式切到了 accept edits", 5: "模型切到了 claude-opus-5-5", 6: "模式切到了 询问"])
    }

    @Test func aModelIsTheOneChosenBySlugOrAlias() {
        #expect(modelIs("gpt-6-sol", "gpt-6-sol"))
        #expect(modelIs("claude-Sonnet-5-5", "sonnet"))
        #expect(!modelIs("claude-opus-5-5", "sonnet"))
        #expect(!modelIs("", "sonnet"))
        #expect(!modelIs("sonnet", ""))
    }

    @Test func saysWhichSideToUpdateWhenProtocolVersionsDiffer() {
        func m(_ v: UInt32) -> MachineView {
            MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.hostname = "mac.local"; $0.protocolVersion = v })
        }
        #expect(protocolMismatch(m(protocolVersion)) == nil)
        #expect(protocolMismatch(m(protocolVersion - 1)) == "mac 上的 remote-agent 版本较旧，请在电脑上更新后再试")
        #expect(protocolMismatch(m(protocolVersion + 1)) == "这个 App 版本较旧，请更新后再试")
        // Not read yet: the daemon answers for itself.
        #expect(protocolMismatch(MachineView(id: "m", online: true, lastSeenMs: 0)) == nil)
        #expect(protocolMismatch(nil) == nil)
    }

    @Test func namesAgentsAndTimes() {
        #expect(agentName(.unspecified) == "Codex")
        #expect(agentName(.claude) == "Claude Code")
        #expect(machineAgents(MachineView(id: "m", online: true, lastSeenMs: 0, meta: PBMachineMeta())) == [.codex])
        let both = PBMachineMeta.with { $0.agents = [.with { $0.agent = .codex }, .with { $0.agent = .claude }] }
        #expect(machineAgents(MachineView(id: "m", online: true, lastSeenMs: 0, meta: both)) == [.codex, .claude])
        let zone = Mk.shanghai
        let now = Mk.at(2026, 9, 29, 14, 0)
        #expect(whenText(now - 10_000, now: now, zone: zone) == "刚刚")
        #expect(whenText(now - 180_000, now: now, zone: zone) == "3 分钟前")
        #expect(whenText(now - (4 * 3600 + 55 * 60) * 1000, now: now, zone: zone) == "9:05")
        #expect(whenText(now - 24 * 3600_000, now: now, zone: zone) == "昨天")
        #expect(whenText(now - 48 * 3600_000, now: now, zone: zone) == "9月27日")
        #expect(projectName("/Users/me/Code/foo/") == "foo")
        #expect(plain("修 `bug`") == "修 bug")
    }

    @Test func followsClaudeCodeModes() {
        #expect(modeLabel(s { $0.mode = .auto })?.mode == .auto)
        let own = modeLabel(s { $0.permissionMode = "acceptEdits" })!
        #expect(own.text == "acceptEdits")
        #expect(own.mode == nil)
        #expect(modeLabel(s { $0.collaborationMode = "plan"; $0.approvalPolicy = "on-request" })?.mode == .plan)
        #expect(noAutoMode(s { $0.agent = .claude; $0.model = "claude-haiku-4-5-20251001" }))
        let models = ["opus", "sonnet", "haiku"]
        #expect(findModel(models, id: "claude-haiku-4-5-20251001") { $0 } == "haiku")
        #expect(findModel(models, id: "sonnet") { $0 } == "sonnet")
        #expect(findModel(models, id: "gpt-6-luna") { $0 } == nil)
        // Started as opus[1m] to name the 1M window: the model is opus.
        #expect(findModel(models, id: "opus[1m]") { $0 } == "opus")
    }

    @Test func aLateConfirmationIsForTheMessageRecordedAfterIt() {
        let client = PBOrigin.with { $0.kind = .client; $0.deviceID = "x" }
        let events = [
            Mk.ev(1, Mk.inputState("a", .queued) { $0.text = "sent from the phone" }),
            Mk.ev(2, Mk.inputState("a", .unconfirmed)),
            Mk.ev(3, Mk.user("typed at the computer")),
            Mk.ev(4, Mk.inputState("a", .submitted)),
            Mk.ev(5, .userMessage(.with { $0.text = "sent from the phone"; $0.requestID = "a" }), origin: client),
            Mk.ev(6, Mk.user("typed later")),
        ]
        #expect(derive(events, agent: .claude).typedLocally == [3, 6])
        #expect(derive(events, agent: .codex).typedLocally == [3, 6])
        #expect(derive(events, agent: .claude).inputs["a"] == .submitted)
    }

    @Test func claudeCodeRecordsTheMessageBeforeItsState() {
        let events = [Mk.ev(1, Mk.user("hi")), Mk.ev(2, Mk.inputState("r", .submitted))]
        #expect(derive(events, agent: .claude).typedLocally == [])
        #expect(derive(events, agent: .codex).typedLocally == [1])
    }

    @Test func warnsAboutClaudeCodesTrustDialogOnly() {
        #expect(popupHint(agent: .claude, detail: "trust folder") == "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎")
        #expect(popupHint(agent: .codex, detail: "trust folder") == "")
        #expect(popupHint(agent: .unspecified, detail: "trust folder") == "")
        #expect(popupHint(agent: .claude, detail: "hooks review") == "")
    }

    @Test func readsTheDaemonsDefaultModePerCli() {
        let s = PBDaemonSettings.with { $0.defaultMode = .fullAccess; $0.claude = .with { $0.mode = .auto } }
        #expect(defaultMode(s, agent: .codex) == .full)
        #expect(defaultMode(s, agent: .unspecified) == .full)
        #expect(defaultMode(s, agent: .claude) == .auto)
        #expect(defaultMode(PBDaemonSettings(), agent: .codex) == nil)
    }

    // --- not in Android's tests: what the JVM does to text, kept the same here ---

    @Test func previewsDropMarksTheWayJavasRegexDoes() {
        #expect(plain("## 标题\n- 一\n> 引用\n**粗** [链接](http://x) ```go\ncode```") == "标题 一 引用 粗 链接 code")
        // \s is ASCII only: an ideographic space inside is kept, the ones at the ends go with trim().
        #expect(plain("\u{3000}a\u{3000}\u{3000}b \t c\u{3000}") == "a\u{3000}\u{3000}b c")
        // \d is ASCII only: a full-width digit is no image number.
        #expect(plain("[Image #１] x") == "[Image #１] x")
        // A line mark only counts at the start of a line, and \r\n is one line break.
        #expect(plain("a - b\r\n- c") == "a - b c")
    }

    @Test func aModelNameMatchesIgnoringCaseTheWayKotlinDoes() {
        #expect(noAutoMode(s { $0.agent = .claude; $0.model = "Claude-HAIKU-5" }))
        #expect(!noAutoMode(s { $0.agent = .codex; $0.model = "haiku" }))
        // One character at a time: ß is not SS.
        #expect(!"STRASSE".jvmContainsIgnoringCase("straße"))
        #expect("ÉCOLE".jvmContainsIgnoringCase("école"))
        // Code units, not canonical equivalence: a decomposed é is another text.
        #expect(!"e\u{301}cole".jvmContainsIgnoringCase("\u{e9}cole"))
    }

    @Test func idsAndNamesAreCutByUtf16Units() {
        #expect(shortId("0123456789abcdef") == "cdef")
        #expect(machineName(MachineView(id: "0123456789abcdef", online: true, lastSeenMs: 0)) == "01234567")
        #expect(machineName(nil, id: "") == "未知电脑")
        #expect(machineName(MachineView(id: "x", online: true, lastSeenMs: 0, meta: .with { $0.hostname = "mac.local" })) == "mac")
        // An emoji is two units; a cut through one loses the half Swift cannot hold.
        #expect("ab😀cd".jvmTake(3) == "ab")
        #expect("ab😀cd".jvmTake(4) == "ab😀")
        #expect("ab😀cd".jvmTakeLast(3) == "cd")
        #expect("ab😀cd".jvmLength == 6)
        #expect(avatarIndex("/Users/me/Code/foo") == Int(UInt32(truncatingIfNeeded: "/Users/me/Code/foo".utf16.reduce(0) { ($0 &* 31 &+ UInt64($1)) & 0xffff_ffff }) % 4))
        #expect(avatarIndex("") == 0)
        #expect(avatarIndex("a") == 1)
    }

    /// What the app's screens trim, test for blankness and split by, as Kotlin does on Android.
    @Test func trimmingBlanknessAndLinesAreTheJvms() {
        #expect("  \t改一下 \n".jvmTrim() == "改一下")
        #expect("  改一下 \n\n".jvmTrimEnd() == "  改一下")
        // A non-breaking space is whitespace to Kotlin; a zero-width space is not.
        #expect("\u{a0}好\u{a0}".jvmTrim() == "好")
        #expect("\u{200b}好".jvmTrim() == "\u{200b}好")
        #expect("".jvmIsBlank && " \n\t\u{a0}".jvmIsBlank && !" a ".jvmIsBlank && !"\u{200b}".jvmIsBlank)
        #expect("".jvmTrim().isEmpty && "   ".jvmTrimEnd().isEmpty)
        // lines(): \r\n, \n and \r end a line, nothing else does, and there is always a first line.
        #expect("a\r\nb\nc\rd".jvmLines() == ["a", "b", "c", "d"])
        #expect("".jvmLines() == [""] && "a\n".jvmLines() == ["a", ""])
        #expect("a\u{2028}b\u{85}c".jvmLines() == ["a\u{2028}b\u{85}c"])
    }

    @Test func contextWindowsAreWrittenShort() {
        #expect(contextText(tokens: 0) == nil)
        #expect(contextText(tokens: 200_000) == "200k 上下文")
        #expect(contextText(tokens: 1_000_000) == "1M 上下文")
        #expect(contextText(tokens: 1_050_000) == "1M 上下文")
        #expect(contextText(tokens: 1_500_000) == "1.5M 上下文")
        #expect(contextPercent(s { $0.contextTokens = 50; $0.contextWindow = 200 }) == 25)
        #expect(contextPercent(s { $0.contextTokens = 500; $0.contextWindow = 200 }) == 100)
        #expect(contextPercent(s { $0.contextTokens = 50 }) == nil)
        #expect(minutesLeft(deadlineMs: 61_000, now: 0) == 2)
        #expect(minutesLeft(deadlineMs: 0, now: 5) == 0)
    }

    @Test func optionsAndBackgroundInputsAreReadByPattern() {
        #expect(optionLabel("Use Postgres (Recommended)").label == "Use Postgres")
        #expect(optionLabel("Use Postgres (recommended) ").recommended)
        #expect(optionLabel("用 Postgres（推荐）") == ("用 Postgres（推荐）", false))
        #expect(optionLabel("用 Postgres (推荐)") == ("用 Postgres", true))
        #expect(backgroundInput("{\"run_in_background\" :\ttrue}"))
        // \s is ASCII: a no-break space is not one.
        #expect(!backgroundInput("{\"run_in_background\":\u{a0}true}"))
    }
}

/// Android's TitleTest (in ImagesTest.kt).
@Suite struct TitleTests {
    @Test func titlesAndPreviewsLeaveOutImagePlaceholders() {
        let s = Mk.session { $0.title = "[Image #1]Which half is red?"; $0.preview = "[Image #1] [Image #2] look" }
        #expect(titleOf(s) == "Which half is red?")
        #expect(previewOf(s, status: .idle) == "look")
    }

    @Test func aSessionWithoutATitleIsNamedByItsFolderAndStart() {
        let s = Mk.session { $0.cwd = "/Users/me/Code/foo"; $0.createdAt = Mk.ts(Mk.at(2026, 10, 4, 9, 5)) }
        #expect(titleOf(s, zone: Mk.shanghai) == "foo · 9:05")
        #expect(titleOf(Mk.session { $0.cwd = "/Users/me/Code/foo" }) == "foo")
        #expect(titleOf(SessionView(id: "s", machineId: "m")) == "新会话")
        #expect(previewOf(s, status: .idle) == "还没发消息")
        #expect(previewOf(s, status: .popup) == "终端里有弹窗")
        #expect(dateTime(Mk.at(2026, 10, 3, 18, 44), now: Mk.at(2026, 10, 4, 9, 41), zone: Mk.shanghai) == "10月3日 18:44")
        #expect(dateTime(Mk.at(2026, 10, 4, 7, 10), now: Mk.at(2026, 10, 4, 9, 41), zone: Mk.shanghai) == "7:10")
    }
}
