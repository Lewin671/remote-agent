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
        guard verifiedNetworkGeneration == networkGeneration else { throw PathChanged() }
        _ = try await securitySession().headStateHash()
    }

    nonisolated func directChanged() {
        directChanges.set(directChanges.value &+ 1)
        Task {
            await self.notifyDirectStatusChanged()
        }
    }

    nonisolated func directObserver() -> SyncObserver {
        guard let direct else { return SyncObserver() }
        return SyncObserver(
            synced: { record, started in await direct.synced(record, started: started) },
            failed: { direct.invalidate() },
            installed: { direct.install($0) }
        )
    }

    /// Which requests may use a DirectPath: read-only ones and bulk. Side effects and terminals stay on the relay.
    func directHint(_ machineId: String, _ cmd: PBCommand) -> RouteHint? {
        switch cmd.kind {
        case .getSharedFileChunk(let c)?: return .bulk(file: "\(c.sessionID)/\(c.fileID)")
        case .putImageChunk(let c)?:
            guard let kind = directTransfers["\(machineId)/\(c.transferID)"] else { return nil }
            return .bulk(file: c.transferID, upload: kind, bytes: c.data.count)
        case .getImageUploadStatus?: return nil
        default: return cmd.isReadOnlyCommand ? .read : nil
        }
    }

    func directHint(for effect: PBCoreDispatchCommandEffect) -> RouteHint? {
        switch effect.routeHint {
        case .read:
            return .read
        case .bulk:
            if effect.bulkUpload {
                return .bulk(file: effect.bulkFileKey, upload: .file, bytes: Int(effect.bulkBytes))
            }
            return .bulk(file: effect.bulkFileKey)
        case .normal, .unspecified, .UNRECOGNIZED:
            return nil
        }
    }

    nonisolated func directBuilder(_ engine: DirectEngine?, _ machine: String, _ hint: RouteHint, probe: Bool = false) -> Link.DirectBuilder? {
        guard let engine else { return nil }
        let build: Link.DirectBuilder = { handoff in engine.attempt(machine, hint, handoff: handoff, probe: probe) }
        return build
    }

    func trackTransfer(machineId: String, command cmd: PBCommand) {
        switch cmd.kind {
        case .beginImageUpload(let c)?:
            if directTransfers.count >= 128, let first = directTransfers.keys.first { directTransfers.removeValue(forKey: first) }
            directTransfers["\(machineId)/\(c.transferID)"] = c.fileName.isEmpty ? .image : .file
        case .completeImageUpload(let c)?: directTransfers.removeValue(forKey: "\(machineId)/\(c.transferID)")
        case .abortImageUpload(let c)?: directTransfers.removeValue(forKey: "\(machineId)/\(c.transferID)")
        default: break
        }
    }

    // MARK: what a screen asks
    public nonisolated var directSupported: Bool { direct != nil }
    public nonisolated func directStatus(_ machineId: String) -> DirectStatus { direct?.status(machineId) ?? DirectStatus(state: .unsupported) }
    public nonisolated func setDirectEnabled(_ machineId: String, _ enabled: Bool) throws { try direct?.setEnabled(machineId, enabled) }
    /// A screen that shows the numbers opened: one warm-up when the window is thin, so they are fresh.
    public nonisolated func refreshDirect(_ machineId: String) { direct?.refresh(machineId) }

    /// One warm-up sample of the read path: an authorized read-only status request of its own.
    func probeRead(_ machineId: String, _ path: RouteName) async {
        guard let direct, !closed else { return }
        log("read sample requested on the \(path == .direct ? "direct" : "relay") path")
        let hint = RouteHint.read, tracking = direct.track(machineId, hint), isDirect = path == .direct
        do {
            var cmd = PBCommand(); cmd.getMachineStatus = PBGetMachineStatusRequest()
            cmd.protocolVersion = protocolVersion
            let requestId = newRequestId()
            let deadline = clock() + commandTTLMs
            let build = isDirect ? directBuilder(direct, machineId, hint, probe: true) : nil
            let delivery = deviceLink.delivery(direct: build)
            guard delivery.isDirect == isDirect else {
                log("read sample: the \(isDirect ? "direct" : "relay") path was not offered")
                return
            }
            let started = ContinuousClock.now
            try await delivery.prepare(prior: false)
            let envelope = try await authorizeCommandBytes(
                machineId: machineId, sessionId: "", requestId: requestId, deadlineMs: deadline,
                commandBytes: try cmd.serializedData())
            let binding = try await securitySession().commandIdentity(envelope)
            let res = try await delivery.send(machineId: machineId, command: envelope)
            let (_, msg) = try await openSecureMessage(
                envelope: res.result,
                want: Place(.commandResult, machineId: machineId, sessionId: "", requestId: requestId),
                as: PBCommandResult.self)
            let outerOutcome = isDirect ? msg.outcome : res.outcome
            let outerReason = isDirect ? msg.reason : res.reason
            let bound = msg.requestKeyHash == binding.key && msg.payloadHash == binding.payload
                && res.attemptID == delivery.attemptId && outerOutcome == msg.outcome && outerReason == msg.reason
            guard bound, msg.errorCode.isEmpty, msg.outcome == "executed" else {
                log("read sample not usable: bound=\(bound) code=\(msg.errorCode)")
                return
            }
            let elapsedMs = Double((ContinuousClock.now - started).milliseconds)
            log("read sample on the \(isDirect ? "direct" : "relay") path: executed in \(Int(elapsedMs)) ms")
            tracking.observe(isDirect, 0, elapsedMs)
        } catch {
            log("read sample on the \(isDirect ? "direct" : "relay") path failed: \(errorText(error))")
            if isDirect { tracking.failed() }
        }
    }
}
