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
                let emoji = Mailbox<[String]>()
                let joining = Task {
                    try await joinByPairing(
                        link: pairing.link, http: w.http, onEmoji: { emoji.put($0) },
                        stopped: { false },
                        persist: { acct, record in try TestSecurity.get(acct.server).install(record)
                        }, confirm: { _ in true })
                }
                defer { joining.cancel() }
                let expected = try await pairing.reveal()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                defer { approving.cancel() }
                let account = try await joining.value
                try await approving.value
                // The same approved request is accepted again.
                try await pairing.approve()
                let login = try await login(
                    api: Api(server: w.server.url, http: w.http), account: account)
                #expect(!login.isEmpty)
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
                            stopped: { false }, persist: { _, _ in _ = persisted.increment() })
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
                        stopped: { false },
                        persist: { acct, record in try TestSecurity.get(acct.server).install(record)
                        }, confirm: { _ in true })
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
