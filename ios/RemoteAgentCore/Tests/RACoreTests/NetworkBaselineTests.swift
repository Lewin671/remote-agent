import Testing
@testable import RACore

extension ServerTests {
    @Suite struct NetworkBaseline {
        @Test func firstObservationEvenAfterReadyOnlyRecordsTheBaseline() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let remote=try await w.client();remote.start()
                _ = try await remote.awaitState("Ready before the monitor baseline"){$0.conn == .online}
                let generation=await remote.networkGeneration,leg=await remote.ws
                remote.networkChanged("first-wifi",available:true)
                try await eventually("first network observation"){await remote.networkSignature == "first-wifi"}
                #expect(await remote.networkGeneration==generation)
                #expect(await remote.ws === leg)
                remote.networkChanged("cellular",available:true)
                try await eventually("changed network observation"){await remote.networkSignature == "cellular"}
                #expect(await remote.networkGeneration==generation+1)
            }
        }
    }
}
