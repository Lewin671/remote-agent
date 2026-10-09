import CryptoKit
import Foundation
import RAProto

@testable import RACore

/// A TCP listener on a free loopback port, for fixtures that speak raw HTTP.
final class Listener: @unchecked Sendable {
    private let fd: Int32
    let port: Int
    private let lock = NSLock()
    private var open: Set<Int32> = []
    private var closed = false

    init() throws {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        self.fd = fd
        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &len) }
        }
        port = Int(UInt16(bigEndian: addr.sin_port))
        guard bound == 0, listen(fd, 16) == 0 else { throw TestFailure("could not listen") }
    }

    /// Hands every connection to `serve` on a thread of its own, until `close`.
    func serve(_ serve: @escaping @Sendable (Int32) -> Void) {
        Thread.detachNewThread { [self] in
            while true {
                let c = accept(fd, nil, nil)
                if c < 0 { return }
                var yes: Int32 = 1
                setsockopt(c, SOL_SOCKET, SO_NOSIGPIPE, &yes, socklen_t(MemoryLayout<Int32>.size))
                let late = lock.withLock { () -> Bool in
                    if closed { return true }
                    open.insert(c)
                    return false
                }
                if late {
                    Darwin.close(c)
                    return
                }
                Thread.detachNewThread { [self] in
                    serve(c)
                    let mine = lock.withLock { open.remove(c) != nil }
                    if mine { Darwin.close(c) }
                }
            }
        }
    }

    func close() {
        let conns = lock.withLock { () -> Set<Int32> in
            closed = true
            defer { open = [] }
            return open
        }
        Darwin.close(fd)
        for c in conns {
            shutdown(c, SHUT_RDWR)
            Darwin.close(c)
        }
    }
}

/// Reads up to the blank line that ends an HTTP request's head; nil when the connection ends first.
func readHead(_ fd: Int32) -> [String]? {
    var bytes = [UInt8]()
    var b: UInt8 = 0
    while !bytes.suffix(4).elementsEqual([13, 10, 13, 10]) {
        if read(fd, &b, 1) != 1 { return nil }
        bytes.append(b)
    }
    return String(decoding: bytes, as: UTF8.self).components(separatedBy: "\r\n").filter { !$0.isEmpty }
}

@discardableResult
func writeAll(_ fd: Int32, _ data: Data) -> Bool {
    var sent = 0
    let bytes = [UInt8](data)
    while sent < bytes.count {
        let n = bytes[sent...].withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
        if n <= 0 { return false }
        sent += n
    }
    return true
}

