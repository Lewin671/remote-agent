import Foundation
import os
import RAProto

extension CommandError {
    public static func unknownBecause(_ reason: String) -> CommandError {
        CommandError(
            unknownOutcome,
            L10nMessage.errorsIosUnsureWhetherTheComputerExecutedThis(value: String(describing: reason)),
            outcomeDetail: reason, outcome: .unknown)
    }
}

/// A display copy fetched for viewing: its pixels, or why there are none.
public enum ImageResult: Equatable, Sendable {
    case loaded(PBImagePayload)
    /// Kept past its retention: only the preview in the event is left.
    case expired
    /// The server never had it.
    case missing
    /// The bytes are not the image they claim to be, or do not open.
    case corrupt(why: String)
    case retiredHistory
}

extension Remote {
    // MARK: - Direct DataChannel Command Executor (relay legs run in Core)

    /// Executes a single Core-managed direct DataChannel command attempt and returns raw
    /// cryptographic and transport evidence in `PBCoreCommandOutcomeEvent`.
    public func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent {
        var ev = PBCoreCommandOutcomeEvent()
        ev.requestID = effect.requestID
        ev.attemptID = effect.attemptID

        guard effect.hasSealedEnvelope else {
            ev.source = .local
            ev.notForwarded = true
            ev.transportCode = "failed_precondition"
            ev.transportDetail = "missing sealed command"
            return ev
        }

        let useDirect = effect.routeLeg == .direct && effect.directPcID > 0
        guard useDirect else {
            ev.source = .local
            ev.notForwarded = true
            ev.transportCode = "failed_precondition"
            ev.transportDetail = "relay_leg_handled_by_core"
            return ev
        }

        let handedOffLock = OSAllocatedUnfairLock(initialState: false)
        let handoff: @Sendable () throws -> Void = {
            handedOffLock.withLock { $0 = true }
        }
        ev.routeWasDirect = true
        let started = ContinuousClock.now
        func elapsed() -> Double {
            Double(max((ContinuousClock.now - started).milliseconds, 1))
        }

        do {
            try Task.checkCancellation()
            let rawEnv = try effect.sealedEnvelope.serializedData()
            let directEnv = try await directExecutor.sendRpc(
                pcId: effect.directPcID,
                requestId: effect.requestID,
                sealedBytes: rawEnv,
                timeoutMs: effect.directTimeoutMs,
                handoff: handoff
            )
            ev.elapsedMs = elapsed()
            ev.handedOff = handedOffLock.withLock { $0 }
            ev.source = .daemon
            ev.notForwarded = false
            ev.attemptBound = true
            ev.sealedResult = directEnv
            return ev
        } catch let error as CommandError {
            ev.elapsedMs = elapsed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.handedOff = handedOff
            ev.source = .local
            ev.notForwarded = !handedOff
            ev.transportCode = error.code
            ev.transportDetail = error.message
            return ev
        } catch is PathChanged {
            ev.elapsedMs = elapsed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.handedOff = handedOff
            ev.source = .transport
            ev.notForwarded = !handedOff
            ev.transportCode = "path_changed"
            return ev
        } catch {
            ev.elapsedMs = elapsed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.handedOff = handedOff
            ev.source = .transport
            ev.notForwarded = !handedOff
            ev.transportCode = unresolvedHost(error) ? "unresolved_host" : (handedOff ? CommandError.unknownOutcome : "network")
            ev.transportDetail = errorText(error)
            return ev
        }
    }

    // MARK: - Core One-Shot Machine Commands & Probes

    @discardableResult
    public func runMachineCommand(
        machineId: String,
        sessionId: String = "",
        _ cmd: PBCommand,
        skipConnectivityPreflight: Bool = false
    ) async throws -> PBCommandResult {
        if let onRun = hostHooks?.onRunMachineCommand {
            return try await onRun(machineId, sessionId, cmd, skipConnectivityPreflight)
        }
        guard let driver = ensureFallbackSecurityDriver() else {
            throw CommandError(CommandError.canceled, L10nMessage.errorsTerminalConnectionCanceled())
        }
        return try await driver.runMachineCommand(
            machineID: machineId,
            sessionID: sessionId,
            command: cmd,
            skipConnectivityPreflight: skipConnectivityPreflight
        )
    }

    public nonisolated func pingServer(timeoutMs: Int64 = 8_000) async throws -> Int64 {
        var request = URLRequest(url: URL(string: origin.server + "/healthz")!)
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        let http = self.http
        let started = ContinuousClock.now
        do {
            let status = try await withTimeout(timeoutMs, sleep: sleep) { [request] in
                try await http.data(for: request).1.statusCode
            }
            if !(200..<300).contains(status) {
                throw UserError(L10nMessage.errorsServerReturned(value: String(describing: status)))
            }
        } catch is TimedOut {
            throw UserError(L10nMessage.connectionNoResponse())
        } catch let e as URLError where e.code == .timedOut {
            throw UserError(L10nMessage.connectionNoResponse())
        }
        return (ContinuousClock.now - started).milliseconds
    }

    public nonisolated func pingMachine(_ machineId: String, timeoutMs: Int64 = 15_000) async throws -> Int64 {
        var cmd = PBCommand()
        cmd.getSettings = PBGetSettingsRequest()
        let started = ContinuousClock.now
        do {
            _ = try await withTimeout(timeoutMs, sleep: sleep) { [cmd] in
                try await self.runMachineCommand(machineId: machineId, sessionId: "", cmd, skipConnectivityPreflight: true)
            }
        } catch is TimedOut {
            throw UserError(L10nMessage.connectionNoResponse())
        }
        return (ContinuousClock.now - started).milliseconds
    }

    // --- images (fetched, decrypted, and validated in Go clientcore) ---

    public func image(_ sid: String, imageId: String) async throws -> ImageResult {
        try await ensureSecurityBootstrapped()
        let cacheKey = "\(sid)/\(imageId)"
        let cachedEnvelope = await cache?.image(key: cacheKey)
        let machineId = machineIdForSessionIfKnown(sid) ?? ""
        let res = try await fetchSessionImageViaCore(
            sessionId: sid,
            imageId: imageId,
            cachedEnvelope: cachedEnvelope,
            machineId: machineId
        )
        switch res.status {
        case .ok:
            if !res.cacheEnvelopeBytes.isEmpty {
                await cache?.putImage(key: cacheKey, envelope: res.cacheEnvelopeBytes)
            }
            var payload = PBImagePayload()
            payload.format = res.format
            payload.width = Int32(res.width)
            payload.height = Int32(res.height)
            payload.data = res.data
            return .loaded(payload)
        case .missing:
            return .missing
        case .expired:
            return .expired
        case .bad:
            if res.retiredHistory { return .retiredHistory }
            let msg = res.hasErrorText
                ? renderCoreText(res.errorText)
                : L10n.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed()
            return .corrupt(why: msg.isEmpty ? L10n.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed() : msg)
        default:
            let code = res.errorCode.isEmpty ? "unavailable" : res.errorCode
            let rendered = res.hasErrorText ? renderCoreText(res.errorText) : ""
            throw CommandError(
                code,
                rendered.isEmpty ? L10nMessage.connectionNoServer() : LocalizedMessage(raw: rendered)
            )
        }
    }
}

extension Duration {
    var milliseconds: Int64 {
        components.seconds * 1000 + components.attoseconds / 1_000_000_000_000_000
    }
}
