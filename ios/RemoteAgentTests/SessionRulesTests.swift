import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// A value the checks below may change in place.
private final class Ref<Value> {
    var v: Value
    init(_ v: Value) { self.v = v }
}

extension Ref<ChatScroll> {
    func changed(rows: Int) -> Bool { v.contentChanged(rows: rows) }
}

extension Ref<LiveAnnouncer> {
    func announce(last: ChatItem?, dock: [DockItem], agent: String, now: Int64, initial: Bool) -> String? {
        v.announce(last: last, dock: dock, agent: agent, now: now, initial: initial)
    }
}

@Suite struct ChatScrollTests {
    @Test func aChatAtItsEndStaysThereAsContentArrives() {
        let scroll = Ref(ChatScroll())
        #expect(scroll.changed(rows: 10))
        // The list has not laid the new rows out yet: it is not at the bottom, and nobody dragged it there.
        scroll.v.observe(userDragging: false, atBottom: false, rows: 10)
        #expect(scroll.v.following)
        #expect(scroll.changed(rows: 11))
        #expect(scroll.v.fresh(rows: 11) == 0)
    }

    @Test func draggingAwayStopsFollowingAndCountsWhatArrives() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 10)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 10)
        #expect(!scroll.v.following)
        // A reply grows, rows come in, the keyboard comes up: nothing moves.
        #expect(!scroll.changed(rows: 10))
        #expect(!scroll.changed(rows: 13))
        #expect(scroll.v.fresh(rows: 13) == 3)
        scroll.v.observe(userDragging: false, atBottom: false, rows: 13)
        #expect(!scroll.v.following, "letting go does not start it again")
    }

    @Test func reachingTheEndStartsFollowingAgain() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 5)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 5)
        _ = scroll.changed(rows: 9)
        scroll.v.observe(userDragging: true, atBottom: true, rows: 9)
        #expect(scroll.v.following && scroll.v.fresh(rows: 9) == 0)
        #expect(scroll.changed(rows: 10))
    }

    @Test func askingForTheEndFollowsAgainAtOnce() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 5)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 5)
        _ = scroll.changed(rows: 8)
        scroll.v.jumpToEnd(rows: 8)
        #expect(scroll.v.following)
        // Still on its way there.
        scroll.v.observe(userDragging: false, atBottom: false, rows: 8)
        #expect(scroll.v.following && scroll.v.fresh(rows: 8) == 0)
    }

    @Test func rowsThatFoldAwayAreNeverCountedAsNew() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 20)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 20)
        #expect(scroll.v.fresh(rows: 18) == 0)
    }

    @MainActor @Test func theTrackerFollowsGrowthAndTheKeyboardOnlyAtTheEnd() {
        let tracker = ChatScrollTracker()
        _ = tracker.floor(700)
        _ = tracker.count(10)
        #expect(tracker.content(height: 2_000, endY: 700))
        #expect(!tracker.content(height: 2_000, endY: 700), "nothing changed")
        // A reply grows by 300 pt: its end is now under the bar.
        #expect(tracker.content(height: 2_300, endY: 1_000))
        _ = tracker.content(height: 2_300, endY: 700)
        // The keyboard comes up.
        #expect(tracker.floor(400))
        _ = tracker.content(height: 2_300, endY: 400)
        #expect(tracker.fresh == 0 && !tracker.away)

        // The user drags back by a screen.
        tracker.dragging = true
        #expect(!tracker.content(height: 2_300, endY: 1_200))
        tracker.dragging = false
        #expect(tracker.away)
        #expect(!tracker.content(height: 2_600, endY: 1_500), "a growing reply moves nothing for someone reading back")
        #expect(!tracker.floor(700), "nor does the keyboard going away")
        #expect(!tracker.count(13))
        #expect(tracker.fresh == 3)
        tracker.jump()
        #expect(tracker.fresh == 0)
        #expect(tracker.content(height: 2_700, endY: 1_600))
    }
}

