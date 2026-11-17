import Foundation
import RAProto

extension Remote {
    public nonisolated func setAuthorityAvailability(active: Bool, protectedDataAvailable: Bool) {
        authorityGate.set(active: active, protectedDataAvailable: protectedDataAvailable)
        if active && protectedDataAvailable { Task { await self.authorityForegroundTick() } }
    }

    public func authorityCoordinator() throws -> AuthorityCoordinator {
        if let management { return management }
        guard let store = securityStorage as? any AuthorityStorage else { throw DeviceSecurity.Failure.storage }
        let coordinator = AuthorityCoordinator(
            account: account, api: api, storage: store,
            security: try securitySession(), gate: authorityGate, clock: clock,
            token: { [weak self] in
                guard let self else { throw DeviceSecurity.Failure.storage }
                return try await self.currentToken()
            })
        management = coordinator
        return coordinator
    }

    public func isAuthority() throws -> Bool { try securityIdentity().1.core.authorityID == account.deviceId }

    public func removeDevice(_ id: String, abandonPairDelivery: Bool = false) async throws {
        try await authorityCoordinator().removeDevice(id, abandonPairDelivery: abandonPairDelivery)
        await refreshDevices()
    }

    public func deleteMachineRecords(_ id: String) async throws {
        try await authorityCoordinator().deleteMachineRecords(id)
        _ = await refresh()
    }

    public func renewLeaseNow() async throws {
        try await authorityCoordinator().renewLeaseNow()
        await refreshDevices()
    }

    public func handoffCandidates() async throws -> [DeviceView] {
        let listed = try await withAuthRetry { [api] headers in
            try await api.client.listDevices(request: .init(), headers: headers).value()
        }
        var views: [DeviceView] = []
        for d in listed.devices {
            let decoded = await decodeDevice(d)
            views.append(DeviceView(
                id: decoded.deviceID,
                kind: decoded.kind,
                createdMs: decoded.createdMs,
                meta: decoded.hasMeta ? decoded.meta : nil,
                metaRev: decoded.metaRev,
                lastSeenMs: decoded.lastSeenMs
            ))
        }
        return try await authorityCoordinator().handoffCandidates(views)
    }

    public func handoffAuthority(incoming: String) async throws {
        _ = try await securitySession().sync()
        let listed = try await withAuthRetry { [api] headers in
            try await api.client.listDevices(request: .init(), headers: headers).value()
        }
        let r = try securityIdentity()
        let decoded: PBCorePlainDevice?
        if let target = listed.devices.first(where: { $0.id == incoming }) {
            decoded = await decodeDevice(target)
        } else {
            decoded = nil
        }
        guard r.1.core.member(incoming)?.kind == 2,
              let offer = decoded?.meta.handoffRecoveryEd,
              offer.count == 32 else { throw DeviceSecurity.Failure.untrusted }
        try await authorityCoordinator().handoffAuthority(
            incoming: incoming, offer: offer, verifiedHead: DeviceSecurity.stateHash(r.1))
    }

    func authorityForegroundTick() async {
        guard (try? authorityGate.ticket()) != nil, !closed else { return }
        do {
            let coordinator = try authorityCoordinator()
            if let pending = try await coordinator.pendingOperation(), pending.kind == .handoffOffer {
                try? await coordinator.resumeHandoffOffer()
            }
            try await coordinator.renewLeaseIfNeeded()
            if let client = deviceSecurity {
                leaseEndMs = try await client.leaseExpiration()
            }
        } catch { /* pending evidence stays available to the foreground security flow */ }
    }
}
