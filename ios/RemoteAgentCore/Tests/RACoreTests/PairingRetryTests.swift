import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Suite(.serialized) struct DeviceEnrollment {
        @Test func aCompletedPairingAcceptsTheSameApprovalAgain() async throws {
            try await World.run(setUp: { _ in }) { w in
                let sponsor = try w.remote(w.daemon.account)
                let pairing = try await Initiation.start(remote: sponsor)
                let emoji = Mailbox<[String]>()
                let joining = Task {
                    try await joinByPairing(link: pairing.link, http: w.http, onEmoji: { emoji.put($0) }, stopped: { false })
                }
                defer { joining.cancel() }
                let expected = try await pairing.joined()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                defer { approving.cancel() }
                let account = try await joining.value
                try await approving.value
                try await pairing.approve()
                #expect(!(try await login(api: Api(server: w.server.url, http: w.http), account: account)).isEmpty)
            }
        }

        // Delivery acknowledgement is best effort: a transport failure must not
        // discard an already registered, usable device identity.
        @Test func aFailedDeliveryAcknowledgementDoesNotDiscardTheRegisteredIdentity() async throws {
            try await World.run(setUp: { _ in }) { w in
                let sponsor = try w.remote(w.daemon.account)
                let pairing = try await Initiation.start(remote: sponsor)
                let emoji = Mailbox<[String]>()
                let faulty = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/GetPairing") == true,
                       let body = request.httpBody, try PBGetPairingRequest(serializedBytes: body).received {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let joining = Task {
                    try await joinByPairing(link: pairing.link, http: faulty, onEmoji: { emoji.put($0) }, stopped: { false })
                }
                defer { joining.cancel() }
                let expected = try await pairing.joined()
                #expect(await emoji.poll(5_000) == expected.emoji)
                let approving = Task { try await pairing.approve() }
                let account = try await joining.value
                approving.cancel()
                _ = try? await approving.value
                #expect(!(try await login(api: Api(server: w.server.url, http: w.http), account: account)).isEmpty)
            }
        }
    }
}
