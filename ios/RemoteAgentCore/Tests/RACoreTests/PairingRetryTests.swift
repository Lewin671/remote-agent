import Foundation
import RAProto
import Testing

@testable import RACore

extension ServerTests {
    @Suite(.serialized) struct DeviceEnrollment {
        @Test func aCompletedPairingAcceptsTheSameApprovalAgain() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let pairing = try await PhonePairing.start(w.phone)
                let candidateSaved = Signal(), accountSaved = Signal(), intentSaved = Signal()
                let receipt = RegistrationReceipt()
                let joiningHTTP = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/JoinPairing") == true { #expect(candidateSaved.isSet) }
                    if request.url?.path.hasSuffix("/RegisterDevice") == true { #expect(accountSaved.isSet) }
                    return request
                }
                let emoji = Mailbox<[String]>()
                let joining = Task {
                    try await joinByPairing(
                        link: pairing.link, http: joiningHTTP, onEmoji: { emoji.put($0) },
                        stopped: { false }, sleep: { _ in try await realSleep(10) },
                        persist: { acct, record in #expect(intentSaved.isSet); accountSaved.set(); try TestSecurity.get(acct.server).install(record)
                        }, persistRegistration: { intent in try receipt.save(intent); intentSaved.set() }, persistIdentity: { _ in candidateSaved.set() }, confirm: { _ in true })
                }
                defer { joining.cancel() }
                let expected = try await pairing.reveal()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                defer { approving.cancel() }
                let account = try await joining.value
                let saved = try receipt.load()
                #expect(String(reflecting:saved)=="RegistrationIntent")
                #expect(Mirror(reflecting:saved).children.isEmpty)
                let retried = try await saved.resume(http:joiningHTTP) { acct,record in try TestSecurity.get(acct.server).install(record) }
                #expect(retried.deviceId == account.deviceId)
                try await approving.value
                // The same approved request is accepted again.
                try await pairing.approve()
                let login = try await login(
                    api: Api(server: w.server.url, http: w.http), account: account)
                #expect(!login.isEmpty)
            }
        }

        @Test func coldStartWithLostReceiptUsesLoginWithoutReplayingExpiredRegistration() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let pairing = try await PhonePairing.start(w.phone)
                let emoji = Mailbox<[String]>(), receipts = Mailbox<PairDeliveryReceipt>()
                let deliveryCleared = Signal(), intentCleared = Signal()
                let faulty = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/GetPairing") == true,
                       let body = request.httpBody, try PBGetPairingRequest(serializedBytes: body).received {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let joining = Task {
                    try await joinByPairing(link: pairing.link, http: faulty, onEmoji: { emoji.put($0) },
                        stopped: { false }, sleep: { _ in try await realSleep(10) },
                        persist: { acct, record in try TestSecurity.get(acct.server).install(record) },
                        persistRegistration: { _ in }, clearRegistration: { _ in intentCleared.set() }, persistIdentity: { _ in },
                        persistDelivery: { receipts.put($0) }, clearDelivery: { deliveryCleared.set() }, confirm: { _ in true })
                }
                defer { joining.cancel() }
                let expected = try await pairing.reveal()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                defer { approving.cancel() }
                let account = try await joining.value
                #expect(deliveryCleared.isSet && intentCleared.isSet)
                let receipt = try #require(await receipts.poll(5_000))
                let replayed = Counter(), bootstrapped = Counter()
                let restartedHTTP = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/RegisterDevice") == true {
                        replayed.increment()
                        throw URLError(.badServerResponse) // expired admission must never be replayed
                    }
                    if request.url?.path.hasSuffix("/GetPairing") == true {
                        throw URLError(.resourceUnavailable) // old pairing slot is gone
                    }
                    return request
                }
                let resumed = try await receipt.resume(http: restartedHTTP) { _, _ in bootstrapped.increment() }
                #expect(resumed.deviceId == account.deviceId)
                #expect(replayed.value == 0)
                #expect(bootstrapped.value == 1) // idempotent install also repairs an interrupted re-pairing
            }
        }

        @Test func candidateDoesNotSignOrPersistWithoutLocalSasConfirmation() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let before = try DeviceSecurity.parseState(
                    try await w.phone.chain().last!.state).core.members.count
                let pairing = try await PhonePairing.start(w.phone)
                let displayed = Mailbox<[String]>()
                let persisted = Counter()
                let joining = Task {
                    do {
                        _ = try await joinByPairing(
                            link: pairing.link, http: w.http, onEmoji: { displayed.put($0) },
                            stopped: { false }, sleep: { _ in try await realSleep(10) }, persist: { _, _ in _ = persisted.increment() }, persistRegistration: { _ in }, persistIdentity: { _ in })
                        return false
                    } catch { return errorText(error).contains("本机未确认") }
                }
                let expected = try await pairing.reveal()
                #expect(await displayed.poll(5000) == expected.emoji)
                #expect(await joining.value)
                #expect(persisted.value == 0)
                let chain = try await w.phone.chain()
                let head = try DeviceSecurity.parseState(chain[chain.count - 1].state)
                #expect(head.core.members.count == before)
            }
        }

        // Delivery acknowledgement is best effort: a transport failure must not
        // discard an already registered, usable device identity.
        @Test func aFailedDeliveryAcknowledgementDoesNotDiscardTheRegisteredIdentity() async throws
        {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let pairing = try await PhonePairing.start(w.phone)
                let emoji = Mailbox<[String]>()
                let faulty = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/GetPairing") == true,
                        let body = request.httpBody,
                        try PBGetPairingRequest(serializedBytes: body).received
                    {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let joining = Task {
                    try await joinByPairing(
                        link: pairing.link, http: faulty, onEmoji: { emoji.put($0) },
                        stopped: { false }, sleep: { _ in try await realSleep(10) },
                        persist: { acct, record in try TestSecurity.get(acct.server).install(record)
                        }, persistRegistration: { _ in }, persistIdentity: { _ in }, confirm: { _ in true })
                }
                defer { joining.cancel() }
                let expected = try await pairing.reveal()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                let account = try await joining.value
                approving.cancel()
                _ = try? await approving.value
                let login = try await login(
                    api: Api(server: w.server.url, http: w.http), account: account)
                #expect(!login.isEmpty)
            }
        }
    }
}

private final class RegistrationReceipt: @unchecked Sendable {
    private let lock=NSLock();private var raw=Data()
    func save(_ intent:RegistrationIntent) throws { let data=try JSONEncoder().encode(intent);lock.withLock{raw=data} }
    func load() throws -> RegistrationIntent { try JSONDecoder().decode(RegistrationIntent.self,from:lock.withLock{raw}) }
}
