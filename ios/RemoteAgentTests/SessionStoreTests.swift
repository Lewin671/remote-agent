import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// The computer as the session screens see it: it answers what the test tells it to, and writes down what it was sent.
final class FakeCommands: SessionCommands, @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [Error?] = []
    private var commands: [PBCommand] = []
    private var forwards: [(id: String, retry: Bool)] = []
    private var made = 0
    private var reply = PBCommandResult()

    /// What a command that succeeds is answered with.
    func result(_ r: PBCommandResult) { lock.withLock { reply = r } }
    /// What the next commands end with, in order; nil is success. With none left they succeed.
    func answer(_ list: Error?...) { lock.withLock { answers += list } }
    var sent: [PBCommand] { lock.withLock { commands } }
    var forwarded: [(id: String, retry: Bool)] { lock.withLock { forwards } }

    private func next() throws -> PBCommandResult {
        if let error = lock.withLock({ answers.isEmpty ? nil : answers.removeFirst() }) { throw error }
        return lock.withLock { reply }
    }

    func command(_ sid: String, _ cmd: PBCommand) async throws -> PBCommandResult {
        lock.withLock { commands.append(cmd) }
        return try next()
    }

    func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws -> PreparedCommand {
        lock.withLock {
            made += 1
            commands.append(cmd)
            return PreparedCommand(machineId: machineId, sessionId: sessionId, requestId: "r\(made)", deadlineMs: 0, envelope: PBEnvelope())
        }
    }

    func forward(_ p: PreparedCommand, retryOfUnknown: Bool) async throws -> PBCommandResult {
        lock.withLock { forwards.append((p.requestId, retryOfUnknown)) }
        return try next()
    }
}

private let unknown = CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行")
private let offline = CommandError(CommandError.unavailable, "电脑不在线，指令没有发出")

@MainActor
@Suite struct SessionStoreTests {
    let store = SessionStore()
    let computer = FakeCommands()

    // MARK: Sending

    @Test func aMessageIsSendingUntilTheComputerAnswers() async {
        let task = store.send(computer, sid: "s", text: "你好", mode: .queue)
        let o = store.outgoing("s")[0]
        #expect(o.state == .sending)
        await task.value
        #expect(o.state == .sent)
        let sent = computer.sent[0].send
        #expect(sent.text == "你好" && sent.mode == .queue && sent.sessionID == "s" && sent.requestID == o.requestId)
    }

    @Test func aSendThatSurelyDidNotGoIsFailedAndOneThatMayHaveIsUnknown() async {
        computer.answer(offline, unknown)
        await store.send(computer, sid: "s", text: "一").value
        await store.send(computer, sid: "s", text: "二").value
        let list = store.outgoing("s")
        #expect(list[0].state == .failed && list[0].error == "电脑不在线，指令没有发出")
        #expect(list[1].state == .unknown)
    }

    @Test func aRetrySendsTheSameRequestSoThatItIsNeverSubmittedTwice() async {
        computer.answer(unknown)
        await store.send(computer, sid: "s", text: "跑测试").value
        let o = store.outgoing("s")[0]
        await store.deliver(computer, sid: "s", o).value
        #expect(o.state == .sent && o.error.isEmpty)
        #expect(computer.sent.map(\.send.requestID) == [o.requestId, o.requestId])
        #expect(store.outgoing("s").count == 1)
    }

    @Test func aSecondTapWhileOneIsOnItsWaySendsNothingMore() async {
        let first = store.send(computer, sid: "s", text: "一次")
        await store.deliver(computer, sid: "s", store.outgoing("s")[0]).value
        await first.value
        #expect(computer.sent.count == 1)
    }

    @Test func textsOfMessagesSentFromHereAreKnownByRequest() async {
        await store.send(computer, sid: "s", text: "甲").value
        let o = store.outgoing("s")[0]
        #expect(store.texts("s") == [o.requestId: "甲"])
        #expect(store.find("s", requestId: o.requestId) === o)
        #expect(store.texts("other").isEmpty)
    }

    @Test func messagesTheCliRecordedAreLetGo() async {
        await store.send(computer, sid: "s", text: "甲").value
        await store.send(computer, sid: "s", text: "乙").value
        var inputs = OrderedMap<String, PBInputState>()
        inputs[store.outgoing("s")[0].requestId] = input(.submitted)
        inputs[store.outgoing("s")[1].requestId] = input(.queued)
        store.settle("s", inputs: inputs)
        #expect(store.outgoing("s").map(\.text) == ["乙"])
    }