/// Answers a WebSocket upgrade; false when the request was not one.
func acceptUpgrade(_ fd: Int32, _ head: [String]) -> Bool {
    guard let line = head.first(where: { $0.lowercased().hasPrefix("sec-websocket-key:") }) else { return false }
    let key = line.split(separator: ":", maxSplits: 1)[1].trimmingCharacters(in: .whitespaces)
    let accept = Data(Insecure.SHA1.hash(data: Data((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").utf8))).base64EncodedString()
    return writeAll(fd, Data("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: \(accept)\r\n\r\n".utf8))
}

/// Local stream fixture: the first upgraded socket never sends Ready, or is
/// closed with `firstClose` as its close code; the ones after it get Ready.
final class StreamBlackhole: @unchecked Sendable {
    private let listener: Listener
    let url: String
    let attempts = Counter()

    init(firstClose: Int? = nil) throws {
        listener = try Listener()
        url = "http://127.0.0.1:\(listener.port)"
        let attempts = self.attempts
        listener.serve { fd in
            guard let head = readHead(fd), acceptUpgrade(fd, head) else { return }
            if attempts.increment() > 1 {
                var ready = PBServerFrame()
                ready.ready = PBReady()
                let bytes: Data = (try? ready.serializedData()) ?? Data()
                writeAll(fd, Data([0x82, UInt8(bytes.count)]) + bytes)
            } else if let code = firstClose {
                // A close frame: its payload is the code.
                writeAll(fd, Data([0x88, 2, UInt8(code >> 8), UInt8(code & 0xff)]))
            }
            // No application frames on the first connection. Its closure
            // is observed independently of the client's lifecycle state.
            var b: UInt8 = 0
            while read(fd, &b, 1) == 1 {}
        }
    }

    func close() { listener.close() }
}

/// Stands in front of a server and passes every byte along, counting the
/// connections that ask for a path: URLSession hands its WebSocket upgrades
/// to no URLProtocol, so this is where a test sees them.
final class CountingProxy: @unchecked Sendable {
    private let listener: Listener
    let url: String
    let streams = Counter()

    init(to port: Int, counting path: String = "/v1/stream") throws {
        listener = try Listener()
        url = "http://127.0.0.1:\(listener.port)"
        let streams = self.streams
        listener.serve { fd in
            let up = socket(AF_INET, SOCK_STREAM, 0)
            defer { Darwin.close(up) }
            var yes: Int32 = 1
            setsockopt(up, SOL_SOCKET, SO_NOSIGPIPE, &yes, socklen_t(MemoryLayout<Int32>.size))
            var addr = sockaddr_in()
            addr.sin_family = sa_family_t(AF_INET)
            addr.sin_addr.s_addr = inet_addr("127.0.0.1")
            addr.sin_port = UInt16(port).bigEndian
            let ok = withUnsafePointer(to: &addr) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(up, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
            }
            guard ok == 0 else { return }
            // Every connection starts with a request line; an upgrade is the only request on its connection.
            var first = [UInt8](repeating: 0, count: 4096)
            let n = read(fd, &first, first.count)
            guard n > 0 else { return }
            if String(decoding: first[..<n], as: UTF8.self).hasPrefix("GET \(path) ") { streams.increment() }
            guard writeAll(up, Data(first[..<n])) else { return }
            Thread.detachNewThread {
                pump(from: up, to: fd)
                shutdown(fd, SHUT_RDWR)
            }
            pump(from: fd, to: up)
            shutdown(up, SHUT_RDWR)
        }
    }

    func close() { listener.close() }
}

private func pump(from: Int32, to: Int32) {
    var buf = [UInt8](repeating: 0, count: 16384)
    while true {
        let n = read(from, &buf, buf.count)
        if n <= 0 { return }
        if !writeAll(to, Data(buf[..<n])) { return }
    }
}

/// Lets a test stand in the way of one HTTP stack's requests, as an OkHttp
/// interceptor does on Android: `handle` sees each request before it goes
/// out, may wait, fail it, or send it elsewhere. WebSocket upgrades do not
/// come by here.
final class Intercept: URLProtocol, @unchecked Sendable {
    typealias Handler = @Sendable (URLRequest) async throws -> URLRequest

    private static let lock = NSLock()
    nonisolated(unsafe) private static var handlers: [String: Handler] = [:]
    private static let forward = URLSession(configuration: .ephemeral)
    private static let marker = "ra-test-forwarded"

    /// An HTTP stack whose requests to `origin` (http://host:port) pass through `handle`.
    static func http(for origin: String, _ handle: @escaping Handler) -> HTTP {
        lock.withLock { handlers[key(URL(string: origin)!)] = handle }
        let c = HTTP.defaultConfiguration()
        c.protocolClasses = [Intercept.self]
        return HTTP(configuration: c)
    }

    private static func key(_ url: URL) -> String { "\(url.host ?? ""):\(url.port ?? 0)" }

    private static func handler(_ request: URLRequest) -> Handler? {
        guard let url = request.url, url.scheme == "http" || url.scheme == "https" else { return nil }
        return lock.withLock { handlers[key(url)] }
    }

    override class func canInit(with request: URLRequest) -> Bool { handler(request) != nil }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    private var work: Task<Void, Never>?

    override func startLoading() {
        var request = self.request
        // A request's body reaches a URLProtocol as a stream.
        if request.httpBody == nil, let stream = request.httpBodyStream {
            var body = Data()
            stream.open()
            var buf = [UInt8](repeating: 0, count: 8192)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(contentsOf: buf[..<n])
            }
            stream.close()
            request.httpBody = body
        }
        guard let handle = Intercept.handler(request) else { return }
        let outgoing = request
        // URLProtocol's client may be called from any thread.
        let me = Unchecked(self)
        work = Task {
            let client = me.value.client
            do {
                let target = try await handle(outgoing)
                let (data, response) = try await Intercept.forward.data(for: target)
                try Task.checkCancellation()
                client?.urlProtocol(me.value, didReceive: response, cacheStoragePolicy: .notAllowed)
                client?.urlProtocol(me.value, didLoad: data)
                client?.urlProtocolDidFinishLoading(me.value)
            } catch {
                if !Task.isCancelled { client?.urlProtocol(me.value, didFailWithError: error) }
            }
        }
    }

    override func stopLoading() {
        work?.cancel()
    }
}

private struct Unchecked<T>: @unchecked Sendable {
    let value: T
    init(_ value: T) { self.value = value }
}

extension URLRequest {
    /// The same request at another origin.
    func moved(to origin: String) -> URLRequest {
        var r = self
        r.url = URL(string: origin + (url?.path ?? ""))
        return r
    }
}
