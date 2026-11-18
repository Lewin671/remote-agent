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

    func securityObserver() -> SyncObserver {
        let direct = self.direct
        return SyncObserver(
            synced: { [weak self] record, started in
                if let direct { await direct.synced(record, started: started) }
                await self?.pushObservedSecuritySnapshot(record)
            },
            failed: { direct?.invalidate() },
            installed: { [weak self] record in
                direct?.install(record)
                Task { await self?.pushObservedSecuritySnapshot(record) }
            }
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
    public nonisolated func directRouteReport(_ machineId: String) -> PBDirectRouteReport {
        directStatus(machineId).toCoreRouteReport()
    }
    public nonisolated func applyDirectPreference(_ machineId: String, enabled: Bool) -> PBDirectRouteReport {
        try? setDirectEnabled(machineId, enabled)
        return directStatus(machineId).toCoreRouteReport()
    }

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

extension DirectStatus {
    public func toCoreRouteReport() -> PBDirectRouteReport {
        let enabled = reason != .disabled
        let activeRtt = readDirect ? readMs[DirectPath.direct] : (state == .connected ? readMs[DirectPath.relay] : nil)
        let readSlow = state == .connected && compared && !readDirect
        let fileSlow = state == .connected && compared && !fileDirect
        return PBDirectRouteReport.with {
            $0.enabled = enabled
            $0.capable = state != .unsupported
            $0.state = switch state {
            case .unsupported: .unsupported
            case .connected: .connected
            case .connecting: .connecting
            case .none: .none
            }
            $0.reason = switch reason {
            case .disabled: .disabled
            case .peerUnsupported: .peerUnsupported
            case .noResponse: .noResponse
            case .noTraversal: .noTraversal
            case .failed: .failed
            case .atCap: .atCap
            case .securityPending: .securityPending
            case .starting: .starting
            case nil: .unspecified
            }
            $0.fileRoute = fileDirect ? .direct : .relay
            $0.readRoute = readDirect ? .direct : .relay
            $0.warming = warming
            $0.compared = compared
            $0.readSlow = readSlow
            $0.fileSlow = fileSlow
            $0.hasDiagnostics_p = reason == .noResponse || reason == .noTraversal || reason == .failed
            if let fd = fileMiBps[DirectPath.direct] { $0.hasDirectFileMibps_p = true; $0.directFileMibps = fd }
            if let fr = fileMiBps[DirectPath.relay] { $0.hasRelayFileMibps_p = true; $0.relayFileMibps = fr }
            if let rd = readMs[DirectPath.direct] { $0.hasDirectReadMs_p = true; $0.directReadMs = Int32(rd.rounded()) }
            if let rr = readMs[DirectPath.relay] { $0.hasRelayReadMs_p = true; $0.relayReadMs = Int32(rr.rounded()) }
            if let rtt = activeRtt { $0.hasActiveRttMs_p = true; $0.activeRttMs = Int32(rtt.rounded()) }
        }
    }
}
