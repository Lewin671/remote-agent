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
    @Test func autoRenewalHonorsBothIntervalsAndMissingCurrentHeadLease() throws {
        let now: UInt64 = 1_000_000
        let l = DeviceSecurity.HeadLease(domain: Data(repeating: 1, count: 32), head: Data(repeating: 2, count: 32),
            issuedAt: now, notAfter: now + DeviceSecurity.lease, signature: Data(repeating: 0, count: 64))
        #expect(Authority.needsRenewal(nil, now: Int64(now)))
        #expect(!Authority.needsRenewal(l, now: Int64(now - 1)))
        #expect(!Authority.needsRenewal(l, now: Int64(l.notAfter - 25 * 60 * 60 * 1000)))
        #expect(Authority.needsRenewal(l, now: Int64(l.notAfter - 23 * 60 * 60 * 1000)))
        var short = l; short.notAfter = now + 60 * 60 * 1000
        #expect(!Authority.needsRenewal(short, now: Int64(now + 2 * 60 * 60 * 1000)))
    }
}
