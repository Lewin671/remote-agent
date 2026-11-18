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
        if let cmd {
            trackTransfer(machineId: effect.machineID, command: cmd)
        }

        let hint = effect.forceRelay ? nil : directHint(for: effect)
        let tracking = hint.flatMap { direct?.track(effect.machineID, $0) }
        let build = hint.flatMap { directBuilder(direct, effect.machineID, $0) }
        let handedOffLock = OSAllocatedUnfairLock(initialState: false)
        let delivery = deviceLink.delivery(
            attemptId: effect.attemptID,
            direct: build,
            handoff: { handedOffLock.withLock { $0 = true } }
        )
        ev.routeWasDirect = delivery.isDirect
        let started = ContinuousClock.now

        do {
            if let cmd {
                try requireFileCapability(machineId: effect.machineID, hasFiles: requiresFileCapability(cmd))
            }
            try await delivery.prepare(prior: effect.retryOfUnknown)

            let stateHash = try await authorizationStateHash()
            let (envelope, keyHash, payloadHash) = try await sealedForDispatch(
                effect: effect, stateHash: stateHash)
            try Task.checkCancellation()
            if let cmd {
                try requireFileCapability(machineId: effect.machineID, hasFiles: requiresFileCapability(cmd))
            }

            let res = try await delivery.send(machineId: effect.machineID, command: envelope)
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
                tracking?.failed()
                ev.source = .daemon
                ev.notForwarded = false
                ev.attemptBound = res.attemptID == delivery.attemptId
                ev.hashesMatch = false
                ev.signed = false
                ev.outerMatchesInner = false
                ev.transportCode = CommandError.unknownOutcome
                ev.transportDetail = L10n.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs()
                return ev
            }

            let hashesMatch = msg.requestKeyHash == keyHash && msg.payloadHash == payloadHash
            let outerOutcome = delivery.isDirect ? msg.outcome : res.outcome
            let outerReason = delivery.isDirect ? msg.reason : res.reason
            let outerMatchesInner = delivery.isDirect || (res.outcome == msg.outcome && res.reason == msg.reason)
            let signed = msg.outcome == "conflict" || msg.conflictKind.isEmpty
            let bound = res.attemptID == delivery.attemptId

            let elapsedMs = Double((ContinuousClock.now - started).milliseconds)
            if delivery.isDirect {
                if bound && hashesMatch && outerMatchesInner && signed && msg.outcome != "unknown" {
                    let bytes = effect.bulkFileKey.isEmpty ? 0 : Int(max(0, effect.bulkBytes))
                    tracking?.observe(true, bytes, elapsedMs)
                } else {
                    tracking?.failed()
                }
            } else if bound && hashesMatch && outerMatchesInner && signed && msg.outcome == "executed" {
                let bytes = effect.bulkFileKey.isEmpty ? 0 : Int(max(0, effect.bulkBytes))
                tracking?.observe(false, bytes, elapsedMs)
            }

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
            tracking?.failed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.source = .local
            ev.notForwarded = !handedOff
            ev.transportCode = error.code
            ev.transportDetail = error.message
            return ev
        } catch let error as ConnectError {
            tracking?.failed()
            let handedOff = handedOffLock.withLock { $0 }
            if let refusal = delivery.refusal(error) {
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
            tracking?.failed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.source = .transport
            ev.notForwarded = !handedOff
            ev.transportCode = "path_changed"
            return ev
        } catch {
            tracking?.failed()
            let handedOff = handedOffLock.withLock { $0 }
            ev.source = .transport
            ev.notForwarded = !handedOff
            ev.transportCode = unresolvedHost(error) ? "unresolved_host" : (handedOff ? CommandError.unknownOutcome : "network")
            ev.transportDetail = errorText(error)
            return ev
        }
    }

    private func sealedForDispatch(
        effect: PBCoreDispatchCommandEffect,
        stateHash: Data
    ) async throws -> (PBEnvelope, Data, Data) {
        if let cached = sealedCommands[effect.requestID], cached.stateHash == stateHash {
            return (cached.envelope, cached.keyHash, cached.payloadHash)
        }
        let env = try await authorizeCommandBytes(
            machineId: effect.machineID,
            sessionId: effect.sessionID,
            requestId: effect.requestID,
            deadlineMs: effect.deadlineMs,
            commandBytes: effect.commandBytes
        )
        let binding = try await securitySession().commandIdentity(env)
        storeSealedCommand(
            requestID: effect.requestID,
            entry: SealedCommandEntry(
                stateHash: stateHash, envelope: env, keyHash: binding.key, payloadHash: binding.payload))
        return (env, binding.key, binding.payload)
    }

    // MARK: - Transitional Step 4 Command & Helper APIs (delete after Step 4 migration)

    public nonisolated func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws -> PreparedCommand {
        try requireFileCapability(machineId: machineId, hasFiles: requiresFileCapability(cmd))
        let requestId = newRequestId()
        let deadline = clock() + commandTTLMs
        var versioned = cmd
        versioned.protocolVersion = protocolVersion
        return PreparedCommand(
            machineId: machineId, sessionId: sessionId, requestId: requestId, deadlineMs: deadline,
            command: versioned)
    }

    @discardableResult
    public func machineCommand(_ machineId: String, _ cmd: PBCommand) async throws -> PBCommandResult {
        try await forward(try prepare(machineId: machineId, sessionId: "", cmd))
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
                try await self.machineCommand(machineId, cmd)
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
        if let refusal = protocolMismatch(state.value.machines[p.machineId]) {
            throw CommandError("failed_precondition", refusal, outcome: prior ? .unknown : .notSent)
        }
        trackTransfer(machineId: p.machineId, command: p.command)
        let routeHint = directHint(p.machineId, p.command)
        var forceRelay = false
        var attemptCount = 0

        while true {
            guard clock() < recoveryDeadline else {
                throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult(), outcome: .unknown)
            }
            let build = routeHint != nil && !forceRelay ? directBuilder(direct, p.machineId, routeHint!) : nil
            let tracking = routeHint.flatMap { direct?.track(p.machineId, $0) }
            let started = ContinuousClock.now
            let handedOffLock = OSAllocatedUnfairLock(initialState: false)
            let delivery = deviceLink.delivery(direct: build, handoff: {
                handedOffLock.withLock { $0 = true }
                p.markMayHaveRun()
            })
            do {
                try requireFileCapability(machineId: p.machineId, hasFiles: requiresFileCapability(p.command))
                try await delivery.prepare(prior: p.mayHaveRun)
                let stateHash = try await authorizationStateHash()
                let auth = p.authorizationResult(stateHash: stateHash) { _ in
                    Task {
                        try await self.authorizePrepared(p)
                    }
                }
                let envelope = try await auth.value
                try Task.checkCancellation()
                let binding = try await securitySession().commandIdentity(envelope)
                let res = try await delivery.send(machineId: p.machineIDOr(p.machineId), command: envelope)
                let msg: PBCommandResult
                do {
                    (_, msg) = try await openSecureMessage(
                        envelope: res.result,
                        want: Place(.commandResult, machineId: p.machineId, sessionId: p.sessionId, requestId: p.requestId),
                        as: PBCommandResult.self)
                } catch {
                    tracking?.failed()
                    throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs(), outcome: .unknown)
                }
                let bound = res.attemptID == delivery.attemptId
                    && msg.requestKeyHash == binding.key
                    && msg.payloadHash == binding.payload
                    && (delivery.isDirect || (res.outcome == msg.outcome && res.reason == msg.reason))
                    && (msg.outcome == "conflict" || msg.conflictKind.isEmpty)
                let category = Outcome(rawValue: msg.outcome) ?? .unknown
                guard bound && category != .unknown else {
                    tracking?.failed()
                    throw CommandError(CommandError.unknownOutcome, L10nMessage.errorsIosCannotOpenTheComputerSReplyCommandExecutionIs(), fromDaemon: true, outcome: .unknown)
                }
                if (category == .executed && !msg.resultAvailable) || (category == .conflict && msg.conflictKind == "identity") {
                    _ = await refresh()
                    throw CommandError(
                        msg.errorCode,
                        category == .executed ? L10nMessage.errorsExecutedResultUnavailable() : L10nMessage.errorsRequestIdentityConflict(),
                        fromDaemon: true, outcome: category, conflictKind: msg.conflictKind, resultAvailable: msg.resultAvailable)
                }
                if !msg.errorCode.isEmpty || category != .executed {
                    throw CommandError(
                        msg.errorCode.ifEmpty(msg.outcome),
                        msg.errorMessage.ifEmpty(msg.errorCode),
                        fromDaemon: true, outcome: category, conflictKind: msg.conflictKind)
                }
                let elapsedMs = Double((ContinuousClock.now - started).milliseconds)
                if delivery.isDirect || routeHint != nil {
                    tracking?.observe(delivery.isDirect, 0, elapsedMs)
                }
                return msg
            } catch let e as CommandError {
                tracking?.failed()
                if prior && !e.unknown && !(e.fromDaemon && e.outcome != .notSent) {
                    throw CommandError(e.code, e.message, fromDaemon: e.fromDaemon, readOnly: e.readOnly, outcomeDetail: e.outcomeDetail, outcome: .unknown, conflictKind: e.conflictKind, resultAvailable: e.resultAvailable)
                }
                throw e
            } catch is PathChanged {
                tracking?.failed()
                if delivery.isDirect { forceRelay = true; continue }
                throw CommandError.unknownBecause(L10n.errorsConnectionDroppedMidway())
            } catch let ce as ConnectError {
                tracking?.failed()
                if let refusal = delivery.refusal(ce) {
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
                tracking?.failed()
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

    /// The models a machine's CLI offers, refreshed when a picker opens.
    public func listModels(_ machineId: String, agent: PBAgent = .unspecified, fresh: Bool = false) async throws -> PBListModelsResponse {
        let k = "\(machineId)/\(agent.rawValue)"
        if let held = models[k], !(held.done && (fresh || held.failed)) {
            return try await held.task.value
        }
        let task = Task { () throws -> PBListModelsResponse in
            do {
                var cmd = PBCommand()
                cmd.listModels.agent = agent
                let r = try await self.machineCommand(machineId, cmd)
                guard case .listModels(let list)? = r.kind else {
                    throw CommandError("internal", L10nMessage.newSessionUnexpectedReply())
                }
                self.models[k]?.done = true
                return list
            } catch {
                self.models[k]?.done = true
                self.models[k]?.failed = true
                throw error
            }
        }
        models[k] = ModelList(task: task)
        return try await task.value
    }

    /// The bearer token of the current login, for services used outside this class.
    public func currentToken() async throws -> String {
        if token.isEmpty { return try await signIn() }
        return token
    }

    // --- shared files ---

    public func sharedFileChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse {
        guard let machineId = machineIdForSessionIfKnown(sessionId) else {
            throw CommandError("not_found", L10nMessage.errorsIosUnknownSession())
        }
        var cmd = PBCommand()
        cmd.getSharedFileChunk.sessionID = sessionId
        cmd.getSharedFileChunk.fileID = fileId
        cmd.getSharedFileChunk.offset = offset
        let r = try await forward(try prepare(machineId: machineId, sessionId: sessionId, cmd))
        guard case .getSharedFileChunk(let chunk)? = r.kind else {
            throw CommandError(CommandError.internalFailure, L10nMessage.newSessionUnexpectedReply())
        }
        return chunk
    }

    public nonisolated var sharedFileFetcher: any SharedFileFetching { RemoteSharedFileFetcher(remote: self) }

    // --- images ---

    /// Transitional Step 3-4 helper used by `SessionScreen` -> `ProjectSettingsSheet` until Session migrates into Core.
    public func applyTransitionalPrefsOp(_ op: PrefsOp) async throws {
        let client = try securitySession()
        let operation = newRequestId()
        let apiClient = api.client
        var askReq = PBGetAccountPrefsRequest()
        askReq.objectID = projectPrefsID
        let ask = askReq
        for _ in 0..<5 {
            let current = try await withAuthRetry { headers in
                try await apiClient.getAccountPrefs(request: ask, headers: headers).value()
            }.object
            let plain = try await decodeAccountPrefs(current, trackRef: false)
            if plain.broken {
                throw CommandError("failed_precondition", L10nMessage.errorsProjectPreferencesFormatIsNotSupported())
            }
            let base = plain.hasPrefs ? plain.prefs : PBProjectPrefs()
            if base.recentOps.contains(operation) { return }
            let next = applyPrefsOp(base: base, op: op, opId: operation, now: clock())
            let env = try await client.seal(
                purpose: .prefs,
                request: projectPrefsID,
                seq: current.revision + 1,
                plaintext: try next.serializedData()
            )
            var putReq = PBPutAccountPrefsRequest()
            putReq.objectID = projectPrefsID
            putReq.expectedRevision = current.revision
            putReq.envelope = env
            let put = putReq
            let reply = try await withAuthRetry { headers in
                try await apiClient.putAccountPrefs(request: put, headers: headers).value()
            }
            if reply.applied {
                return
            }
        }
        throw CommandError("aborted", L10nMessage.errorsIosOtherClientsKeptChangingProjectPreferencesChangesCouldNot())
    }

    public func image(_ sid: String, imageId: String) async throws -> ImageResult {
        guard let machineId = machineIdForSessionIfKnown(sid), sessionKeys[sid] != nil else {
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
        case .getMachineStatus, .getSettings, .listModels, .listDirectories, .getLiveSnapshot, .getImageUploadStatus, .getSharedFileChunk:
            true
        case .schedule(let request):
            ["list", "get"].contains(request.operation)
        default:
            false
        }
    }
}

private struct RemoteSharedFileFetcher: SharedFileFetching {
    let remote: Remote
    func fetchChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse {
        try await remote.sharedFileChunk(sessionId: sessionId, fileId: fileId, offset: offset)
    }
}

/// Session ID created by a `createSession` command result, if present.
public func startedSession(_ r: PBCommandResult?) -> String? {
    guard case .createSession(let reply)? = r?.kind, reply.hasSession else { return nil }
    return reply.session.id.isEmpty ? nil : reply.session.id
}

public func listDirectoriesCommand(path: String) -> PBCommand {
    var cmd = PBCommand()
    cmd.listDirectories = .with { $0.path = path }
    return cmd
}

public func getSettingsCommand() -> PBCommand {
    var cmd = PBCommand()
    cmd.getSettings = PBGetSettingsRequest()
    return cmd
}
