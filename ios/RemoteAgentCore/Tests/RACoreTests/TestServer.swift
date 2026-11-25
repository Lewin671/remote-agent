import Foundation
import RAProto
import Testing
import os

@testable import RACore

/// The real Go server, built once per test run from this repository.
enum GoServer {
    /// Where `go` is, nil when the toolchain is not installed: the tests that need the server are skipped then.
    static let go: String? = {
        // RA_TEST_NO_GO=1 runs the suite as a machine without Go would.
        if ProcessInfo.processInfo.environment["RA_TEST_NO_GO"] == "1" { return nil }
        let path = ProcessInfo.processInfo.environment["PATH"] ?? ""
        for dir in path.split(separator: ":").map(String.init) + [
            "/opt/homebrew/bin", "/usr/local/go/bin", "/usr/local/bin",
        ] {
            let candidate = dir + "/go"
            if FileManager.default.isExecutableFile(atPath: candidate) { return candidate }
        }
        return nil
    }()

    static var available: Bool { go != nil }
    static let missing: Comment =
        "the Go toolchain is not installed: tests against the real server are skipped"

    /// The built binary, or why the build failed. A failed build fails the tests; it does not skip them.
    static let binary: Result<URL, TestFailure> = {
        guard let go else { return .failure(TestFailure("go is not installed")) }
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(
            "ra-server-bin-\(getpid())")
        do {
            try? FileManager.default.removeItem(at: dir)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            // The binary is this run's own: it goes when the run ends.
            builtIn.withLock { $0 = dir.path }
            atexit {
                if let path = builtIn.withLock({ $0 }) {
                    try? FileManager.default.removeItem(atPath: path)
                }
            }
            let out = dir.appendingPathComponent("relaytestserver")
            let p = Process()
            p.executableURL = URL(fileURLWithPath: go)
            p.arguments = ["build", "-o", dir.path + "/",
                "./internal/testutil/relaytestserver", "./internal/testutil/securityfixture"]
            p.currentDirectoryURL = repoRoot
            let log = Pipe()
            p.standardOutput = log
            p.standardError = log
            let exited = DispatchSemaphore(value: 0)
            p.terminationHandler = { _ in exited.signal() }
            try p.run()
            let output = log.fileHandleForReading.readDataToEndOfFile()
            exited.wait()
            guard p.terminationStatus == 0 else {
                return .failure(
                    TestFailure(
                        "go build failed: " + String(decoding: output.suffix(2000), as: UTF8.self)))
            }
            return .success(out)
        } catch {
            return .failure(TestFailure("go build could not run: \(error)"))
        }
    }()
}

private let builtIn = OSAllocatedUnfairLock<String?>(initialState: nil)

struct TestFailure: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}

/// A port nothing listens on now.
func freePort() -> Int {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    addr.sin_port = 0
    _ = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    var len = socklen_t(MemoryLayout<sockaddr_in>.size)
    _ = withUnsafeMutablePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &len) }
    }
    return Int(UInt16(bigEndian: addr.sin_port))
}

/// A remote-agent-server process on a free local port.
final class TestServer: @unchecked Sendable {
    private(set) var port = 0
    var url: String { "http://127.0.0.1:\(port)" }
    let setupToken = "setup-token-for-tests"
    let directory: URL
    private let signup: String
    private let lock = NSLock()
    private var proc: Process?
    private var exited: DispatchSemaphore?