    @Test func aMessageWhoseAnswerWasLostIsLetGoOnceTheCliRecordedIt() async {
        computer.answer(unknown, offline)
        await store.send(computer, sid: "s", text: "甲").value
        await store.send(computer, sid: "s", text: "乙").value
        #expect(store.outgoing("s").map(\.state) == [.unknown, .failed])
        var inputs = OrderedMap<String, PBInputState>()
        inputs[store.outgoing("s")[0].requestId] = input(.submitted)
        store.settle("s", inputs: inputs)
        // The one the events say nothing of stays, with its retry.
        #expect(store.outgoing("s").map(\.text) == ["乙"])
    }

    @Test func aMessageCanceledElsewhereIsLetGoAfterItsAnswerWasLost() async {
        computer.answer(unknown, offline)
        await store.send(computer, sid: "s", text: "甲").value
        await store.send(computer, sid: "s", text: "乙").value
        var inputs = OrderedMap<String, PBInputState>()
        inputs[store.outgoing("s")[0].requestId] = input(.canceled)
        store.settle("s", inputs: inputs)
        #expect(store.outgoing("s").map(\.text) == ["乙"])
    }

    @Test func aMessageTheSessionEndedOnIsKeptToBePutBack() async {
        computer.answer(unknown)
        await store.send(computer, sid: "s", text: "甲").value
        let o = store.outgoing("s")[0]
        var inputs = OrderedMap<String, PBInputState>()
        inputs[o.requestId] = input(.canceled, reason: "exited")
        store.settle("s", inputs: inputs)
        // What 放回输入框 on the chat's row finds.
        #expect(store.find("s", requestId: o.requestId) === o)
        store.putBack("s", o)
        #expect(store.draft("s") == "甲" && store.outgoing("s").isEmpty)
    }

    private func input(_ status: PBInputStatus, reason: String = "") -> PBInputState {
        .with {
            $0.status = status
            $0.reason = reason
        }
    }

    // MARK: Drafts

    @Test func aDraftIsKeptPerSession() {
        store.setDraft("a", "写到一半")
        store.setDraft("b", "另一个")
        #expect(store.draft("a") == "写到一半" && store.draft("b") == "另一个" && store.draft("c").isEmpty)
        store.setDraft("a", "")
        #expect(store.draft("a").isEmpty && store.draft("b") == "另一个")
    }

    @Test func aMessagePutBackGoesAfterWhatIsAlreadyTyped() async {
        computer.answer(offline)
        await store.send(computer, sid: "s", text: "没发出去的").value
        let o = store.outgoing("s")[0]
        store.setDraft("s", "正在写的  \n")
        store.putBack("s", o)
        #expect(store.draft("s") == "正在写的\n没发出去的")
        #expect(store.outgoing("s").isEmpty)
    }

    @Test func aMessagePutBackIntoAnEmptyBoxIsTheDraft() {
        store.setDraft("s", "   ")
        store.appendToDraft("s", "原话")
        #expect(store.draft("s") == "原话")
    }

    @Test func aRefusalReasonStaysWithItsApprovalAndIsNotCarriedToAnother() {
        store.setReason("A1", "先别删")
        #expect(store.reason("A1") == "先别删")
        // Another client answered A1 meanwhile: the words are still there, and the next approval starts empty.
        #expect(store.reason("A2").isEmpty)
        #expect(store.reason("A1") == "先别删")
    }

    @Test func theFirstMessageOfASessionStartedHereIsRemembered() {
        store.rememberPrompt("new", "")
        #expect(store.prompt("new") == nil)
        store.rememberPrompt("new", "建一个 Makefile")
        #expect(store.prompt("new") == "建一个 Makefile")
    }

    // MARK: Approvals

    @Test func aDecisionThatWentThroughFreesTheApproval() async throws {
        let allowed = try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
        #expect(allowed)
        #expect(!store.resolve("A").attempt.frozen)
        let sent = computer.sent[0].resolveApproval
        #expect(sent.approvalID == "A" && sent.allow && sent.sessionID == "s")
        #expect(computer.forwarded.map(\.retry) == [false])
    }