@Suite struct SessionWordsTests {
    private func pending(mode: PBSendMode = .steer, paused: Bool = false, held: Bool = false) -> PendingInput {
        PendingInput(id: "q", text: "t", mode: mode, paused: paused, held: held, origin: nil)
    }

    @Test func aWaitingMessageSaysWhatItWaitsFor() {
        func label(_ p: PendingInput, cancel: SessionStore.CancelState? = nil, resuming: Bool = false, failed: Bool = false, starting: String? = nil) -> String {
            pendingLabel(p, cancel: cancel, resuming: resuming, resumeFailed: failed, starting: starting)
        }
        #expect(label(pending()) == "插入这一轮")
        #expect(label(pending(mode: .queue)) == "这一轮结束后发")
        #expect(label(pending(held: true)) == "插入这一轮")
        #expect(label(pending(mode: .queue, paused: true)) == "已暂停")
        #expect(label(pending(), starting: "Codex") == "等 Codex 就绪后发出")
        #expect(label(pending(mode: .queue), starting: "Codex") == "这一轮结束后发")
        #expect(label(pending(paused: true), resuming: true) == "正在发送…")
        #expect(label(pending(paused: true), failed: true) == "发送没成功，仍已暂停")
        #expect(label(pending(paused: true), cancel: .canceling, resuming: true) == "正在取消…")
        #expect(label(pending(), cancel: .failed("x")) == "取消没成功，仍在排队")
    }

    @Test func aWaitingMessageNamesAnotherSenderOrItsPlace() {
        #expect(pendingSuffix(pending(mode: .queue), origin: nil, index: 2, of: 2) == " · 第 2 条")
        #expect(pendingSuffix(pending(mode: .queue), origin: "我的 iPad", index: 2, of: 2) == " · 我的 iPad")
        #expect(pendingSuffix(pending(), origin: nil, index: 1, of: 2).isEmpty)
        // Its place is said only while more than one waits.
        #expect(pendingSuffix(pending(mode: .queue), origin: nil, index: 1, of: 1).isEmpty)
    }

    @Test func moreThanThreeWaitingMessagesFoldToTwo() {
        let four = (0..<4).map { PendingInput(id: "\($0)", text: "", mode: .queue, paused: false, held: false, origin: nil) }
        #expect(shownPending(four, expanded: false).count == 2)
        #expect(shownPending(four, expanded: true).count == 4)
        #expect(shownPending(Array(four.prefix(3)), expanded: false).count == 3)
    }