    init(signup: String = "token") async throws {
        self.signup = signup
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(
            "ra-server-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        do { try await start() }
        catch { close(); throw error }
    }

    func start() async throws {
        let bin = try GoServer.binary.get()
        let p = Process()
        p.executableURL = bin
        p.arguments = [
            "-listen", "127.0.0.1:\(port)", "-db", directory.appendingPathComponent("ra.db").path,
            "-signup", signup,
        ]
        var env = ProcessInfo.processInfo.environment
        env["RA_SETUP_TOKEN"] = setupToken
        p.environment = env
        let logURL = directory.appendingPathComponent("server.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let log = try FileHandle(forWritingTo: logURL)
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        let exited = DispatchSemaphore(value: 0)
        p.terminationHandler = { _ in exited.signal() }
        lock.withLock {
            self.proc = p
            self.exited = exited
        }
        let boundPort: Int = try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Int, Error>) in
                let settled = OSAllocatedUnfairLock(initialState: false)
                let finish: @Sendable (Result<Int, Error>) -> Void = { result in
                    let first = settled.withLock { done -> Bool in
                        if done { return false }
                        done = true
                        return true
                    }
                    if first { cont.resume(with: result) }
                }
                do {
                    try p.run()
                    try? pipe.fileHandleForWriting.close()
                } catch {
                    try? log.close()
                    finish(.failure(error))
                    return
                }
                Thread.detachNewThread {
                    let reader = pipe.fileHandleForReading
                    var buffer = Data()
                    var captured = ""
                    while true {
                        let chunk = reader.availableData
                        if chunk.isEmpty { break }
                        try? log.write(contentsOf: chunk)
                        buffer.append(chunk)
                        if !settled.withLock({ $0 }) {
                            captured = String(decoding: buffer, as: UTF8.self)
                            if let ready = captured.components(separatedBy: "\n").first(where: { $0.hasPrefix("TEST_SERVER_URL=") }),
                               let url = URL(string: String(ready.dropFirst("TEST_SERVER_URL=".count)).trimmingCharacters(in: .whitespacesAndNewlines)),
                               let bound = url.port, bound > 0 {
                                finish(.success(bound))
                            }
                        }
                    }
                    try? log.close()
                    finish(.failure(TestFailure("server exited: " + String(captured.prefix(500)))))
                }
            }
        } onCancel: {
            p.terminate()
        }
        port = boundPort
    }

    func stop() {
        let (p, exited) = lock.withLock { () -> (Process?, DispatchSemaphore?) in
            defer { proc = nil; exited = nil }
            return (proc, exited)
        }
        guard let p else { return }
        p.terminate()
        if let exited {
            if exited.wait(timeout: .now() + 5) == .timedOut, p.isRunning {
                kill(p.processIdentifier, SIGKILL)
                exited.wait()
            }
        }
    }

    func close() {
        stop()
        TestSecurity.remove(url)
        try? FileManager.default.removeItem(at: directory)
    }
}

/// A thread-safe queue a test awaits deterministically via continuations.
final class Mailbox<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [T] = []
    private var waiters: [(id: UUID, cont: CheckedContinuation<T, Error>)] = []

    func put(_ item: T) {
        let waiter: CheckedContinuation<T, Error>? = lock.withLock {
            if !waiters.isEmpty { return waiters.removeFirst().cont }
            items.append(item)
            return nil
        }
        waiter?.resume(returning: item)
    }

    var all: [T] { lock.withLock { items } }

    func take() async throws -> T {
        try Task.checkCancellation()
        let id = UUID()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<T, Error>) in
                var item: T?
                var cancelNow = false
                lock.withLock {
                    if !items.isEmpty { item = items.removeFirst() }
                    else if Task.isCancelled { cancelNow = true }
                    else { waiters.append((id, c)) }
                }
                if let item { c.resume(returning: item) }
                else if cancelNow { c.resume(throwing: CancellationError()) }
            }
        } onCancel: {
            let waiter: CheckedContinuation<T, Error>? = lock.withLock {
                guard let i = waiters.firstIndex(where: { $0.id == id }) else { return nil }
                return waiters.remove(at: i).cont
            }
            waiter?.resume(throwing: CancellationError())
        }
    }
}

