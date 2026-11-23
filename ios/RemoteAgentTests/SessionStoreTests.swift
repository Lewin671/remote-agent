import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

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