    @Test func aMessageOnItsWaySaysHowSendingStands() {
        func note(_ state: Outgoing.State, _ input: PBInputStatus? = nil, failure: String? = nil, error: String = "原因") -> OutgoingNote {
            outgoingNote(state: state, error: error, input: input, failure: failure, agent: .claude)
        }
        #expect(note(.sending) == OutgoingNote(caption: "发送中…", sending: true))
        #expect(note(.sent) == OutgoingNote(caption: "已送达电脑"))
        // Did not go: red, with a short reason.
        #expect(note(.failed) == OutgoingNote(caption: "没发出去：原因", tone: .error, failed: true, actions: [.retry, .remove]))
        #expect(note(.sent, .failed, failure: "input_changed") == OutgoingNote(
            caption: "没发出去：无法确认终端输入框的内容", tone: .error, failed: true, actions: [.putBack]))
        // May not have gone: orange, with what could be done before.
        #expect(note(.unknown) == OutgoingNote(caption: "可能没发出去：原因", tone: .warn, failed: true, actions: [.retry, .remove]))
        // The error's own 「不确定电脑有没有执行」 is said by 「可能没发出去」 already.
        #expect(note(.unknown, error: "不确定电脑有没有执行：连接中途断了").caption == "可能没发出去：连接中途断了")
        #expect(note(.sent, .needsTerminal, failure: "write_failed") == OutgoingNote(
            caption: "可能没发出去：写入终端失败", tone: .warn, failed: true, actions: [.terminal, .putBack]))
        #expect(note(.sent, .unconfirmed) == OutgoingNote(
            caption: "可能没发出去：已送到终端但没确认", tone: .warn, actions: [.remove]))
        // The daemon has it queued and this client's own try was unsure: it did arrive, and nothing is drawn as failed.
        #expect(!note(.unknown, .queued).failed)
    }

    @Test func aMessageIsShownUnderTheChatUntilTheEventsHaveIt() {
        #expect(outgoingShown(input: nil))
        #expect(!outgoingShown(input: .queued), "the pending bubble shows it from here")
        #expect(!outgoingShown(input: .heldByCli))
        // Whatever the try from here ended with, lost answer included: the CLI recorded the message.
        #expect(!outgoingShown(input: .submitted))
        #expect(!outgoingShown(input: .canceled))
        #expect(outgoingShown(input: .failed))
        #expect(outgoingShown(input: .needsTerminal))
        #expect(outgoingShown(input: .unconfirmed))
    }

    @Test func sendingNeedsTextAConnectionAndAComputerThatSpeaksTheSameProtocol() {
        func can(_ text: String, online: Bool = true, keeping: Bool = false, images: Bool = false, ready: Bool = true, refused: Bool = false) -> Bool {
            composerCanSend(text: text, online: online, keeping: keeping, hasImages: images, imagesReady: ready, refused: refused)
        }
        #expect(can("你好"))
        #expect(!can("  \n"))
        #expect(!can("你好", online: false), "the draft stays and sending waits")
        #expect(!can("你好", refused: true))
        #expect(!can("你好", keeping: true))
        #expect(can("", images: true))
        #expect(!can("带图", images: true, ready: false), "never a message with some of its images")
    }

    @Test func aLeadingSlashIsWarnedAbout() {
        #expect(slashWarning("/compact"))
        #expect(slashWarning("  /model"))
        #expect(!slashWarning("a/b"))
        #expect(!slashWarning(""))
    }

    @Test func theMenuStartsWithTheSettingsAsNameAndValue() {
        let full = ModeLabel(text: "完全放行", mode: .full)
        #expect(menuSettings(mode: full, model: "GPT-6", effort: "high", percent: 86) == [
            MenuSetting(text: "审批模式", value: "完全放行", danger: true, opens: .mode),
            MenuSetting(text: "模型", value: "GPT-6 · high", opens: .model),
            MenuSetting(text: "上下文", value: "86%", warn: true, opens: .model),
        ])
        // A mode that could not be read has no row, a model not reported yet is 会话设置, an unknown context is left out.
        #expect(menuSettings(mode: nil, model: "", effort: "", percent: nil) == [MenuSetting(text: "会话设置", value: "", opens: .model)])
        #expect(menuSettings(mode: ModeLabel(text: "询问", mode: .ask), model: "opus", effort: "", percent: 12).map(\.value) == ["询问", "opus", "12%"])
    }

    @Test func theBarsSecondLineIsTheStateThenTheMode() {
        let ask = ModeLabel(text: "询问", mode: .ask)
        let full = ModeLabel(text: "完全放行", mode: .full)
        #expect(sessionSubtitle(status: .running, mode: ask) == SessionSubtitle(status: "运行中", mode: "询问", danger: false))
        #expect(sessionSubtitle(status: .approval, mode: full) == SessionSubtitle(status: "等待审批", mode: "完全放行", danger: true))
        #expect(sessionSubtitle(status: .approval, mode: full).text == "等待审批 · 完全放行")
        #expect(sessionSubtitle(status: .exited, mode: full).text == "已结束")
        #expect(sessionSubtitle(status: .starting, mode: nil).text == "启动中")
        #expect(sessionSubtitle(status: .starting, mode: ask).text == "启动中 · 询问")
        // A mode that could not be read is not written at all.
        #expect(sessionSubtitle(status: .idle, mode: nil) == SessionSubtitle(status: "空闲", mode: "", danger: false))
        #expect(sessionSubtitle(status: .running, mode: nil).text == "运行中")
        #expect(sessionSubtitle(status: .idle, mode: ModeLabel(text: "acceptEdits", mode: nil)).text == "空闲 · acceptEdits")
        #expect(sessionSubtitle(status: .unlinked, mode: ask).text == "未连接 · 询问")
    }

    @Test func anEndedSessionSaysHowItEndedAndWhetherItCanBeResumed() {
        #expect(endedText(detail: "stopped", unlinked: false, machineOnline: true, machine: "MacBook")
            == "会话是被手动结束的。恢复会在 MacBook 上用同样的设置重新打开它，聊天记录都在。")
        #expect(endedText(detail: "", unlinked: false, machineOnline: false, machine: "iMac") == "tmux 会话已经不在了。iMac 不在线，连上后才能恢复。")
        #expect(endedText(detail: "", unlinked: true, machineOnline: true, machine: "iMac") == "tmux 会话已经不在了。连上服务器后才能恢复。")
        #expect(offlineText(lastSeen: "").hasPrefix("可能是合盖睡眠"))
        #expect(offlineText(lastSeen: "3 分钟前").hasPrefix("3 分钟前断开，可能是"))
    }

    @Test func anEndedSessionSaysWhenItsLoginShellCouldNotStartTheCLI() {
        #expect(exitReason("the login shell could not find codex in PATH")
            == "电脑上的登录 shell 没能启动 CLI（the login shell could not find codex in PATH）。请检查电脑上的 shell 配置，比如 ~/.zshrc。")
        #expect(exitReason("the login shell did not start the CLI")
            == "电脑上的登录 shell 没能启动 CLI（the login shell did not start the CLI）。请检查电脑上的 shell 配置，比如 ~/.zshrc。")
        // The one the daemon ends itself: the shell may be waiting for input.
        #expect(exitReason("the login shell did not start the CLI within 1m0s")
            == "电脑上的登录 shell 没能启动 CLI（the login shell did not start the CLI within 1m0s），可能卡在等待输入。请检查电脑上的 shell 配置，比如 ~/.zshrc。")
        #expect(endedText(detail: "the login shell did not start the CLI", unlinked: false, machineOnline: true, machine: "MacBook")
            == "电脑上的登录 shell 没能启动 CLI（the login shell did not start the CLI）。请检查电脑上的 shell 配置，比如 ~/.zshrc。恢复会在 MacBook 上用同样的设置重新打开它，聊天记录都在。")
        // Every other detail keeps the words the card had before.
        #expect(exitReason("stopped") == "会话是被手动结束的。")
        #expect(exitReason("tmux session gone") == "tmux 会话已经不在了。")
    }

    @Test func voiceOverIsToldOfRepliesAndOfWhatWaitsButNotOfEverything() {
        let say = Ref(LiveAnnouncer())
        let reply = { (n: Int) in ChatItem.agent(.init(key: "e\(n)", text: "好")) }
        let approval = DockItem.approval(.with {
            $0.approvalID = "A"
            $0.command = "rm -rf build"
        })
        // What is there when the page opens is not announced.
        #expect(say.announce(last: reply(1), dock: [approval], agent: "Codex", now: 0, initial: true) == nil)
        #expect(say.announce(last: reply(1), dock: [approval], agent: "Codex", now: 10_000, initial: false) == nil)
        #expect(say.announce(last: reply(2), dock: [approval], agent: "Codex", now: 20_000, initial: false) == "Codex 回复了")
        // A burst of replies is one announcement.
        #expect(say.announce(last: reply(3), dock: [approval], agent: "Codex", now: 21_000, initial: false) == nil)
        #expect(say.announce(last: .turnEnd(.init(key: "e4", text: "3 秒")), dock: [approval], agent: "Codex", now: 40_000, initial: false) == nil)
        // Something new that needs the user is said at once.
        let question = DockItem.question(.with { $0.questionID = "Q" })
        #expect(say.announce(last: reply(5), dock: [approval, question], agent: "Codex", now: 41_000, initial: false) == "Codex 在问你")
        #expect(say.announce(last: reply(5), dock: [approval, question], agent: "Codex", now: 60_000, initial: false) == nil)
    }

    @Test func theTerminalGivesWayToTheChatWhenItCannotBeShown() {
        #expect(!terminalAvailable(.exited))
        // The computer is away: the cached chat can still be read, the terminal cannot.
        #expect(!terminalAvailable(.offline))
        for status in [Status.idle, .running, .starting, .approval, .question, .plan, .popup, .unlinked, .loading] {
            #expect(terminalAvailable(status))
        }
    }

    @Test func theForkActionSaysHowAForkStands() {
        func row(blocked: String? = nil, running: Bool = false, error: String = "", frozen: Bool = false, expired: Bool = false) -> ForkAction {
            forkAction(blocked: blocked, running: running, error: error, frozen: frozen, expired: expired)
        }
        #expect(row() == ForkAction(text: "分叉会话", note: "", noteError: true, enabled: true, busy: false, tap: .fork))
        #expect(row(running: true, error: "x", frozen: true) == ForkAction(text: "正在分叉…", busy: true))
        #expect(row(blocked: "这一轮结束后可用", frozen: true) == ForkAction(note: "这一轮结束后可用", enabled: false))
        #expect(row(error: "电脑不在线") == ForkAction(note: "没能分叉：电脑不在线", noteError: true))
        // It may have happened: the row never promises a new fork, only the same request again.
        let unsure = row(error: "不确定电脑有没有执行", frozen: true)
        #expect(unsure.tap == .fork && unsure.enabled && unsure.noteError)
        #expect(unsure.note == "上次的分叉结果不确定。再点会重发同一个请求，电脑收到过就不会再分叉一次")
        // Expired: it cannot be asked again, so the tap gives it up and nothing is sent.
        let gone = row(error: "x", frozen: true, expired: true)
        #expect(gone.text == "放弃上次的分叉" && gone.tap == .abandon && gone.enabled)
        #expect(gone.note == "不确定上次有没有分叉出新会话，它已过期，不会再重发。请先看看会话列表")
    }

    @Test func anApprovalIsHeadedByWhatItAsksFor() {
        let command = PBApprovalRequested.with {
            $0.command = "curl https://example.com\u{202E}"
            $0.toolName = "Bash"
        }
        #expect(approvalHead(command) == "请求执行")
        #expect(approvalBody(command) == "curl https://example.com\\u202e", "what a command hides is spelled out")
        let tool = PBApprovalRequested.with {
            $0.toolName = "WebFetch"
            $0.toolInputJson = "{\"url\":\"https://example.com\"}"
        }
        #expect(approvalHead(tool) == "请求权限 · WebFetch")
        #expect(approvalBody(tool) == "{\"url\":\"https://example.com\"}")
    }

    @Test func anApprovalsLastMinuteIsMarked() {
        let a = PBApprovalRequested.with { $0.deadline = .with { $0.seconds = 1_000 } }
        #expect(ApprovalDeadline(a, now: 1_000_000 - 300_000) == ApprovalDeadline(a, now: 700_000))
        #expect(ApprovalDeadline(a, now: 700_000)?.text == "剩 5 分钟")
        #expect(ApprovalDeadline(a, now: 700_000)?.last == false)
        #expect(ApprovalDeadline(a, now: 950_000)?.last == true)
        #expect(ApprovalDeadline(PBApprovalRequested(), now: 0) == nil)
    }
}