/// A stand-in for the daemon: it uploads a machine, sessions and events the
/// way internal/relay does, answers forwarded commands, and serves terminals.
actor FakeDaemon {
    nonisolated let account: Account
    nonisolated let id: String
    private let http: HTTP
    private var security: TestSecurity { get throws { try TestSecurity.get(account.server) } }
    private let api: PBMachineServiceClient
    private let rpc: Api
    private var token = ""
    private var ready = false
    private var readyFailure: Error?
    private var readyWaiters: [CheckedContinuation<Void, Error>] = []
    private var ws: Socket?
    private var metaRev: UInt64 = 0
    private(set) var sessionKeys: [String: Data] = [:]
    private var sessionRevs: [String: UInt64] = [:]
    private var lastSeq: [String: UInt64] = [:]
    private var info: [String: PBSession] = [:]
    private var history: [String: [PBEvent]] = [:]
    nonisolated let commands = Mailbox<(PBHeader, PBCommand)>()
    nonisolated let commandPayloads = Mailbox<(PBHeader, Data)>()
    nonisolated let termInput = Mailbox<PBTerminalFrame>()

    /// Seal results with a wrong key, as a corrupted relay would deliver them.
    private var garbleResults = false
    func setGarbleResults(_ on: Bool) { garbleResults = on }

    /// Send the last terminal frame a second time under the same number, as a replaying relay would.
    private var replayTerminal = false
    func setReplayTerminal(_ on: Bool) { replayTerminal = on }

    /// What to answer; the default is an empty success of the right kind.
    private var answer: @Sendable (PBHeader, PBCommand) async -> PBCommandResult = { _, c in
        FakeDaemon.defaultResult(c)
    }
    func setAnswer(_ answer: @escaping @Sendable (PBHeader, PBCommand) async -> PBCommandResult) {
        self.answer = answer
    }

    init(account: Account, http: HTTP) throws {
        self.account = account
        self.http = http
        id = account.deviceId
        rpc = try Api(server: account.server, http: http)
        api = PBMachineServiceClient(client: rpc.protocolClient)
    }

    static func join(phone: SyntheticPhone, http: HTTP) async throws -> FakeDaemon {
        let (acct, _) = try await phone.enroll(seed: randomBytes(32), kind: .machine)
        let d = try FakeDaemon(account: acct, http: http)
        try await d.login()
        return d
    }

    func login() async throws {
        let store = try security.store(account.deviceId) ?? { throw TestFailure("missing daemon test store") }()
        token = try await RACore.loginWithStore(api: rpc, deviceId: account.deviceId, store: store).token
    }

    private func header(_ kind: PBPayloadKind) -> PBHeader {
        var h = PBHeader()
        h.kind = kind
        h.machineID = id
        h.sender = id
        return h
    }

    /// `fileUpload` is what the meta advertises of sending files; nil leaves capabilities out.
    func putMachine(_ hostname: String, protocol version: UInt32 = peerProtocolVersion, fileUpload: Bool? = nil) async throws {
        metaRev += 1
        var meta = PBMachineMeta()
        meta.hostname = hostname
        meta.os = "darwin"
        meta.home = "/Users/me"
        meta.protocolVersion = version
        if let fileUpload { meta.capabilities.fileUpload = fileUpload }
        var h = header(.machineMeta)
        h.seq = metaRev
        var req = PBPutMachineRequest()
        req.meta = try security.seal(account, h, meta)
        _ = try await api.putMachine(request: req, headers: Api.headers(token: token)).value()
    }

    /// `endedMs` is what the daemon tells the server in the clear to page the listing by: 0 while the session has not ended.
    func putSession(_ sid: String, _ info: PBSession, endedMs: Int64 = 0) async throws {
        let key = sessionKeys[sid] ?? randomBytes(32)
        sessionKeys[sid] = key
        let rev = (sessionRevs[sid] ?? 0) + 1
        sessionRevs[sid] = rev
        var kh = header(.sessionKey)
        kh.sessionID = sid
        kh.seq = 1
        var mh = header(.sessionMeta)
        mh.sessionID = sid
        mh.seq = rev
        var named = info
        named.id = sid
        self.info[sid] = named
        var req = PBPutSessionRequest()
        req.sessionID = sid
        req.key = try security.seal(account, kh, key)
        req.meta = try security.seal(account, mh, named)
        req.endedMs = endedMs
        req.taskOwned = info.taskOwned
        _ = try await api.putSession(request: req, headers: Api.headers(token: token)).value()
    }

    /// Stores an image's display copy for a session, as the daemon does before the event that refers to it.
    func putImage(_ sid: String, _ imageId: String, _ payload: PBImagePayload) async throws {
        var h = header(.image)
        h.sessionID = sid
        h.requestID = imageId
        var req = PBPutImageRequest()
        req.sessionID = sid
        req.imageID = imageId
        req.image = try security.seal(account, h, payload, seed: sessionKeys[sid]!)
        _ = try await api.putImage(request: req, headers: Api.headers(token: token)).value()
    }

    /// Appends events to a session, numbering them.
    func upload(_ sid: String, _ events: PBEvent...) async throws {
        try await uploadBatch(sid, events)
    }

    func uploadBatch(_ sid: String, _ events: [PBEvent]) async throws {
        let key = sessionKeys[sid]!
        var req = PBUploadEventsRequest()
        req.sessionID = sid
        for var ev in events {
            let seq = (lastSeq[sid] ?? 0) + 1
            lastSeq[sid] = seq
            ev.seq = seq
            ev.sessionID = sid
            ev.time.seconds = Int64(Date().timeIntervalSince1970)
            var h = header(.event)
            h.sessionID = sid
            h.seq = seq
            var stored = PBStoredEvent()
            stored.seq = seq
            history[sid, default: []].append(ev)
            stored.envelope = try security.seal(account, h, ev, seed: key)
            req.events.append(stored)
        }
        _ = try await api.uploadEvents(request: req, headers: Api.headers(token: token)).value()
    }

    /// Connects the stream and answers forwarded commands.
    func waitReady() async throws {
        if ready { return }
        if let err = readyFailure { throw err }
        try await withCheckedThrowingContinuation { readyWaiters.append($0) }
    }
    private func signalReady() {
        ready = true
        let waiters = readyWaiters
        readyWaiters.removeAll()
        for w in waiters { w.resume() }
    }
    private func failReady(_ error: Error) {
        guard !ready else { return }
        readyFailure = error
        let waiters = readyWaiters
        readyWaiters.removeAll()
        for w in waiters { w.resume(throwing: error) }
    }
    func connect() throws {
        ready = false
        readyFailure = nil
        var hello = PBPeerFrame()
        hello.hello.token = token
        let socket = Socket(
            http: http, url: rpc.socketURL("/v1/stream"), hello: try hello.serializedData(),
            sleep: { _ in try await Task.sleep(for: .seconds(3600)) })
        ws = socket
        Task {
            while let bytes = try? await socket.receive() {
                guard let f = try? PBServerFrame(serializedBytes: bytes) else { continue }
                if case .ready? = f.kind {
                    self.signalReady()
                    continue
                }
                guard case .forwardCall(let call)? = f.kind else { continue }
                await self.handle(call, on: socket)
            }
            let end = await socket.ended()
            self.failReady(TestFailure("synthetic authority stream ended before ready (code=\(String(describing: end.closeCode)), error=\(String(describing: end.error)))"))
        }
    }

    private func handle(_ call: PBForwardCall, on socket: Socket) async {
        guard let (h, pt) = try? security.open(account, call.command),
            let cmd = try? PBCommand(serializedBytes: pt)
        else { return }
        var result: PBCommandResult
        if case .getLiveSnapshot(let ask)? = cmd.kind {
            var snapshot = PBLiveSnapshot()
            snapshot.session = info[ask.sessionID] ?? PBSession()
            snapshot.session.id = ask.sessionID
            var r = PBCommandResult()
            r.liveSnapshot = snapshot
            result = r
        } else {
            commands.put((h, cmd))
            commandPayloads.put((h,pt))
            if case .openTerminal(let open)? = cmd.kind { serveTerminal(open) }
            result = await answer(h, cmd)
        }
        guard let rec = try? security.record(member: account.deviceId) else { return }
        let binding = testRequestIdentity(account: rec.account, domain: rec.domain, header: h, plaintext: pt)
        result.requestKeyHash=binding.key; result.payloadHash=binding.payload
        if result.outcome.isEmpty { result.outcome="executed"; result.reason="completed"; result.resultAvailable=true }
        var rh = header(.commandResult)
        rh.sessionID = h.sessionID
        rh.requestID = h.requestID
        var reply = PBPeerFrame()
        reply.forwardReply.callID = call.callID
        reply.forwardReply.attemptID=call.attemptID
        reply.forwardReply.outcome=result.outcome
        reply.forwardReply.reason=result.reason
        guard var env = try? security.seal(account, rh, result) else { return }
        if garbleResults { env.ciphertext = randomBytes(env.ciphertext.count) }
        reply.forwardReply.result = env
        if let bytes = try? reply.serializedData() { socket.send(bytes) }
    }

    func disconnect() {
        failReady(TestFailure("synthetic authority stream disconnected"))
        ready = false
        ws?.close()
        ws = nil
    }

    private func serveTerminal(_ open: PBOpenTerminalRequest) {
        let sid = open.sessionID
        let stream = open.streamID
        guard let key = sessionKeys[sid] else { return }
        var hello = PBTermHello()
        hello.token = token
        hello.streamID = stream
        guard let helloBytes: Data = try? hello.serializedData() else { return }
        let socket = Socket(
            http: http, url: rpc.socketURL("/v1/term"), hello: helloBytes, sleep: { _ in try await Task.sleep(for: .seconds(3600)) })
        let out = header(.terminalOutput)
        let input = termInput
        let replay = replayTerminal
        Task {
            guard await socket.opened() else { return }
            var snap = PBTerminalFrame()
            snap.snapshot.cols = 120
            snap.snapshot.rows = 40
            snap.snapshot.data = Data("\u{1b}[Hhello".utf8)
            var more = PBTerminalFrame()
            more.output = Data(" world".utf8)
            var conflict = PBTerminalFrame()
            conflict.output = Data(" tampered".utf8)
            var seq: UInt64 = 0
            // Phase 3: identical duplicate seq=2 is ignored without closing; a conflicting payload
            // at the same seq=2 still terminates the stream with errorsTerminalFramesAreOutOfOrder.
            for (i, f) in ([snap, more] + (replay ? [more, conflict] : [])).enumerated() {
                seq = UInt64(min(i + 1, 2))
                var h = out
                h.sessionID = sid
                h.requestID = stream
                h.seq = seq
                if let bytes: Data = try? security.seal(account, h, f, seed: key).serializedData() {
                    socket.send(bytes)
                }
            }
            while let bytes = try? await socket.receive() {
                guard let env = try? PBEnvelope(serializedBytes: bytes),
                    let (hh, raw) = try? security.open(account, env, seed: key),
                    let frame = try? PBTerminalFrame(serializedBytes: raw), hh.seq > 0,
                    hh.kind == .terminalInput, hh.sessionID == sid, hh.requestID == stream
                else { continue }
                input.put(frame)
            }
        }
    }

    static func defaultResult(_ c: PBCommand) -> PBCommandResult {
        var r = PBCommandResult()
        switch c.kind {
        case .send?: r.send.queuePosition = 1
        case .openTerminal?: r.openTerminal = PBOpenTerminalResponse()
        case .interrupt?: r.interrupt.interrupted = true
        default:
            r.errorCode = "unimplemented"
            r.errorMessage = "fake daemon"
        }
        return r
    }
}