    @Test func anUnknownOutcomeIsSentAgainAsItWasNeverAsTheOppositeDecision() async throws {
        computer.answer(unknown)
        await #expect(throws: CommandError.self) {
            try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
        }
        let r = store.resolve("A")
        #expect(r.attempt.frozen && r.allow)
        // The user now taps 不允许: what goes is still the first decision, under the same request.
        let went = try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "不行")
        #expect(went)
        #expect(computer.forwarded.map(\.id) == ["r1", "r1"])
        #expect(computer.forwarded.map(\.retry) == [false, true])
        #expect(computer.sent.count == 1, "no second command was sealed")
        #expect(!r.attempt.frozen)
    }

    @Test func aDefiniteFailureLetsAnotherDecisionBeMade() async throws {
        computer.answer(offline)
        await #expect(throws: CommandError.self) {
            try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
        }
        #expect(!store.resolve("A").attempt.frozen)
        let went = try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "先别跑")
        #expect(!went)
        #expect(computer.forwarded.map(\.id) == ["r1", "r2"])
        #expect(computer.sent[1].resolveApproval.message == "先别跑")
    }

    @Test func anUnknownDecisionThatExpiredIsNotSentAgainAndSaysSo() async {
        computer.answer(unknown, CommandError(CommandError.expired, "这条指令已过期，没有发出"))
        _ = try? await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "")
        do {
            _ = try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "")
            Issue.record("an expired retry went through")
        } catch {
            #expect(errorText(error) == Attempt.expiredUnknown)
        }
        let r = store.resolve("A")
        #expect(r.attempt.frozen && r.attempt.expired && !r.allow)
        // Nothing more is forwarded for it.
        _ = try? await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
        #expect(computer.forwarded.count == 2)
    }

    // One store for every place an approval is drawn: what the home page's card decides is what the session's page reads.

    @Test func aDecisionMadeInOnePlaceIsLockedInEveryOther() async {
        let card = store.resolve("A")
        #expect(!card.decided && !card.running)
        let sent = store.decide(computer, machineId: "m", sid: "s", approvalId: "A", allow: true)
        // The session's page asks for the same approval and gets the same decision, not one of its own.
        let page = store.resolve("A")
        #expect(page === card)
        #expect(page.running && page.decided)
        // A tap on the other screen meanwhile adds nothing.
        let second = store.decide(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "不行")
        #expect(await second.value == nil)
        #expect(await sent.value == true)
        #expect(!page.running && page.decided && page.allow && page.error.isEmpty && !page.frozen)
        #expect(computer.sent.count == 1 && computer.forwarded.count == 1)
        // Another approval starts clean.
        #expect(!store.resolve("B").decided)
    }

    @Test func aDecisionLeftUnknownOnOneScreenIsOnlySentAgainAsItWasFromAnother() async {
        computer.answer(unknown)
        #expect(await store.decide(computer, machineId: "m", sid: "s", approvalId: "A", allow: false, message: "先别删").value == nil)
        // What the other screen reads: unknown, which decision it was, and why.
        let r = store.resolve("A")
        #expect(r.frozen && !r.expired && !r.allow && r.decided && !r.running)
        #expect(r.error == errorText(unknown))
        // Its 重试 sends the same sealed command, marked as a retry; no other decision is sealed.
        #expect(await store.retryDecision(computer, machineId: "m", sid: "s", approvalId: "A").value == false)
        #expect(computer.forwarded.map(\.id) == ["r1", "r1"] && computer.forwarded.map(\.retry) == [false, true])
        #expect(computer.sent.count == 1 && computer.sent[0].resolveApproval.message == "先别删")
        #expect(!r.frozen && r.error.isEmpty && r.decided)
    }

    @Test func aDecisionThatDefinitelyFailedKeepsItsReasonAndIsOfferedAgain() async {
        computer.answer(offline)
        #expect(await store.decide(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, always: true).value == nil)
        let r = store.resolve("A")
        #expect(r.decided && !r.frozen && r.error == errorText(offline))
        #expect(await store.retryDecision(computer, machineId: "m", sid: "s", approvalId: "A").value == true)
        // A definite failure is sealed anew, with the same decision.
        #expect(computer.forwarded.map(\.id) == ["r1", "r2"])
        #expect(computer.sent[1].resolveApproval.allow && computer.sent[1].resolveApproval.always)
    }

    @Test func anExpiredUnknownDecisionSaysSoWhereverItIsRead() async {
        computer.answer(unknown, CommandError(CommandError.expired, "这条指令已过期，没有发出"))
        _ = await store.decide(computer, machineId: "m", sid: "s", approvalId: "A", allow: true).value
        _ = await store.retryDecision(computer, machineId: "m", sid: "s", approvalId: "A").value
        let r = store.resolve("A")
        #expect(r.frozen && r.expired && r.allow)
    }

    @Test func aNetworkFailureAfterAnUnknownOutcomeLeavesItUnknown() async {
        computer.answer(unknown, CommandError("network", "网络不通，指令没有发出"))
        _ = try? await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
        do {
            _ = try await store.resolveApproval(computer, machineId: "m", sid: "s", approvalId: "A", allow: true, message: "")
            Issue.record("it went through")
        } catch {
            #expect(uncertain(error))
        }
        #expect(store.resolve("A").attempt.frozen)
    }

    // MARK: Forks

    private func forked(_ id: String) -> PBCommandResult {
        .with { $0.forkSession = .with { $0.session = .with { $0.id = id } } }
    }

    @Test func aForkWhoseAnswerWasLostIsSentAgainAsItWas() async {
        computer.result(forked("s-fork"))
        computer.answer(unknown)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == nil)
        // What the menu reads when it is opened again, and what the page reads when it is come back to.
        let f = store.fork("s")
        #expect(f.frozen && !f.expired && !f.running)
        #expect(f.error == errorText(unknown))
        store.dismissForkError("s")
        #expect(f.frozen && !f.error.isEmpty, "closing the menu does not forget a fork that may have happened")
        // The same request: the computer answers with the session it already made instead of forking again.
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == "s-fork")
        #expect(computer.forwarded.map(\.id) == ["r1", "r1"])
        #expect(computer.forwarded.map(\.retry) == [false, true])
        #expect(computer.sent.count == 1)
        #expect(!f.frozen && f.error.isEmpty)
        // The next fork is another one.
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == "s-fork")
        #expect(computer.forwarded.map(\.id) == ["r1", "r1", "r2"])
    }

    @Test func aForkThatSurelyDidNotHappenIsANewRequestNextTime() async {
        computer.result(forked("s-fork"))
        computer.answer(offline)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == nil)
        let f = store.fork("s")
        #expect(!f.frozen && f.error == errorText(offline))
        store.dismissForkError("s")
        #expect(f.error.isEmpty)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == "s-fork")
        #expect(computer.forwarded.map(\.id) == ["r1", "r2"])
        // Each session has its own.
        #expect(store.fork("other") !== f)
    }

    @Test func anUnknownForkThatExpiredIsNotSentAgainUntilItIsGivenUp() async {
        computer.result(forked("s-fork"))
        computer.answer(unknown, CommandError(CommandError.expired, "这条指令已过期，没有发出"))
        _ = await store.forkSession(computer, machineId: "m", sid: "s").value
        _ = await store.forkSession(computer, machineId: "m", sid: "s").value
        let f = store.fork("s")
        #expect(f.frozen && f.expired)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == nil)
        #expect(computer.forwarded.count == 2, "nothing more is sent")
        store.abandonFork("s")
        #expect(!f.frozen && !f.expired && f.error.isEmpty)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == "s-fork")
        #expect(computer.forwarded.map(\.id) == ["r1", "r1", "r2"])
    }

    @Test func oneForkAtATime() async {
        computer.result(forked("s-fork"))
        let first = store.forkSession(computer, machineId: "m", sid: "s")
        #expect(store.fork("s").running)
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == nil)
        #expect(await first.value == "s-fork")
        #expect(computer.forwarded.count == 1)
    }

    @Test func anAnswerThatNamesNoSessionIsAFailure() async {
        #expect(await store.forkSession(computer, machineId: "m", sid: "s").value == nil)
        #expect(store.fork("s").error == "意外的回复" && !store.fork("s").frozen)
    }

    // MARK: Answers

    @Test func anAnswerWhoseOutcomeIsUnknownIsSentAgainUnchanged() async throws {
        let answer = store.answer("Q", 0)
        var sealed: [[Int]] = []
        let prepare: ([Int]) throws -> PreparedCommand = { options in
            sealed.append(options)
            return try self.computer.prepare(machineId: "m", sessionId: "s", PBCommand())
        }
        computer.answer(unknown)
        await #expect(throws: CommandError.self) { try await store.sendAnswer(computer, answer, options: [1], prepare: prepare) }
        #expect(answer.attempt.frozen && answer.options == [1])
        let went = try await store.sendAnswer(computer, answer, options: [0], prepare: prepare)
        #expect(went == [1], "the earlier answer, not the new tap")
        #expect(sealed == [[1]])
        #expect(computer.forwarded.map(\.id) == ["r1", "r1"])
        #expect(store.answer("Q", 0) === answer && store.answer("Q", 1) !== answer)
    }

    @Test func anAnswerGivenUpOnCanBeGivenAgain() async throws {
        let answer = store.answer("Q", 0)
        computer.answer(unknown)
        _ = try? await store.sendAnswer(computer, answer, options: [2]) { _ in try computer.prepare(machineId: "m", sessionId: "s", PBCommand()) }
        store.abandon(answer)
        #expect(!answer.attempt.frozen)
        let went = try await store.sendAnswer(computer, answer, options: [0]) { _ in try computer.prepare(machineId: "m", sessionId: "s", PBCommand()) }
        #expect(went == [0])
        #expect(computer.forwarded.map(\.id) == ["r1", "r2"])
    }

    // MARK: Cancels

    @Test func aCancelIsInFlightUntilAnsweredAndAFailureStaysToBeRetried() async {
        computer.answer(CommandError("failed_precondition", "已送达，不能取消", fromDaemon: true))
        let task = store.cancel(computer, sid: "s", requestId: "q1")
        #expect(store.cancels["q1"] == .canceling)
        await task.value
        #expect(store.cancels["q1"] == .failed("已送达，不能取消"))
        await store.cancel(computer, sid: "s", requestId: "q1").value
        #expect(store.cancels["q1"] == nil)
        #expect(computer.sent.map(\.cancelInput.requestID) == ["q1", "q1"])
    }

    @Test func aCancelIsForgottenOnceItsMessageLeftTheQueue() async {
        computer.answer(offline, offline)
        await store.cancel(computer, sid: "s", requestId: "q1").value
        await store.cancel(computer, sid: "s", requestId: "q2").value
        var inputs = OrderedMap<String, PBInputStatus>()
        inputs["q1"] = .submitted
        inputs["q2"] = .queued
        store.settleCancels(inputs: inputs)
        #expect(store.cancels["q1"] == nil)
        #expect(store.cancels["q2"] != nil)
    }
}

