import Foundation
import RAProto

/// A separate signing domain prevents a login proof from authorizing deletion.
public func accountDeletionMessage(account: String, head: Data, challenge: Data) -> Data {
    Data(("remote-agent/v1/delete-account\0" + account + "\0").utf8) + head + challenge
}

extension AuthorityCoordinator {
    /// Explicit, foreground-only management action. No automatic retry: a lost
    /// response retains local keys so the caller can explain the unknown outcome.
    public func deleteAccount() async throws {
        let ticket = try begin()
        defer { busy = false }
        guard try storage.readOperation() == nil else {
            throw CommandError("pending", L10nMessage.securityIosFinishOrAbandonUnfinishedAccountSecurityOperationsBeforeDeleting())
        }
        _ = try await rpc(ticket) { [security] in try await security.sync() }
        let record = try authority(ticket)
        let head = try DeviceSecurity.stateHash(record.state)
        let headers = Api.headers(token: try await rpc(ticket, token))
        var challengeRequest = PBGetChallengeRequest()
        challengeRequest.deviceID = account.deviceId
        let challenge = try await rpc(ticket) { [api, challengeRequest] in
            try await api.auth.getChallenge(request: challengeRequest).value().challenge
        }
        try DeviceSecurity.check(challenge.count == 32, .invalid)
        _ = try authority(ticket, head: head)
        guard try storage.readOperation() == nil else { throw DeviceSecurity.Failure.conflict }
        var request = PBDeleteAccountRequest()
        request.stateHash = head
        request.challenge = challenge
        request.signature = try gate.withPermit(ticket) {
            try SigningKey(seed: account.deviceSeed).sign(accountDeletionMessage(
                account: record.pin.account, head: head, challenge: challenge))
        }
        try await rpc(ticket) { [api, request] in
            _ = try await api.client.deleteAccount(request: request, headers: headers).value()
        }
    }
}

extension Remote {
    public func deleteAccount() async throws {
        try await authorityCoordinator().deleteAccount()
    }
}
