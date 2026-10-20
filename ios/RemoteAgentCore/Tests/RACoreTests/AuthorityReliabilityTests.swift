import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct AuthorityReliabilityTests {
    @Test func signingBuildersOnlyRunForWinnerUnderTheSharedStoreLock() throws {
        let store = AuthorityTestVault()
        let wins = Counter(), signed = Counter()
        DispatchQueue.concurrentPerform(iterations: 32) { _ in
            do {
                _ = try store.prepareOperation(expectedHead: nil) { _ in
                    _ = signed.increment()
                    return SecurityOperation(kind: .creation, parent: Data(repeating: 1, count: 32), signed: Data([1]))
                }
                _ = wins.increment()
            } catch {}
        }
        #expect(wins.value == 1 && signed.value == 1)
    }
    @Test func gateCancelsWorkSynchronouslyAndGenerationNeverComesBack() throws {
        let gate = AuthorityGate(), canceled = Signal()
        gate.set(active: true, protectedDataAvailable: true)
        let ticket = try gate.ticket()
        _ = try gate.track(ticket) { canceled.set() }
        gate.set(active: false, protectedDataAvailable: true)
        #expect(canceled.isSet)
        gate.set(active: true, protectedDataAvailable: true)
        #expect(throws: DeviceSecurity.Failure.self) { try gate.check(ticket) }
        let fresh = try gate.ticket()
        gate.set(active: true, protectedDataAvailable: false)
        #expect(throws: DeviceSecurity.Failure.self) { try gate.check(fresh) }
    }
}