@MainActor @Suite struct MissingImageSendTests {
    @Test func aDaemonMissingImageReplyCanBePutBackWithTextImagesAndOriginalOption() async {
        let store = SessionStore()
        let commands = FakeCommands()
        let image = PendingImage(id: "picked", uri: "source")
        let o = Outgoing(requestId: "request", text: "看图", imageIds: ["old"], images: [image], original: true)
        commands.answer(CommandError("not_found", "image old is not on this machine; upload it again", fromDaemon: true))
        store.add("s", o)
        await store.deliver(commands, sid: "s", o).value
        #expect(o.state == .failed && o.missingImages && o.canPutBack)
        let note = outgoingNote(state: o.state, error: o.error, input: nil, failure: nil, agent: .codex, hasImages: true, missingImages: o.missingImages)
        #expect(note.actions == [.putBack, .remove])
        #expect(o.text == "看图" && o.images[0] === image && o.original)
    }

    @Test func anUnknownMessageKeepsItsRequestAndImageIdsAfterExpiryAndAnOfflineRetry() async {
        let store = SessionStore()
        let commands = FakeCommands()
        let image = PendingImage(id: "picked", uri: nil)
        let o = Outgoing(requestId: "request", text: "看图", imageIds: ["old"], images: [image])
        commands.answer(CommandError(CommandError.unknownOutcome, "lost"), CommandError("unavailable", "offline"))
        store.add("s", o)
        await store.deliver(commands, sid: "s", o).value
        image.imageId = "renewed"
        image.expiresMs = 0
        await store.deliver(commands, sid: "s", o).value
        #expect(o.state == .unknown && !o.canPutBack && !o.missingImages)
        #expect(commands.sent.map(\.send.requestID) == ["request", "request"])
        #expect(commands.sent.map(\.send.imageIds) == [["old"], ["old"]])
        let note = outgoingNote(state: o.state, error: o.error, input: nil, failure: nil, agent: .codex, hasImages: true)
        #expect(note.actions == [.retry, .remove])
    }

    @Test func aDockedItemThatComesWithADraftStartsFolded() {
        let store = SessionStore()
        // Asked once, when the item is first shown: typing later does not fold an open one.
        #expect(store.dockOpen("s|a1", drafting: { false }))
        #expect(store.dockOpen("s|a1", drafting: { true }))
        // A new item while a draft is in the box comes folded, and opens when tapped.
        #expect(!store.dockOpen("s|a2", drafting: { true }))
        #expect(!store.dockOpen("s|a2", drafting: { false }))
        store.setDockOpen("s|a2", true)
        #expect(store.dockOpen("s|a2", drafting: { true }))
        store.setDockOpen("s|a1", false)
        #expect(!store.dockOpen("s|a1", drafting: { false }))
    }
}
