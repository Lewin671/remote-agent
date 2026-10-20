import Foundation

extension Remote {
    public nonisolated func setAuthorityAvailability(active: Bool, protectedDataAvailable: Bool) {
        authorityGate.set(active: active, protectedDataAvailable: protectedDataAvailable)
        if active && protectedDataAvailable { Task { await self.authorityForegroundTick() } }
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
    public func removeDevice(_ id: String, abandonPairDelivery: Bool = false) async throws {
        try await authorityCoordinator().removeDevice(id, abandonPairDelivery: abandonPairDelivery)
        try await refreshTrustedDirectory()
    }
    public func deleteMachineRecords(_ id: String) async throws {
        try await authorityCoordinator().deleteMachineRecords(id)
        _ = await refresh()
    }
    public func renewLeaseNow() async throws {
        try await authorityCoordinator().renewLeaseNow()
        try await refreshTrustedDirectory()
    }
    func authorityForegroundTick() async {
        guard (try? authorityGate.ticket()) != nil, !closed else { return }
        do {
            let coordinator = try authorityCoordinator()
            try await coordinator.renewLeaseIfNeeded()
            if let client = deviceSecurity {
                updateLeaseExpiration(try await client.leaseExpiration())
            }
        } catch { /* pending evidence stays available to the foreground security flow */ }
    }
}