/// A CipherCache in memory, as the stored one behaves.
final class MemoryCache: CipherCache, @unchecked Sendable {
    private let lock = NSLock()
    private var imageStore: [String: Data] = [:]
    private var machineStore: [String: PBMachine] = [:]
    private var sessionStore: [String: PBRemoteSession] = [:]
    private var eventStore: [String: [UInt64: PBStoredEvent]] = [:]
    private var blobs: [String: Data] = [:]

    var images: [String: Data] { lock.withLock { imageStore } }
    func image(key: String) async -> Data? { lock.withLock { imageStore[key] } }
    func putImage(key: String, envelope: Data) async {
        lock.withLock { imageStore[key] = envelope }
    }
    func blob(name: String) async -> Data? { lock.withLock { blobs[name] } }
    func putBlob(name: String, data: Data) async { lock.withLock { blobs[name] = data } }
    func removeBlob(name: String) async { lock.withLock { blobs[name] = nil } }

    func machines() async -> [PBMachine] { lock.withLock { Array(machineStore.values) } }
    func sessions() async -> [PBRemoteSession] { lock.withLock { Array(sessionStore.values) } }
    func events(sessionId: String) async -> [PBStoredEvent] {
        lock.withLock { (eventStore[sessionId] ?? [:]).values.sorted { $0.seq < $1.seq } }
    }
    func eventPage(sessionId: String, afterSeq: UInt64) async -> [PBStoredEvent] {
        lock.withLock {
            Array((eventStore[sessionId] ?? [:]).values.filter { $0.seq > afterSeq }
                .sorted { $0.seq < $1.seq }.prefix(cipherEventPageLimit))
        }
    }
    func newestEvents(sessionId: String) async -> [PBStoredEvent] {
        lock.withLock {
            Array((eventStore[sessionId] ?? [:]).values.sorted { $0.seq < $1.seq }.suffix(cipherEventPageLimit))
        }
    }
    func putMachine(_ m: PBMachine) async { lock.withLock { machineStore[m.id] = m } }
    func putSession(_ s: PBRemoteSession) async { lock.withLock { sessionStore[s.sessionID] = s } }
    func putEvents(sessionId: String, events: [PBStoredEvent]) async {
        // An event already stored is left as it is, as in the app's store.
        lock.withLock {
            for e in events where eventStore[sessionId]?[e.seq] == nil {
                eventStore[sessionId, default: [:]][e.seq] = e
            }
        }
    }
    func dropEvents(sessionId: String) async { lock.withLock { eventStore[sessionId] = nil } }

