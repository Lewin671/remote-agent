import Connect
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
    // MARK: - M2 Core Command Attempt Executor

    /// Executes a single Core-managed command attempt and returns raw cryptographic and transport
    /// evidence in `PBCoreCommandOutcomeEvent`. Core owns retry scheduling, freezing, and outcome classification.
    public func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent {
        var ev = PBCoreCommandOutcomeEvent()
        ev.requestID = effect.requestID
        ev.attemptID = effect.attemptID

        let cmd = try? PBCommand(serializedBytes: effect.commandBytes)

        let useDirect = effect.routeLeg == .direct && effect.directPcID > 0
        let handedOffLock = OSAllocatedUnfairLock(initialState: false)
        let handoff: @Sendable () throws -> Void = {
            handedOffLock.withLock { $0 = true }
        }
        let relayAttempt = useDirect ? nil : deviceLink.delivery(attemptId: effect.attemptID, handoff: handoff)
        ev.routeWasDirect = useDirect
        let started = ContinuousClock.now
        func elapsed() -> Double {
            Double(max((ContinuousClock.now - started).milliseconds, 1))
        }

        do {
            if let relayAttempt {
                try await relayAttempt.prepare(prior: effect.retryOfUnknown)
            }

            let envelope: PBEnvelope
            let keyHash: Data
            let payloadHash: Data
            if effect.hasSealedEnvelope {
                envelope = effect.sealedEnvelope
                keyHash = Data()
                payloadHash = Data()
            } else {
                let auth = try await authorizeCommandBytes(
                    machineId: effect.machineID,
                    sessionId: effect.sessionID,
                    requestId: effect.requestID,
                    deadlineMs: effect.deadlineMs,
                    commandBytes: effect.commandBytes
                )
                envelope = auth.envelope
                keyHash = auth.keyHash
                payloadHash = auth.payloadHash
            }
            try Task.checkCancellation()

            let res: PBForwardResponse
            if useDirect {
                let rawEnv = try envelope.serializedData()
                let directEnv = try await directExecutor.sendRpc(
                    pcId: effect.directPcID,
                    requestId: effect.requestID,
                    sealedBytes: rawEnv,
                    timeoutMs: effect.directTimeoutMs,
                    handoff: handoff
                )
                res = PBForwardResponse.with {
                    $0.result = directEnv
                    $0.attemptID = effect.attemptID
                }
            } else {
                res = try await relayAttempt!.send(machineId: effect.machineID, command: envelope)
            }
            ev.elapsedMs = elapsed()
            ev.handedOff = handedOffLock.withLock { $0 }

            if effect.hasSealedEnvelope {
                let bound = res.attemptID == effect.attemptID
                if bound,
                   let cmd, case .schedule(let req)? = cmd.kind, req.operation == "list",
                   res.hasResult,
                   let (_, msg) = try? await openSecureMessage(
                       envelope: res.result,
                       want: Place(
                           .commandResult,
                           machineId: effect.machineID,
                           sessionId: effect.sessionID,
                           requestId: effect.requestID),
                       as: PBCommandResult.self),
                   msg.outcome == "executed", msg.errorCode.isEmpty, msg.resultAvailable,
                   case .schedule? = msg.kind,
                   let envBytes = try? res.result.serializedData() {
                    await keepScheduleSnapshot(effect.machineID, data: envBytes)
                }
                ev.source = .daemon
                ev.notForwarded = false
                ev.attemptBound = bound
                ev.outerOutcome = useDirect ? "" : res.outcome
                ev.outerReason = useDirect ? "" : res.reason
                if res.hasResult {
                    ev.sealedResult = res.result
                }
                return ev
            }

            let (_, msg): (PBHeader, PBCommandResult)
            do {
                (_, msg) = try await openSecureMessage(
                    envelope: res.result,
                    want: Place(
                        .commandResult,
                        machineId: effect.machineID,
                        sessionId: effect.sessionID,
                        requestId: effect.requestID),
                    as: PBCommandResult.self)
            } catch {
                ev.source = .daemon
                ev.notForwarded = false
                ev.attemptBound = res.attemptID == effect.attemptID
                ev.hashesMatch = false
                ev.signed = false
                ev.outerMatchesInner = false
                ev.transportCode = CommandError.unknownOutcome
                ev.transportDetail = L10n.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs()
                return ev
            }

            let hashesMatch = msg.requestKeyHash == keyHash && msg.payloadHash == payloadHash
            let outerOutcome = useDirect ? msg.outcome : res.outcome
            let outerReason = useDirect ? msg.reason : res.reason
            let outerMatchesInner = useDirect || (res.outcome == msg.outcome && res.reason == msg.reason)
            let signed = msg.outcome == "conflict" || msg.conflictKind.isEmpty
            let bound = res.attemptID == effect.attemptID

            let obsBytes: Int64 = if let cmd, case .putImageChunk(let put)? = cmd.kind {
                Int64(put.data.count)
            } else if case .getSharedFileChunk(let chunk)? = msg.kind {
                Int64(chunk.data.count)
            } else {
                0
            }
            ev.observedBytes = obsBytes

            if bound && hashesMatch && outerMatchesInner && signed && msg.outcome == "executed",
               msg.errorCode.isEmpty, msg.resultAvailable,
               let cmd, case .schedule(let req)? = cmd.kind, req.operation == "list",
               case .schedule? = msg.kind,
               let envBytes = try? res.result.serializedData() {
                await keepScheduleSnapshot(effect.machineID, data: envBytes)
            }

            ev.source = .daemon
            ev.notForwarded = false
            ev.attemptBound = bound
            ev.hashesMatch = hashesMatch
            ev.outerMatchesInner = outerMatchesInner
            ev.signed = signed
            ev.outerOutcome = outerOutcome
            ev.outerReason = outerReason
            ev.result = msg
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
        } catch let error as ConnectError {
            ev.elapsedMs = elapsed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.handedOff = handedOff
            if let relayAttempt, let refusal = relayAttempt.refusal(error) {
                if error.code == .unauthenticated { dropToken() }
                ev.source = .server
                ev.notForwarded = true
                ev.attemptBound = true
                ev.outerReason = refusal.reason
                ev.transportCode = error.code.name
                ev.transportDetail = errorText(error)
            } else if error.code == .unauthenticated {
                dropToken()
                ev.source = .server
                ev.notForwarded = !handedOff
                ev.transportCode = "unauthenticated"
                ev.transportDetail = errorText(error)
            } else if error.code == .unavailable {
                ev.source = .transport
                ev.notForwarded = !handedOff
                ev.transportCode = CommandError.unavailable
                ev.transportDetail = errorText(error)
            } else {
                ev.source = .server
                ev.notForwarded = !handedOff
                ev.attemptBound = false
                ev.transportCode = error.code.name
                ev.transportDetail = errorText(error)
            }
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

    // MARK: - Transitional Step 4 Command & Helper APIs (delete after Step 4 migration)

    public nonisolated func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws -> PreparedCommand {
        let requestId = newRequestId()
        let deadline = clock() + commandTTLMs
        var versioned = cmd
        versioned.protocolVersion = peerProtocolVersion
        return PreparedCommand(
            machineId: machineId, sessionId: sessionId, requestId: requestId, deadlineMs: deadline,
            command: versioned)
    }

    @discardableResult
    public func machineCommand(_ machineId: String, _ cmd: PBCommand) async throws -> PBCommandResult {
        try await forward(try prepare(machineId: machineId, sessionId: "", cmd))
    }

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
            throw CommandError("canceled", L10nMessage.errorsTerminalConnectionCanceled())
        }
        return try await driver.runMachineCommand(
            machineID: machineId,
            sessionID: sessionId,
            command: cmd,
            skipConnectivityPreflight: skipConnectivityPreflight
        )
    }

    public nonisolated func pingServer(timeoutMs: Int64 = 8_000) async throws -> Int64 {
        var request = URLRequest(url: URL(string: api.server + "/healthz")!)
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

    @discardableResult
    public func forward(_ p: PreparedCommand, retryOfUnknown: Bool = false) async throws -> PBCommandResult {
        let (sum, overflow) = p.deadlineMs.addingReportingOverflow(resultRetentionMs)
        let recoveryDeadline = overflow ? Int64.max : sum
        let prior = retryOfUnknown || p.mayHaveRun
        if !prior && clock() >= p.deadlineMs {
            throw CommandError(CommandError.expired, L10nMessage.errorsIosThisCommandExpiredAndWasNotSent())
        }
        guard clock() < recoveryDeadline else {
            throw prior
                ? CommandError(CommandError.unknownOutcome, L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult(), outcome: .unknown)
                : CommandError(CommandError.expired, L10nMessage.errorsIosThisCommandExpiredAndWasNotSent())
        }
        if let mp = machineProtocols[p.machineId] {
            if mp.version < peerProtocolVersion {
                throw CommandError("failed_precondition", L10n.errorsRemoteAgentOnIsOutOfDateUpdateIt(value: mp.name), outcome: prior ? .unknown : .notSent)
            }
            if mp.version > peerProtocolVersion {
                throw CommandError("failed_precondition", L10n.errorsIosThisAppVersionIsOutOfDateUpdateAnd(), outcome: prior ? .unknown : .notSent)
            }
        }
        let bulkHint = directBulkHint(p.command)
        var forceRelay = false
        var attemptCount = 0

        while true {
            guard clock() < recoveryDeadline else {
                throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult(), outcome: .unknown)
            }
            let attemptId = newRequestId()
            let dec: PBCoreDirectBulkRouteEffect
            if bulkHint.routeHintValue != 0, let hooks = hostHooks {
                let beginEv = PBCoreDirectBulkTransferBeginEvent.with {
                    $0.machineID = p.machineId
                    $0.routeHint = bulkHint.routeHintValue
                    $0.bulkFileKey = bulkHint.bulkFileKey
                    $0.deadlineMs = p.deadlineMs
                    $0.forceRelay = forceRelay
                }
                dec = await hooks.onBeginDirectBulkTransfer(beginEv)
            } else {
                dec = PBCoreDirectBulkRouteEffect()
            }
            let useDirect = dec.useDirect && !forceRelay && dec.pcID > 0
            let started = ContinuousClock.now
            let handoff: @Sendable () throws -> Void = { p.markMayHaveRun() }
            let relayAttempt = useDirect ? nil : deviceLink.delivery(attemptId: attemptId, handoff: handoff)
            let initialBytes: Int64 = 0
            @Sendable func reportRoute(_ ok: Bool, _ obsBytes: Int64) async {
                guard dec.ticketID != 0 else { return }
                let done = PBCoreDirectBulkTransferDoneEvent.with {
                    $0.ticketID = dec.ticketID
                    $0.routeWasDirect = useDirect
                    $0.ok = ok
                    $0.elapsedMs = Double(max((ContinuousClock.now - started).milliseconds, 1))
                    $0.observedBytes = obsBytes
                    $0.machineID = p.machineId
                }
                await hostHooks?.onFinishDirectBulkTransfer(done)
            }
            var routeReported = false
            do {
                if let relayAttempt {
                    try await relayAttempt.prepare(prior: p.mayHaveRun)
                }
                let stateHash = try await authorizationStateHash()
                let auth = p.authorizationResult(stateHash: stateHash) { _ in
                    Task {
                        try await self.authorizePrepared(p)
                    }
                }
                let authorized = try await auth.value
                try Task.checkCancellation()
                let envelope = authorized.envelope
                let res: PBForwardResponse
                if useDirect {
                    let directEnv = try await directExecutor.sendRpc(
                        pcId: dec.pcID,
                        requestId: p.requestId,
                        sealedBytes: try envelope.serializedData(),
                        timeoutMs: dec.timeoutMs,
                        handoff: handoff
                    )
                    res = PBForwardResponse.with {
                        $0.result = directEnv
                        $0.attemptID = attemptId
                    }
                } else {
                    res = try await relayAttempt!.send(machineId: p.machineIDOr(p.machineId), command: envelope)
                }
                let msg: PBCommandResult
                do {
                    (_, msg) = try await openSecureMessage(
                        envelope: res.result,
                        want: Place(.commandResult, machineId: p.machineId, sessionId: p.sessionId, requestId: p.requestId),
                        as: PBCommandResult.self)
                } catch {
                    routeReported = true
                    await reportRoute(false, initialBytes)
                    throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs(), outcome: .unknown)
                }
                let bound = res.attemptID == attemptId
                    && msg.requestKeyHash == authorized.keyHash
                    && msg.payloadHash == authorized.payloadHash
                    && (useDirect || (res.outcome == msg.outcome && res.reason == msg.reason))
                    && (msg.outcome == "conflict" || msg.conflictKind.isEmpty)
                let category = Outcome(rawValue: msg.outcome) ?? .unknown
                guard bound && category != .unknown else {
                    routeReported = true
                    await reportRoute(false, initialBytes)
                    throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs(), fromDaemon: true, outcome: .unknown)
                }
                if (category == .executed && !msg.resultAvailable) || (category == .conflict && msg.conflictKind == "identity") {
                    routeReported = true
                    await reportRoute(false, initialBytes)
                    _ = await refresh()
                    throw CommandError(
                        msg.errorCode,
                        category == .executed ? L10nMessage.errorsExecutedResultUnavailable() : L10nMessage.errorsRequestIdentityConflict(),
                        fromDaemon: true, outcome: category, conflictKind: msg.conflictKind, resultAvailable: msg.resultAvailable)
                }
                if !msg.errorCode.isEmpty || category != .executed {
                    routeReported = true
                    await reportRoute(category == .executed, initialBytes)
                    throw CommandError(
                        msg.errorCode.ifEmpty(msg.outcome),
                        msg.errorMessage.ifEmpty(msg.errorCode),
                        fromDaemon: true, outcome: category, conflictKind: msg.conflictKind)
                }
                var finalBytes = initialBytes
                if case .getSharedFileChunk(let chunk)? = msg.kind {
                    finalBytes = Int64(chunk.data.count)
                }
                routeReported = true
                await reportRoute(true, finalBytes)
                return msg
            } catch let e as CommandError {
                if !routeReported { await reportRoute(false, initialBytes) }
                if prior && !e.unknown && !(e.fromDaemon && e.outcome != .notSent) {
                    throw CommandError(e.code, e.message, fromDaemon: e.fromDaemon, readOnly: e.readOnly, outcomeDetail: e.outcomeDetail, outcome: .unknown, conflictKind: e.conflictKind, resultAvailable: e.resultAvailable)
                }
                throw e
            } catch is PathChanged {
                if !routeReported { await reportRoute(false, initialBytes) }
                if useDirect { forceRelay = true; continue }
                throw CommandError.unknownBecause(L10n.errorsConnectionDroppedMidway())
            } catch let ce as ConnectError {
                if !routeReported { await reportRoute(false, initialBytes) }
                if let relayAttempt, let refusal = relayAttempt.refusal(ce) {
                    if ce.code == .unauthenticated {
                        rejectCredential(token)
                        attemptCount += 1
                        if attemptCount < 3 { continue }
                    }
                    throw CommandError(ce.code.name, refusal.reason.ifEmpty(errorText(ce)), outcome: p.mayHaveRun ? .unknown : .notSent)
                }
                if ce.code == .unauthenticated, attemptCount < 2 {
                    attemptCount += 1
                    rejectCredential(token)
                    continue
                }
                if (ce.code == .unavailable || ce.code == .deadlineExceeded), attemptCount < 3, clock() + 500 < recoveryDeadline {
                    attemptCount += 1
                    try? await sleep(500)
                    continue
                }
                if p.mayHaveRun { throw CommandError.unknownBecause(errorText(ce)) }
                throw CommandError(ce.code.name, errorText(ce))
            } catch {
                if !routeReported { await reportRoute(false, initialBytes) }
                if p.mayHaveRun { throw CommandError.unknownBecause(errorText(error)) }
                throw error
            }
        }
    }

    func relogin(_ mayHaveRun: Bool) async throws -> String {
        do {
            return try await signIn()
        } catch {
            if !mayHaveRun { throw error }
            throw CommandError.unknownBecause(
                L10n.errorsIosTheRequestMayHaveArrivedButLoginThenFailed(value: String(describing: errorText(error))))
        }
    }

    /// The bearer token of the current login, for services used outside this class.
    public func currentToken() async throws -> String {
        if token.isEmpty { return try await signIn() }
        return token
    }

    // --- images ---

    public func image(_ sid: String, imageId: String) async throws -> ImageResult {
        guard let machineId = machineIdForSessionIfKnown(sid), await hasSessionSeed(sid) else {
            throw CommandError("not_found", L10nMessage.errorsIosUnknownSession())
        }
        let cacheKey = "\(sid)/\(imageId)"
        var env = await cache?.image(key: cacheKey).flatMap { try? PBEnvelope(serializedBytes: $0) }
        if env == nil {
            var askReq = PBGetImageRequest()
            askReq.sessionID = sid
            askReq.imageID = imageId
            let ask = askReq
            let r: PBGetImageResponse
            do {
                r = try await withAuthRetry { [api] headers in
                    try await api.client.getImage(request: ask, headers: headers).value()
                }
            } catch let e as ConnectError where e.code == .notFound {
                return .missing
            }
            if r.expired || !r.hasImage { return .expired }
            env = r.image
        }
        let payload: PBImagePayload
        do {
            (_, payload) = try await openSecureMessage(
                envelope: env,
                want: Place(.image, machineId: machineId, sessionId: sid, requestId: imageId),
                as: PBImagePayload.self, history: true)
        } catch is UnsupportedHistoryError {
            return .retiredHistory
        } catch {
            return .corrupt(why: L10n.errorsIosImageContentOrOriginCouldNotBeVerified())
        }
        if detectFormat(payload.data) != payload.format {
            return .corrupt(why: L10n.errorsIosImageContentDoesNotMatchItsFormat())
        }
        if let env, let bytes: Data = try? env.serializedData() {
            await cache?.putImage(key: cacheKey, envelope: bytes)
        }
        return .loaded(payload)
    }
}

private extension PreparedCommand {
    func machineIDOr(_ fallback: String) -> String { machineId.isEmpty ? fallback : machineId }
}

extension Duration {
    var milliseconds: Int64 {
        components.seconds * 1000 + components.attoseconds / 1_000_000_000_000_000
    }
}

// Keep in step with deviceauth.IsReadOnlyCommand; unknown kinds mutate.
extension PBCommand {
    var isReadOnlyCommand: Bool {
        switch kind {
        case .getMachineStatus, .getUsageStats, .getSettings, .listModels, .listDirectories, .getLiveSnapshot, .getImageUploadStatus, .getSharedFileChunk:
            true
        case .schedule(let request):
            ["list", "get"].contains(request.operation)
        default:
            false
        }
    }
}
