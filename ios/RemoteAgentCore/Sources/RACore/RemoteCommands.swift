// Sealed commands through the server's Forward, and what an answer that
// did not come means (architecture §10 「指令」): a command that may have
// reached the computer is never called failed, only unknown, and is sent
// again as it was.
import Connect
import Foundation
import RAProto

/// A display copy fetched for viewing: its pixels, or why there are none.
public enum ImageResult: Equatable, Sendable {
    case loaded(PBImagePayload)
    /// Readable old-root data without authenticated provenance.
    case legacy(PBImagePayload)
    /// Kept past its retention: only the preview in the event is left.
    case expired
    /// The server never had it.
    case missing
    /// The bytes are not the image they claim to be, or do not open.
    case corrupt(why: String)
}

extension Remote {
    /// Seals a command. The header names no session for machine commands.
    public nonisolated func prepare(machineId: String, sessionId: String, _ cmd: PBCommand) throws
        -> PreparedCommand
    {
        let requestId = newRequestId()
        let deadline = clock() + commandTTLMs
        var h = PBHeader()
        h.kind = .command
        h.machineID = machineId
        h.sessionID = sessionId
        h.requestID = requestId
        h.deadlineMs = deadline
        h.sender = account.deviceId
        var versioned = cmd
        versioned.protocolVersion = protocolVersion
        versioned = try observedCommand(versioned, snapshot: eventFeed(sessionId).value.live)
        // Signed by this device, so a daemon can tell who sent it
        // (architecture §9).
        return PreparedCommand(
            machineId: machineId, sessionId: sessionId, requestId: requestId, deadlineMs: deadline,
            command: versioned)
    }

    /// Sends a command about a session.
    @discardableResult
    public func command(_ sid: String, _ cmd: PBCommand) async throws -> PBCommandResult {
        guard let s = sessions[sid] else { throw CommandError("not_found", "未知会话") }
        return try await forward(try prepare(machineId: s.machineId, sessionId: sid, cmd))
    }

    /// Sends a command about a machine, such as starting a session.
    @discardableResult
    public func machineCommand(_ machineId: String, _ cmd: PBCommand) async throws
        -> PBCommandResult
    {
        try await forward(try prepare(machineId: machineId, sessionId: "", cmd))
    }

    /// How long one round trip to the server takes now, in ms: a request that does nothing
    /// there (连接检测). Throws when no answer comes in `timeoutMs`.
    public nonisolated func pingServer(timeoutMs: Int64 = 8_000) async throws -> Int64 {
        var request = URLRequest(url: URL(string: api.server + "/healthz")!)
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        let http = self.http
        let started = ContinuousClock.now
        do {
            let status = try await withTimeout(timeoutMs, sleep: sleep) { [request] in
                try await http.data(for: request).1.statusCode
            }
            if !(200..<300).contains(status) { throw UserError("服务器返回 \(status)") }
        } catch is TimedOut {
            throw UserError("没有回应")
        } catch let e as URLError where e.code == .timedOut {
            throw UserError("没有回应")
        }
        return (ContinuousClock.now - started).milliseconds
    }

    /// How long one round trip to a computer takes, through the server and back: a sealed command that only reads its settings.
    public nonisolated func pingMachine(_ machineId: String, timeoutMs: Int64 = 15_000) async throws
        -> Int64
    {
        var cmd = PBCommand()
        cmd.getSettings = PBGetSettingsRequest()
        let started = ContinuousClock.now
        do {
            _ = try await withTimeout(timeoutMs, sleep: sleep) { [cmd] in
                try await self.machineCommand(machineId, cmd)
            }
        } catch is TimedOut {
            throw UserError("没有回应")
        }
        return (ContinuousClock.now - started).milliseconds
    }

