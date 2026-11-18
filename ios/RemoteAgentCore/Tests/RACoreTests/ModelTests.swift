import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core ModelTest.
@Suite struct ModelTests {
    @Test func unconfirmedStartupModeIsHidden() {
        for permission in ["", "未知审批模式", "default"] {
            #expect(modeLabel(s { $0.status = .starting; $0.permissionMode = permission }) == nil)
        }
        #expect(modeLabel(s { $0.permissionMode = "未知审批模式" })?.text == "未知审批模式")
    }

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

    @Test func titleInputEnforcesSingleLineAndCodePointLimit() {
        // 60 code points, not UTF-16 units: an emoji counts once and is never cut in half.
        #expect(titleInput(String(repeating: "字", count: 61)) == String(repeating: "字", count: 60))
        #expect(titleInput(String(repeating: "😀", count: 61)) == String(repeating: "😀", count: 60))
        #expect(titleInput(String(repeating: "a", count: 59) + "😀" + "b") == String(repeating: "a", count: 59) + "😀")
        #expect(titleInput("第一行\r\n第二行") == "第一行 第二行")
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
        #expect(protocolMismatch(m(protocolVersion + 1)) == L10n.errorsIosThisAppVersionIsOutOfDateUpdateAnd())
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
        #expect(ago(now - 2 * 3600_000, now: now, zone: zone) == L10n.timeHoursAgo(count: 2))
        #expect(ago(now - 24 * 3600_000, now: now, zone: zone) == L10n.timeYesterday())
        #expect(ago(0, now: now, zone: zone).isEmpty)
        #expect(whenText(now - 10_000, now: now, zone: zone) == L10n.timeJustNow())
        #expect(whenText(now - 180_000, now: now, zone: zone) == L10n.timeMinutesAgo(count: 3))
        #expect(whenText(now - (4 * 3600 + 55 * 60) * 1000, now: now, zone: zone) == "9:05")
        #expect(whenText(now - 24 * 3600_000, now: now, zone: zone) == L10n.timeYesterday())
        #expect(whenText(now - 48 * 3600_000, now: now, zone: zone) == L10n.timeMonthDay(day: 27, month: 9))
        #expect(projectName("/Users/me/Code/foo/") == "foo")
        #expect(plain("修 `bug`") == "修 bug")
    }

    @Test func followsClaudeCodeModes() {
        #expect(modeLabel(s { $0.mode = .auto })?.mode == .auto)
        let own = modeLabel(s { $0.permissionMode = "acceptEdits" })!
        #expect(own.text == "accept edits")
        #expect(own.mode == nil)
        #expect(modeLabel(s { $0.collaborationMode = "plan"; $0.approvalPolicy = "on-request" })?.mode == .plan)
        // The list says which models come without the auto mode; a model it
        // does not know gives no answer.
        let list = [
            PBModelInfo.with { $0.slug = "claude-haiku-4-5-20251001"; $0.autoUnavailable = true },
            PBModelInfo.with { $0.slug = "claude-sonnet-5-5" },
        ]
        #expect(autoUnavailable(list, model: "claude-haiku-4-5-20251001"))
        #expect(!autoUnavailable(list, model: "claude-sonnet-5-5"))
        #expect(!autoUnavailable(list, model: "haiku"))
        #expect(!autoUnavailable(nil, model: "claude-haiku-4-5-20251001"))
        let agyModels = [
            PBModelInfo.with { $0.slug = "gemini-3.1-pro-high"; $0.autoUnavailable = true },
            PBModelInfo.with { $0.slug = "gemini-3-flash"; $0.autoUnavailable = true },
        ]
        #expect(allAutoUnavailable(agyModels))
        #expect(!allAutoUnavailable(list))
        #expect(!allAutoUnavailable([]))
        #expect(!allAutoUnavailable(nil))
        #expect(autoUnavailable(agyModels, model: "gemini-3-flash"))
        #expect(autoUnavailable(agyModels, model: "unknown-model"))
        let models = ["opus", "sonnet", "haiku"]
        #expect(findModel(models, id: "claude-haiku-4-5-20251001") { $0 } == "haiku")
        #expect(findModel(models, id: "sonnet") { $0 } == "sonnet")
        #expect(findModel(models, id: "gpt-6-luna") { $0 } == nil)
        // Started as opus[1m] to name the 1M window: the model is opus.
        #expect(findModel(models, id: "opus[1m]") { $0 } == "opus")
    }

    @Test func aLongModelListShowsThreeAndThePickedOne() {
        let few = ["a", "b", "c", "d", "e"]
        #expect(shownModels(few, picked: nil, all: false) == few)
        let many = few + ["f", "g"]
        #expect(shownModels(many, picked: "b", all: false) == ["a", "b", "c"])
        #expect(shownModels(many, picked: "g", all: false) == ["a", "b", "c", "g"])
        #expect(shownModels(many, picked: "g", all: true) == many)
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
        #expect("Claude-HAIKU-5".jvmContainsIgnoringCase("haiku"))
        #expect(!"claude-sonnet-5-5".jvmContainsIgnoringCase("haiku"))
        // One character at a time: ß is not SS.
        #expect(!"STRASSE".jvmContainsIgnoringCase("straße"))
        #expect("ÉCOLE".jvmContainsIgnoringCase("école"))
        // Code units, not canonical equivalence: a decomposed é is another text.
        #expect(!"e\u{301}cole".jvmContainsIgnoringCase("\u{e9}cole"))
    }

    @Test func simpleCaseMappingsAreTheOnesJavaHas() {
        // İ lowers to i for Java; its full mapping, i with a combining dot, is not the simple one.
        #expect("İstanbul".jvmContainsIgnoringCase("istanbul"))
        #expect("ISTANBUL".jvmContainsIgnoringCase("İstanbul"))
        #expect("ırmak".jvmContainsIgnoringCase("İRMAK"))
        // Compared as Java does, by the lower case of the upper case: ϑ and ϴ both come to θ.
        #expect("\u{3D1}".jvmContainsIgnoringCase("\u{3F4}"))
        // ᾀ has the simple upper case ᾈ besides the full one of two letters.
        #expect("\u{1F80}".jvmContainsIgnoringCase("\u{1F88}"))
        #expect(!"\u{1F80}".jvmContainsIgnoringCase("\u{1F08}\u{399}"))
    }

    @Test func idsAndNamesAreCutByUtf16Units() {
        #expect(machineName(MachineView(id: "0123456789abcdef", online: true, lastSeenMs: 0)) == "01234567")
        #expect(machineName(nil, id: "") == L10n.commonUnknownComputer())
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
        #expect(titleOf(SessionView(id: "s", machineId: "m")) == L10n.listStateIosNewSession())
        #expect(previewOf(s, status: .idle) == L10n.listStateNoMessage())
        #expect(previewOf(s, status: .popup) == L10n.listStatePopup())
        #expect(dateTime(Mk.at(2026, 10, 3, 18, 44), now: Mk.at(2026, 10, 4, 9, 41), zone: Mk.shanghai) == L10n.timeIosDateTime(day: 3, month: 10, time: "18:44"))
        #expect(dateTime(Mk.at(2026, 10, 4, 7, 10), now: Mk.at(2026, 10, 4, 9, 41), zone: Mk.shanghai) == "7:10")
    }
}
