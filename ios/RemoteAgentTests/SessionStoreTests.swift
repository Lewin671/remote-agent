import Foundation
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct SessionStoreTests {
    let store = SessionStore()

    @Test func aRefusalReasonStaysWithItsApprovalAndIsNotCarriedToAnother() {
        store.setReason("A1", "先别删")
        #expect(store.reason("A1") == "先别删")
        #expect(store.reason("A2").isEmpty)
        #expect(store.reason("A1") == "先别删")
    }

    @Test func aDockedItemThatComesWithADraftStartsFolded() {
        #expect(store.dockOpen("s|a1", drafting: { false }))
        #expect(store.dockOpen("s|a1", drafting: { true }))
        #expect(!store.dockOpen("s|a2", drafting: { true }))
        #expect(!store.dockOpen("s|a2", drafting: { false }))
        store.setDockOpen("s|a2", true)
        #expect(store.dockOpen("s|a2", drafting: { true }))
        store.setDockOpen("s|a1", false)
        #expect(!store.dockOpen("s|a1", drafting: { false }))
    }
}