    /// Changes a stored event behind the cache's back, as a damaged row would be.
    func damage(sessionId: String, seq: UInt64, _ change: (inout PBStoredEvent) -> Void) {
        lock.withLock {
            if var e = eventStore[sessionId]?[seq] {
                change(&e)
                eventStore[sessionId]?[seq] = e
            }
        }
    }

    func removeMachine(id: String) async {
        lock.withLock {
            machineStore[id] = nil
            for s in sessionStore.values where s.machineID == id {
                sessionStore[s.sessionID] = nil
                eventStore[s.sessionID] = nil
            }
        }
    }

    func removeSession(id: String) async {
        lock.withLock {
            sessionStore[id] = nil
            eventStore[id] = nil
        }
    }

    func unlistSession(id: String) async { lock.withLock { sessionStore[id] = nil } }
}

/// A cache that passes everything on to `inner`, except what a test hooks.
final class HookedCache: CipherCache, @unchecked Sendable {
    let inner: MemoryCache
    private let lock = NSLock()
    private var _machines: (@Sendable () async -> Void)?
    private var _events: (@Sendable (String) async -> [PBStoredEvent])?
    private var _eventPage: (@Sendable (String, UInt64) async -> Void)?
    private var _putEvents: (@Sendable (String, [PBStoredEvent]) async -> Void)?
    private var _blob: (@Sendable (String, Data?) async -> Data?)?
    private var _putBlob: (@Sendable (String, Data) async -> Void)?
    private var _putMachine: (@Sendable (PBMachine) async -> Void)?

