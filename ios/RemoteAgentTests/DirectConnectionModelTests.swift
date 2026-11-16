import Foundation
import Testing
import RACore
@testable import RemoteAgent

/// The 「连接」group's model: what it says right after the switch is turned on, when that stops,
/// and what a refused change says.
@MainActor
@Suite struct DirectConnectionModelTests {
    final class Box: @unchecked Sendable {
        var status = DirectStatus(state: .none, reason: nil)
        var now = Date(timeIntervalSince1970: 1_000)
        var fail = false
        var writes: [Bool] = []
    }

    func model(_ box: Box) -> DirectConnectionModel {
        DirectConnectionModel(
            status: { box.status },
            setEnabled: { on in
                if box.fail { throw UserError("no") }
                box.writes.append(on)
                box.status = on ? DirectStatus(state: .none, reason: nil) : DirectStatus(state: .none, reason: .disabled)
            },
            now: { box.now })
    }

    @Test func aPathJustTurnedOnSaysTryingUntilItReportsOrTheTimeIsUp() {
        let box = Box()
        box.status = DirectStatus(state: .none, reason: .disabled)
        let m = model(box)
        #expect(!m.on && m.view.kind == .off)
        m.set(true)
        #expect(box.writes == [true] && m.on && m.view.kind == .trying)
        box.now = box.now.addingTimeInterval(DirectConnectionModel.tryingSeconds + 1)
        m.expireTrying()
        #expect(m.view.kind == .relay, "nothing reported in time: the path in use is the relay")
    }

    @Test func aReportEndsTryingAtOnce() {
        let box = Box()
        let m = model(box)
        m.set(true)
        box.status = DirectStatus(state: .connected, reason: nil)
        m.reload()
        #expect(m.view.kind != .trying)
    }

    @Test func turningOffFoldsTheStateAway() {
        let box = Box()
        let m = model(box)
        m.set(true)
        m.set(false)
        #expect(box.writes == [true, false] && !m.on && m.view.kind == .off)
    }

    @Test func aRefusedChangeSaysSoAndChangesNothing() {
        let box = Box()
        let m = model(box)
        box.fail = true
        m.set(true)
        #expect(!m.error.isEmpty && box.writes.isEmpty)
        box.fail = false
        m.set(true)
        #expect(m.error.isEmpty)
    }

    @Test func aBuildWithoutDirectHasNoSwitch() {
        let box = Box()
        box.status = DirectStatus(state: .unsupported)
        let m = model(box)
        #expect(!m.supported && !m.on)
    }
}
