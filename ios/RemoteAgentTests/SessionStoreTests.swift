import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// Test stub for Step 4 command callers (`prepare`/`forward`).
final class FakeCommands: NewSessionCommands, @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [Error?] = []
    private var commands: [PBCommand] = []
    private var sends: [String:PBCommand] = [:]
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

    func prepare(_ sid: String, _ cmd: PBCommand) async throws -> PreparedCommand {
        lock.withLock {
            made += 1
            let id="r\(made)";sends[id]=cmd
            return PreparedCommand(machineId:"fixture-machine",sessionId:sid,requestId:id,deadlineMs:0,command:cmd)
        }
    }

    func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws -> PreparedCommand {
        lock.withLock {
            made += 1
            commands.append(cmd)
            return PreparedCommand(machineId: machineId, sessionId: sessionId, requestId: "r\(made)", deadlineMs: 0, command: cmd)
        }
    }

    func forward(_ p: PreparedCommand, retryOfUnknown: Bool) async throws -> PBCommandResult {
        lock.withLock { forwards.append((p.requestId, retryOfUnknown)); if let cmd=sends[p.requestId] {commands.append(cmd)} }
        return try next()
    }
}

@MainActor
@Suite struct SessionStoreTests {
    let store = SessionStore()

    @Test func draftTextUsesTheSealedStoreAndIsRemovedWhenCleared() {
        let files = VolatileFiles()
        let first = SessionStore(draftFiles: files, draftQueue: nil)
        first.setDraft("one", "文字草稿")
        let next = SessionStore(draftFiles: files, draftQueue: nil)
        #expect(next.draft("one") == "文字草稿")
        #expect(next.draft("two").isEmpty)
        next.setDraft("one", "")
        #expect(SessionStore(draftFiles: files, draftQueue: nil).draft("one").isEmpty)
    }

    @Test func scheduleTextIsNotPersistedAndRemovedSessionTextCannotReturn() {
        let files = VolatileFiles()
        let first = SessionStore(draftFiles: files, draftQueue: nil)
        first.setDraft("schedule:one", "temporary")
        first.setDraft("gone", "remove")
        first.setDraft("keep", "keep")
        first.forgetSession("gone")
        let next = SessionStore(draftFiles: files, draftQueue: nil)
        #expect(next.draft("schedule:one").isEmpty)
        #expect(next.draft("gone").isEmpty)
        #expect(next.draft("keep") == "keep")
    }

    // MARK: Drafts

    @Test func aDraftIsKeptPerSession() {
        store.setDraft("a", "写到一半")
        store.setDraft("b", "另一个")
        #expect(store.draft("a") == "写到一半" && store.draft("b") == "另一个" && store.draft("c").isEmpty)
        store.setDraft("a", "")
        #expect(store.draft("a").isEmpty && store.draft("b") == "另一个")
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

    @Test func aDockedItemThatComesWithADraftStartsFolded() {
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