    init(_ inner: MemoryCache = MemoryCache()) { self.inner = inner }

    /// Runs before the machines are read.
    func hookMachines(_ f: (@Sendable () async -> Void)?) { lock.withLock { _machines = f } }
    func hookEvents(_ f: (@Sendable (String) async -> [PBStoredEvent])?) {
        lock.withLock { _events = f }
    }
    /// Runs before a page of older events is read (not the newest window).
    func hookEventPage(_ f: (@Sendable (String, UInt64) async -> Void)?) {
        lock.withLock { _eventPage = f }
    }
    func hookPutEvents(_ f: (@Sendable (String, [PBStoredEvent]) async -> Void)?) {
        lock.withLock { _putEvents = f }
    }
    func hookBlob(_ f: (@Sendable (String, Data?) async -> Data?)?) { lock.withLock { _blob = f } }
    func hookPutBlob(_ f: (@Sendable (String, Data) async -> Void)?) { lock.withLock { _putBlob = f } }
    func hookPutMachine(_ f: (@Sendable (PBMachine) async -> Void)?) {
        lock.withLock { _putMachine = f }
    }

    func machines() async -> [PBMachine] {
        if let f = lock.withLock({ _machines }) { await f() }
        return await inner.machines()
    }
    func sessions() async -> [PBRemoteSession] { await inner.sessions() }
    func events(sessionId: String) async -> [PBStoredEvent] {
        if let f = lock.withLock({ _events }) { return await f(sessionId) }
        return await inner.events(sessionId: sessionId)
    }
    func eventPage(sessionId: String, afterSeq: UInt64) async -> [PBStoredEvent] {
        if let f = lock.withLock({ _eventPage }) { await f(sessionId, afterSeq) }
        if let f = lock.withLock({ _events }) {
            return Array(await f(sessionId).filter { $0.seq > afterSeq }
                .sorted { $0.seq < $1.seq }.prefix(cipherEventPageLimit))
        }
        return await inner.eventPage(sessionId: sessionId, afterSeq: afterSeq)
    }
    func newestEvents(sessionId: String) async -> [PBStoredEvent] {
        if let f = lock.withLock({ _events }) {
            return Array(await f(sessionId).sorted { $0.seq < $1.seq }.suffix(cipherEventPageLimit))
        }
        return await inner.newestEvents(sessionId: sessionId)
    }
    func putMachine(_ m: PBMachine) async {
        if let f = lock.withLock({ _putMachine }) { await f(m) }
        await inner.putMachine(m)
    }
    func putSession(_ s: PBRemoteSession) async { await inner.putSession(s) }
    func putEvents(sessionId: String, events: [PBStoredEvent]) async {
        if let f = lock.withLock({ _putEvents }) { return await f(sessionId, events) }
        await inner.putEvents(sessionId: sessionId, events: events)
    }
    func dropEvents(sessionId: String) async { await inner.dropEvents(sessionId: sessionId) }
    func removeMachine(id: String) async { await inner.removeMachine(id: id) }
    func removeSession(id: String) async { await inner.removeSession(id: id) }
    func unlistSession(id: String) async { await inner.unlistSession(id: id) }
    func image(key: String) async -> Data? { await inner.image(key: key) }
    func putImage(key: String, envelope: Data) async {
        await inner.putImage(key: key, envelope: envelope)
    }
    func blob(name: String) async -> Data? {
        let value = await inner.blob(name: name)
        if let f = lock.withLock({ _blob }) { return await f(name, value) }
        return value
    }
    func putBlob(name: String, data: Data) async {
        if let f = lock.withLock({ _putBlob }) { await f(name, data) }
        await inner.putBlob(name: name, data: data)
    }
    func removeBlob(name: String) async { await inner.removeBlob(name: name) }
}

