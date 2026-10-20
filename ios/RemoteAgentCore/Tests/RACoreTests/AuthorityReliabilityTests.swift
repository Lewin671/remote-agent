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
    @Test func recoveryHintsUpdateOnSameHeadAndFailedPullIsStale() async throws {
        let store = AuthorityTestVault(), gate = AuthorityGate()
        gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: store, gate: gate)
        _ = try await bootstrap.prepareCreation(server: "https://relay.example.com")
        let material = try AuthorityMaterial.parse(store.readOperation()!.local)
        let record = try DeviceSecurity.Record.parse(material.chain)
        try store.initializeSecurity(record)
        let count = Counter()
        let client = DeviceSecurityClient(store: store, memberID: record.memberID,
            signingKey: try SigningKey(seed: material.seed), fetchStates: { _ in
                let n = count.increment()
                if n == 3 { throw URLError(.notConnectedToInternet) }
                var reply = PBGetSecurityStatesResponse(); reply.headEpoch = 1
                reply.recoveryPending = n == 1; reply.recoveryReadyMs = n == 1 ? 1234 : 0
                return reply
            })
        _ = try await client.sync()
        #expect(await client.recoveryPending)
        #expect(await client.recoveryReadyMs == 1234)
        _ = try await client.sync()
        #expect(await !client.recoveryPending)
        do { _ = try await client.sync(); Issue.record("pull should fail") } catch {}
        #expect(await client.recoveryStatusStale)
        #expect(await !client.recoveryPending)
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
