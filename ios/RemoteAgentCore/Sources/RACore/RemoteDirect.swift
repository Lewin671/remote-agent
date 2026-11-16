import Foundation
import RAProto

/// Weak, so the engine's callbacks (made before the actor exists) never keep it alive.
final class WeakRemote: @unchecked Sendable {
    private let lock = NSLock(); private weak var stored: Remote?
    var remote: Remote? { get { lock.withLock { stored } } set { lock.withLock { stored = newValue } } }
}

// Direct paths for read-only requests and bulk (docs/transport.md §3 「只读选路」): what Remote adds
// around DirectEngine. The engine owns sessions and routing; Remote keeps the keys, the
// authorization and the verification of every answer, whichever path carried it.
extension Remote {
    func directAuthorize() async throws {
        // A network change makes the security state stale until the main connection's sync verified it again.
        guard verifiedNetworkGeneration == networkGeneration else { throw PathChanged() }
        _ = try await securitySession().headStateHash()
    }
    nonisolated func directChanged() { directChanges.set(directChanges.value &+ 1) }
    nonisolated func directObserver() -> SyncObserver {
        guard let direct else { return SyncObserver() }
        return SyncObserver(synced: { record, started in await direct.synced(record, started: started) }, failed: { direct.invalidate() }, installed: { direct.install($0) })
    }

    /// Which requests may use a DirectPath: read-only ones and bulk. Side effects and terminals stay on the relay.
    func directHint(_ machineId: String, _ cmd: PBCommand) -> RouteHint? {
        switch cmd.kind {
        case .getSharedFileChunk(let c)?: return .bulk(file: "\(c.sessionID)/\(c.fileID)")
        case .putImageChunk(let c)?:
            guard let kind = directTransfers["\(machineId)/\(c.transferID)"] else { return nil }
            return .bulk(file: c.transferID, upload: kind, bytes: c.data.count)
        // Recovery status stays on the independent relay before a chunk is retried.
        case .getImageUploadStatus?: return nil
        default: return cmd.isReadOnlyCommand ? .read : nil
        }
    }
    nonisolated func directBuilder(_ engine: DirectEngine?, _ machine: String, _ hint: RouteHint, probe: Bool = false) -> Link.DirectBuilder? {
        guard let engine else { return nil }
        let build: Link.DirectBuilder = { handoff in engine.attempt(machine, hint, handoff: handoff, probe: probe) }
        return build
    }
    func trackTransfer(_ p: PreparedCommand) {
        switch p.command.kind {
        case .beginImageUpload(let c)?:
            if directTransfers.count >= 128, let first = directTransfers.keys.first { directTransfers.removeValue(forKey: first) }
            directTransfers["\(p.machineId)/\(c.transferID)"] = c.fileName.isEmpty ? .image : .file
        case .completeImageUpload(let c)?: directTransfers.removeValue(forKey: "\(p.machineId)/\(c.transferID)")
        case .abortImageUpload(let c)?: directTransfers.removeValue(forKey: "\(p.machineId)/\(c.transferID)")
        default: break
        }
    }

    // MARK: what a screen asks
    public nonisolated var directSupported: Bool { direct != nil }
    public nonisolated func directStatus(_ machineId: String) -> DirectStatus { direct?.status(machineId) ?? DirectStatus(state: .unsupported) }
    public nonisolated func directEnabled(_ machineId: String) -> Bool { direct?.enabled(machineId) ?? false }
    public nonisolated func setDirectEnabled(_ machineId: String, _ enabled: Bool) throws { try direct?.setEnabled(machineId, enabled) }
    /// A screen that shows the numbers opened: one warm-up when the window is thin, so they are fresh.
    public nonisolated func refreshDirect(_ machineId: String) { direct?.refresh(machineId) }

    /// One warm-up sample of the read path: an authorized read-only status request of its own, on the
    /// path asked for, timed like a user's read. It never changes a user's request, result or
    /// deadline; its count, rate and alternation are bounded by the routing that asks. A failed
    /// direct sample demotes direct, a failed relay one is only a missing sample.
    func probeRead(_ machineId: String, _ path: RouteName) async {
        guard let direct, !closed else { return }
        let hint = RouteHint.read, tracking = direct.track(machineId, hint), isDirect = path == .direct
        do {
            var cmd = PBCommand(); cmd.getMachineStatus = PBGetMachineStatusRequest()
            let p = try prepare(machineId: machineId, sessionId: "", cmd)
            let link = deviceLink
            let work: @Sendable (DispatchState.Call) async throws -> Void = { call in
                try await self.probeOnce(p, machineId, isDirect, call, link, direct, hint, tracking)
            }
            try await link.call(p, prior: false, work: work)
        } catch { if isDirect { tracking.failed() } }
    }

    private func probeOnce(_ p: PreparedCommand, _ machineId: String, _ isDirect: Bool, _ call: DispatchState.Call, _ link: Link, _ direct: DirectEngine, _ hint: RouteHint, _ tracking: DirectEngine.Tracking) async throws {
        let started = ContinuousClock.now
        let build = isDirect ? directBuilder(direct, machineId, hint, probe: true) : nil
        let delivery = link.delivery(call, direct: build)
        guard delivery.isDirect == isDirect else { return }
        try await delivery.prepare(prior: false)
        let envelope = try await authorizePrepared(p, verifyObservation: false)
        let binding = try await securitySession().commandIdentity(envelope)
        let res = try await delivery.send(machineId: machineId, command: envelope)
        let (_, msg) = try await openSecureMessage(envelope: res.result, want: Place(.commandResult, machineId: machineId, sessionId: "", requestId: p.requestId), as: PBCommandResult.self)
        let outerOutcome = isDirect ? msg.outcome : res.outcome, outerReason = isDirect ? msg.reason : res.reason
        let bound = msg.requestKeyHash == binding.key && msg.payloadHash == binding.payload && res.attemptID == delivery.attemptId && outerOutcome == msg.outcome && outerReason == msg.reason
        guard bound, msg.errorCode.isEmpty else { return }
        let category = verifiedOutcome(msg, key: binding.key, payload: binding.payload, outerOutcome: outerOutcome, outerReason: outerReason)
        if category == .executed { tracking.observe(isDirect, 0, Double((ContinuousClock.now - started).milliseconds)) }
    }
}
