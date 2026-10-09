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

    /// What the next commands end with, in order; nil is success. With none left they succeed.
    func answer(_ list: Error?...) { lock.withLock { answers += list } }
    var sent: [PBCommand] { lock.withLock { commands } }
    var forwarded: [(id: String, retry: Bool)] { lock.withLock { forwards } }

    private func next() throws -> PBCommandResult {
        if let error = lock.withLock({ answers.isEmpty ? nil : answers.removeFirst() }) { throw error }
        return PBCommandResult()
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
        var inputs = OrderedMap<String, PBInputStatus>()
        inputs[store.outgoing("s")[0].requestId] = .submitted
        inputs[store.outgoing("s")[1].requestId] = .queued
        store.settle("s", inputs: inputs)
        #expect(store.outgoing("s").map(\.text) == ["乙"])
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