/// Something a test waits for, set once.
final class Signal: Sendable {
    private struct State {
        var set = false
        var waiters: [Int: CheckedContinuation<Bool, Never>] = [:]
        var nextID = 0
    }
    private let state = OSAllocatedUnfairLock(initialState: State())

    func set() {
        let waiters = state.withLock { s -> [CheckedContinuation<Bool, Never>] in
            s.set = true
            let w = Array(s.waiters.values)
            s.waiters.removeAll()
            return w
        }
        for c in waiters { c.resume(returning: true) }
    }

    var isSet: Bool { state.withLock { $0.set } }

    /// Waits for the notification without a wall-clock deadline; the per-test hang guard catches stalls.
    @discardableResult
    func wait() async -> Bool {
        if isSet { return true }
        let id = state.withLock { s -> Int in
            s.nextID += 1
            return s.nextID
        }
        return await withTaskCancellationHandler {
            await withCheckedContinuation { cont in
                let alreadySet = state.withLock { s -> Bool in
                    if s.set || Task.isCancelled { return true }
                    s.waiters[id] = cont
                    return false
                }
                if alreadySet {
                    cont.resume(returning: isSet)
                }
            }
        } onCancel: {
            let cont = state.withLock { $0.waiters.removeValue(forKey: id) }
            cont?.resume(returning: false)
        }
    }
}

final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    @discardableResult
    func increment() -> Int {
        lock.withLock {
            n += 1
            return n
        }
    }
    var value: Int { lock.withLock { n } }
}
