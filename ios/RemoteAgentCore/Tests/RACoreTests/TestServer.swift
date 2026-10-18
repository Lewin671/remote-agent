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
            let out = dir.appendingPathComponent("remote-agent-server")
            let p = Process()
            p.executableURL = URL(fileURLWithPath: go)
            p.arguments = ["build", "-o", out.path, "./cmd/remote-agent-server"]
            p.currentDirectoryURL = repoRoot
            let log = Pipe()
            p.standardOutput = log
            p.standardError = log
            try p.run()
            let output = log.fileHandleForReading.readDataToEndOfFile()
            while p.isRunning { usleep(10_000) }
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
    let port: Int
    let url: String
    let setupToken = "setup-token-for-tests"
    private let dbDir: URL
    private let lock = NSLock()
    private var proc: Process?

    init() async throws {
        port = freePort()
        url = "http://127.0.0.1:\(port)"
        dbDir = FileManager.default.temporaryDirectory.appendingPathComponent(
            "ra-server-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dbDir, withIntermediateDirectories: true)
        try await start()
    }

    func start() async throws {
        let bin = try GoServer.binary.get()
        let p = Process()
        p.executableURL = bin
        p.arguments = [
            "-listen", "127.0.0.1:\(port)", "-db", dbDir.appendingPathComponent("ra.db").path,
        ]
        var env = ProcessInfo.processInfo.environment
        env["RA_SETUP_TOKEN"] = setupToken
        p.environment = env
        let logURL = dbDir.appendingPathComponent("server.log")
        if !FileManager.default.fileExists(atPath: logURL.path) {
            FileManager.default.createFile(atPath: logURL.path, contents: nil)
        }
        let log = try FileHandle(forWritingTo: logURL)
        log.seekToEndOfFile()
        p.standardOutput = log
        p.standardError = log
        try p.run()
        lock.withLock { proc = p }
        // Ready means this process answers HTTP, not only that the port takes a connection.
        let session = URLSession(configuration: .ephemeral)
        defer { session.invalidateAndCancel() }
        let until = Date().addingTimeInterval(10)
        while Date() < until {
            guard p.isRunning else {
                let text = (try? String(contentsOf: logURL, encoding: .utf8)) ?? ""
                throw TestFailure("server exited: " + String(text.prefix(500)))
            }
            var req = URLRequest(url: URL(string: url + "/healthz")!)
            req.timeoutInterval = 0.5
            if (try? await session.data(for: req)) != nil { return }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        throw TestFailure("server did not start")
    }

    func stop() {
        let p = lock.withLock { () -> Process? in
            defer { proc = nil }
            return proc
        }
        guard let p else { return }
        p.terminate()
        // Not waitUntilExit: it waits on a run loop, which a test's thread does not turn.
        let until = Date().addingTimeInterval(5)
        while p.isRunning {
            if Date() >= until { kill(p.processIdentifier, SIGKILL) }
            usleep(10_000)
        }
    }

    func close() {
        stop()
        TestSecurity.remove(url)
        try? FileManager.default.removeItem(at: dbDir)
    }
}

/// A thread-safe list a test polls, as a LinkedBlockingQueue is on Android.
final class Mailbox<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [T] = []

    func put(_ item: T) { lock.withLock { items.append(item) } }
    var all: [T] { lock.withLock { items } }

    /// The next item, waiting up to `ms` for one.
    func poll(_ ms: Int) async -> T? {
        let until = Date().addingTimeInterval(Double(ms) / 1000)
        while true {
            let item = lock.withLock { items.isEmpty ? nil : items.removeFirst() }
            if let item { return item }
            if Date() >= until { return nil }
            try? await Task.sleep(nanoseconds: 5_000_000)
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
    private var ws: Socket?
    private var metaRev: UInt64 = 0
    private(set) var sessionKeys: [String: Data] = [:]
    private var sessionRevs: [String: UInt64] = [:]
    private var lastSeq: [String: UInt64] = [:]
    private var info: [String: PBSession] = [:]
    private var history: [String: [PBEvent]] = [:]
    nonisolated let commands = Mailbox<(PBHeader, PBCommand)>()
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
        token = try await RACore.login(api: rpc, account: account)
    }

    private func header(_ kind: PBPayloadKind) -> PBHeader {
        var h = PBHeader()
        h.kind = kind
        h.machineID = id
        h.sender = id
        return h
    }

    func putMachine(_ hostname: String, protocol version: UInt32 = protocolVersion) async throws {
        metaRev += 1
        var meta = PBMachineMeta()
        meta.hostname = hostname
        meta.os = "darwin"
        meta.home = "/Users/me"
        meta.protocolVersion = version
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
        let deadline = Date().addingTimeInterval(5)
        while !ready {
            if Date() > deadline { throw TestFailure("synthetic authority stream not ready") }
            try await Task.sleep(for: .milliseconds(10))
        }
    }
    func connect() throws {
        ready = false
        var hello = PBPeerFrame()
        hello.hello.token = token
        let socket = Socket(
            http: http, url: rpc.socketURL("/v1/stream"), hello: try hello.serializedData(),
            sleep: realSleep)
        ws = socket
        Task {
            while let bytes = try? await socket.receive() {
                guard let f = try? PBServerFrame(serializedBytes: bytes) else { continue }
                if case .ready? = f.kind {
                    self.ready = true
                    continue
                }
                guard case .forwardCall(let call)? = f.kind else { continue }
                await self.handle(call, on: socket)
            }
        }
    }

    private func handle(_ call: PBForwardCall, on socket: Socket) async {
        guard let (h, pt) = try? security.open(account, call.command),
            let cmd = try? PBCommand(serializedBytes: pt)
        else { return }
        let result: PBCommandResult
        if case .getLiveSnapshot(let ask)? = cmd.kind {
            let events = history[ask.sessionID] ?? []
            let derived = derive(events)
            let actions = events.filter { event in
                switch event.kind {
                case .approvalRequested(let request)?:
                    return derived.approvals[request.approvalID] != nil
                case .questionAsked(let request)?:
                    return derived.questions[request.questionID] != nil
                case .inputState(let state)?:
                    return derived.inputs[state.requestID] == .queued
                        || derived.inputs[state.requestID] == .heldByCli
                default: return false
                }
            }
            var snapshot = PBLiveSnapshot()
            snapshot.running = derived.running
            snapshot.session = info[ask.sessionID] ?? PBSession()
            snapshot.session.id = ask.sessionID
            snapshot.actions = actions
            var r = PBCommandResult()
            r.liveSnapshot = snapshot
            result = r
        } else {
            commands.put((h, cmd))
            if case .openTerminal(let open)? = cmd.kind { serveTerminal(open) }
            result = await answer(h, cmd)
        }
        var rh = header(.commandResult)
        rh.sessionID = h.sessionID
        rh.requestID = h.requestID
        var reply = PBPeerFrame()
        reply.forwardReply.callID = call.callID
        guard var env = try? security.seal(account, rh, result) else { return }
        if garbleResults { env.ciphertext = randomBytes(env.ciphertext.count) }
        reply.forwardReply.result = env
        if let bytes = try? reply.serializedData() { socket.send(bytes) }
    }

    func disconnect() {
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
            http: http, url: rpc.socketURL("/v1/term"), hello: helloBytes, sleep: realSleep)
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
            var seq: UInt64 = 0
            for (i, f) in ([snap, more] + (replay ? [more] : [])).enumerated() {
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
                    (try? Envelopes.expect(
                        hh, Place(.terminalInput, sessionId: sid, requestId: stream))) != nil
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

    func machines() async -> [PBMachine] { lock.withLock { Array(machineStore.values) } }
    func sessions() async -> [PBRemoteSession] { lock.withLock { Array(sessionStore.values) } }
    func events(sessionId: String) async -> [PBStoredEvent] {
        lock.withLock { (eventStore[sessionId] ?? [:]).values.sorted { $0.seq < $1.seq } }
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
    private var _putEvents: (@Sendable (String, [PBStoredEvent]) async -> Void)?
    private var _putMachine: (@Sendable (PBMachine) async -> Void)?

    init(_ inner: MemoryCache = MemoryCache()) { self.inner = inner }

    /// Runs before the machines are read.
    func hookMachines(_ f: (@Sendable () async -> Void)?) { lock.withLock { _machines = f } }
    func hookEvents(_ f: (@Sendable (String) async -> [PBStoredEvent])?) {
        lock.withLock { _events = f }
    }
    func hookPutEvents(_ f: (@Sendable (String, [PBStoredEvent]) async -> Void)?) {
        lock.withLock { _putEvents = f }
    }
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
    func blob(name: String) async -> Data? { await inner.blob(name: name) }
    func putBlob(name: String, data: Data) async { await inner.putBlob(name: name, data: data) }
}

/// Something a test waits for, set once.
final class Signal: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false

    func set() { lock.withLock { done = true } }
    var isSet: Bool { lock.withLock { done } }

    /// Waits until it is set; false when `ms` passed first.
    @discardableResult
    func wait(_ ms: Int = 10_000) async -> Bool {
        let until = Date().addingTimeInterval(Double(ms) / 1000)
        while !isSet {
            if Date() >= until { return false }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        return true
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

func sleepMs(_ ms: Int) async {
    try? await Task.sleep(nanoseconds: UInt64(ms) * 1_000_000)
}

/// Waits for `ok`, failing the test when it does not come in `ms`.
func eventually(_ what: String, ms: Int = 10_000, _ ok: @Sendable () async -> Bool) async throws {
    let until = Date().addingTimeInterval(Double(ms) / 1000)
    while !(await ok()) {
        if Date() >= until { throw TestFailure("timed out waiting for \(what)") }
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
}

extension Feed {
    /// The first value that satisfies `ok`; throws when none comes in `ms`.
    func await(_ what: String, ms: Int = 10_000, _ ok: @escaping @Sendable (Value) -> Bool)
        async throws -> Value
    {
        let found = Task { await self.first(where: ok) }
        let limit = Task {
            try? await Task.sleep(nanoseconds: UInt64(ms) * 1_000_000)
            found.cancel()
        }
        defer { limit.cancel() }
        if let v = await found.value { return v }
        throw TestFailure("timed out waiting for \(what)")
    }
}

extension Remote {
    func awaitState(
        _ what: String, ms: Int = 10_000, _ ok: @escaping @Sendable (RemoteState) -> Bool
    ) async throws -> RemoteState {
        do {
            return try await state.await(what, ms: ms, ok)
        } catch {
            // Lengths and IDs only: a state holds titles.
            let st = current
            throw TestFailure(
                "timed out waiting for \(what): \(st.sessions.count) sessions, next \(String(describing: st.sessionsNext)), \(st.conn), link \(st.link), removed \(st.removed), error \(st.error), sessionsError \(st.sessionsError)"
            )
        }
    }

    func awaitEvents(_ sid: String, _ n: Int, ms: Int = 10_000) async throws -> SessionEvents {
        try await events(sid).await("\(n) events of \(sid)", ms: ms) { $0.events.count >= n }
    }
}

extension SessionEvents {
    var seqs: [UInt64] { events.map(\.seq) }
}