    /// Forwards a prepared command and returns the opened result. Transport
    /// failures are retried with the same request ID, which the daemon runs at
    /// most once; when the outcome stays unknown the error says so.
    @discardableResult
    public func forward(_ p: PreparedCommand, retryOfUnknown: Bool = false) async throws
        -> PBCommandResult
    {
        // A machine of another protocol version would refuse it: say which
        // side to update instead of sending.
        if let refusal = protocolMismatch(state.value.machines[p.machineId]) {
            throw CommandError("failed_precondition", refusal)
        }
        // A command whose outcome is unknown is sent even when expired: the
        // daemon may still have its result in its log.
        if !retryOfUnknown && clock() >= p.deadlineMs {
            throw CommandError(CommandError.expired, "这条指令已过期，没有发出")
        }
        var attempt = 0
        // Once a try may have reached the daemon, a later try that surely did
        // not (machine gone, network down) says nothing about that one: the
        // outcome stays unknown.
        var mayHaveRun = retryOfUnknown
        func notSent(_ code: String, _ text: String, _ later: String) -> CommandError {
            mayHaveRun
                ? CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行：请求可能已经送到，之后\(later)")
                : CommandError(code, text)
        }
        var req = PBForwardRequest()
        req.machineID = p.machineId
        // Authorize only after login. A prepared intent keeps plaintext locally;
        // no old-key ciphertext is sent while authority freshness is unknown.
        while true {
            do {
                if token.isEmpty { token = try await relogin(mayHaveRun) }
                let stateHash = try await authorizationStateHash()
                req.command = try await p.authorizationTask(stateHash: stateHash) {
                    verifyObservation in
                    Task {
                        try await self.authorizePrepared(p, verifyObservation: verifyObservation)
                    }
                }.value
                try Task.checkCancellation()
                mayHaveRun = true
                let res = try await api.client.forward(
                    request: req, headers: Api.headers(token: token)
                ).value()
                let msg: PBCommandResult
                do {
                    (_, msg) = try await openSecureMessage(
                        envelope: res.result,
                        want: Place(
                            .commandResult, machineId: p.machineId, sessionId: p.sessionId,
                            requestId: p.requestId),
                        as: PBCommandResult.self)
                } catch {
                    // A result that does not open says nothing about whether
                    // the command ran; retrying the same request asks again.
                    throw CommandError(CommandError.unknownOutcome, "电脑的回复打不开，不确定指令有没有执行")
                }
                if !msg.errorCode.isEmpty {
                    throw CommandError(
                        msg.errorCode, msg.errorMessage.isEmpty ? msg.errorCode : msg.errorMessage,
                        fromDaemon: true)
                }
                return msg
            } catch let ce as ConnectError {
                if neverSent(ce.exception) { throw notSent("network", "网络不通，指令没有发出", "网络断了") }
                if ce.code == .unauthenticated && attempt < 3 {
                    attempt += 1
                    token = try await relogin(mayHaveRun)
                    continue
                }
                // aborted: the daemon could not seal a result, as on the web.
                let unknown = [
                    Code.deadlineExceeded, .unknown, .internalError, .canceled, .aborted,
                ].contains(ce.code)
                if unknown { mayHaveRun = true }
                if unknown && attempt < 3 && clock() + 2000 < p.deadlineMs {
                    attempt += 1
                    try await sleep(1000 * Int64(attempt))
                    continue
                }
                if ce.code == .unavailable && ce.exception == nil {
                    throw notSent(CommandError.unavailable, "电脑不在线，指令没有发出", "电脑离线了")
                }
                if unknown || ce.code == .unavailable {
                    throw CommandError(CommandError.unknownOutcome, "不确定电脑有没有执行：\(errorText(ce))")
                }
                throw CommandError(CommandError.unknownOutcome, "中继未提供可验证的执行结果，请重试同一请求")
            } catch {
                if let answer = error as? CommandError, answer.fromDaemon { throw answer }
                if mayHaveRun {
                    throw CommandError(CommandError.unknownOutcome, "未收到可验证的执行结果，请重试同一请求")
                }
                throw error
            }
        }
    }

    /// Logs in again during a forward. When an earlier try may already have
    /// run the command, a failure here is no answer about it: it stays unknown.
    func relogin(_ mayHaveRun: Bool) async throws -> String {
        do {
            return try await signIn()
        } catch {
            if !mayHaveRun { throw error }
            throw CommandError(
                CommandError.unknownOutcome, "不确定电脑有没有执行：请求可能已经送到，之后登录失败（\(errorText(error))）")
        }
    }

    /// The models a machine's CLI offers, refreshed when a picker opens.
    public func listModels(_ machineId: String, agent: PBAgent = .unspecified, fresh: Bool = false)
        async throws -> PBListModelsResponse
    {
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
                    throw CommandError("internal", "意外的回复")
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
        if token.isEmpty { token = try await signIn() }
        return token
    }

    // --- images ---

    /// An image's display copy, from the cache of envelopes or the server.
    /// Only the envelope is cached; it is opened and checked in memory each
    /// time: bound to this machine, session and image, and data that starts
    /// with its format's signature.
    public func image(_ sid: String, imageId: String) async throws -> ImageResult {
        guard let s = sessions[sid], sessionKeys[sid] != nil else {
            throw CommandError("not_found", "未知会话")
        }
        let cacheKey = "\(sid)/\(imageId)"
        var env = await cache?.image(key: cacheKey).flatMap { try? PBEnvelope(serializedBytes: $0) }
        if env == nil {
            var ask = PBGetImageRequest()
            ask.sessionID = sid
            ask.imageID = imageId
            let r: PBGetImageResponse
            do {
                let tok = try await currentToken()
                r = try await api.client.getImage(request: ask, headers: Api.headers(token: tok))
                    .value()
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
                want: Place(.image, machineId: s.machineId, sessionId: sid, requestId: imageId),
                as: PBImagePayload.self)
        } catch {
            return .corrupt(why: "图片的内容或来源无法核实")
        }
        if detectFormat(payload.data) != payload.format { return .corrupt(why: "图片内容和格式对不上") }
        if let env, let bytes: Data = try? env.serializedData() {
            await cache?.putImage(key: cacheKey, envelope: bytes)
        }
        if let env, try PBHeader(serializedBytes: env.header).version == 1 {
            return .legacy(payload)
        }
        return .loaded(payload)
    }
}

extension Duration {
    var milliseconds: Int64 {
        components.seconds * 1000 + components.attoseconds / 1_000_000_000_000_000
    }
}
