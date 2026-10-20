import Foundation

extension Remote {
    public nonisolated func setAuthorityAvailability(active: Bool, protectedDataAvailable: Bool) {
        authorityGate.set(active: active, protectedDataAvailable: protectedDataAvailable)
    }
    public func authorityCoordinator() throws -> AuthorityCoordinator {
        if let management { return management }
        guard let store = securityStorage as? any AuthorityStorage else { throw DeviceSecurity.Failure.storage }
        let coordinator = AuthorityCoordinator(account: account, api: api, storage: store,
            security: try securitySession(), gate: authorityGate, clock: clock,
            token: { [weak self] in
                guard let self else { throw DeviceSecurity.Failure.storage }
                return try await self.currentToken()
            })
        management = coordinator; return coordinator
    }
    public func isAuthority() throws -> Bool { try securityIdentity().1.core.authorityID == account.deviceId }
}
