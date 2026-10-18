// ConnectRPC clients for the server's services, registering a device, and
// what a failure is called. Matches Android's core/Api.kt.
import Connect
import Foundation
import RAProto
import os

/// A device of the account. `root` and `deviceSeed` are secrets: the app
/// keeps them in the Keychain and never logs them.
public struct Account: Sendable, CustomStringConvertible {
    public let server: String
    public let root: Data
    public let deviceSeed: Data
    public let deviceId: String

    public init(server: String, root: Data, deviceSeed: Data, deviceId: String) {
        self.server = server
        self.root = root
        self.deviceSeed = deviceSeed
        self.deviceId = deviceId
    }

    /// Never the keys, so that they cannot reach a log by accident.
    public var description: String { "Account(server=\(server), device=\(deviceId))" }
}

/// The app's one HTTP stack: requests, RPCs and WebSockets share its
/// connections. It never follows a redirect, so that a login, a registration
/// or a pairing body is not replayed at another URL, and a WebSocket's hello
/// with the token does not go to another host.
public final class HTTP: Sendable {
    let session: URLSession
    private let delegate: SessionDelegate

    /// `configuration` is copied by URLSession; tests add their URLProtocol classes to it.
    public init(configuration: URLSessionConfiguration = HTTP.defaultConfiguration()) {
        let delegate = SessionDelegate()
        self.delegate = delegate
        session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    }

    deinit {
        session.finishTasksAndInvalidate()
    }

    /// The timeouts the protocol expects: Forward waits up to 30 s, so no
    /// answer for 45 s is a dead request. Nothing is cached or remembered
    /// between requests; everything the app keeps goes through its own stores.
    public static func defaultConfiguration() -> URLSessionConfiguration {
        let c = URLSessionConfiguration.ephemeral
        c.timeoutIntervalForRequest = 45
        c.waitsForConnectivity = false
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.urlCache = nil
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.urlCredentialStorage = nil
        return c
    }

    /// One request and its whole answer. A redirect comes back as the answer it is.
    func data(for request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let box = OSAllocatedUnfairLock<URLSessionDataTask?>(initialState: nil)
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation {
                (c: CheckedContinuation<(Data, HTTPURLResponse), Error>) in
                let task = session.dataTask(with: request) { data, response, error in
                    if error == nil, let http = response as? HTTPURLResponse {
                        c.resume(returning: (data ?? Data(), http))
                    } else {
                        c.resume(throwing: error ?? URLError(.badServerResponse))
                    }
                }
                box.withLock { $0 = task }
                task.resume()
                if Task.isCancelled { task.cancel() }
            }
        } onCancel: {
            box.withLock { $0 }?.cancel()
        }
    }

    /// A WebSocket task that reports to `handlers`; the caller resumes it.
    func webSocketTask(_ request: URLRequest, handlers: SocketHandlers) -> URLSessionWebSocketTask {
        let task = session.webSocketTask(with: request)
        delegate.register(task.taskIdentifier, handlers)
        return task
    }
}

/// What the session tells about one WebSocket task, on the session's delegate queue, in order.
struct SocketHandlers: Sendable {
    var opened: @Sendable () -> Void
    var closed: @Sendable (_ code: Int) -> Void
    var completed: @Sendable (_ error: Error?) -> Void
}

private final class SessionDelegate: NSObject, URLSessionWebSocketDelegate, URLSessionDataDelegate,
    Sendable
{
    private let sockets = OSAllocatedUnfairLock<[Int: SocketHandlers]>(initialState: [:])

    func register(_ id: Int, _ handlers: SocketHandlers) {
        sockets.withLock { $0[id] = handlers }
    }

    func urlSession(
        _ session: URLSession, task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void
    ) {
        // Refused: the task ends with the redirect itself as its response.
        completionHandler(nil)
    }

    func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask,
        didOpenWithProtocol protocol: String?
    ) {
        sockets.withLock { $0[webSocketTask.taskIdentifier] }?.opened()
    }

    func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask,
        didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?
    ) {
        sockets.withLock { $0[webSocketTask.taskIdentifier] }?.closed(closeCode.rawValue)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?)
    {
        sockets.withLock { $0.removeValue(forKey: task.taskIdentifier) }?.completed(error)
    }
}

extension HTTP: HTTPClientInterface {
    @discardableResult
    public func unary(
        request: HTTPRequest<Data?>, onMetrics: @escaping @Sendable (HTTPMetrics) -> Void,
        onResponse: @escaping @Sendable (HTTPResponse) -> Void
    ) -> Cancelable {
        var r = URLRequest(url: request.url)
        r.httpMethod = request.method.rawValue
        r.httpBody = request.message
        for (name, values) in request.headers {
            r.setValue(values.joined(separator: ","), forHTTPHeaderField: name)
        }
        let task = session.dataTask(with: r) { data, response, error in
            // An answer cut short is no answer: its status says nothing of what follows.
            if error == nil, let http = response as? HTTPURLResponse {
                var headers = Headers()
                for (k, v) in http.allHeaderFields {
                    guard let name = (k as? String)?.lowercased() else { continue }
                    let value = v as? String ?? String(describing: v)
                    headers[name] = value.components(separatedBy: ",").map {
                        $0.trimmingCharacters(in: .whitespaces)
                    }
                }
                onResponse(
                    HTTPResponse(
                        code: Code.fromHTTPStatus(http.statusCode), headers: headers, message: data,
                        trailers: [:],
                        error: nil, tracingInfo: .init(httpStatus: http.statusCode)))
                return
            }
            // No answer at all. The code says only how it ended, as on
            // Android: whether the request left the device is read from the
            // failure itself (`neverSent`), which travels as the exception.
            let failure = error ?? URLError(.badServerResponse)
            let code: Code
            switch (failure as? URLError)?.code {
            case .timedOut?: code = .deadlineExceeded
            case .cancelled?: code = .canceled
            default: code = .unknown
            }
            onResponse(
                HTTPResponse(
                    code: code, headers: [:], message: nil, trailers: [:],
                    error: ConnectError(
                        code: code, message: failure.localizedDescription, exception: failure),
                    tracingInfo: nil))
        }
        task.resume()
        return Cancelable { task.cancel() }
    }

    /// The server has no streaming RPCs: its streams are WebSockets.
    public func stream(request: HTTPRequest<Data?>, responseCallbacks: ResponseCallbacks)
        -> RequestCallbacks<Data>
    {
        responseCallbacks.receiveClose(
            .unimplemented, [:],
            ConnectError(code: .unimplemented, message: "streaming RPCs are not used"))
        return RequestCallbacks(cancel: {}, sendData: { _ in }, sendClose: {})
    }
}

/// The server's services over the Connect protocol with binary protobuf.
public final class Api: Sendable {
    public let server: String
    public let http: HTTP
    public let auth: PBAuthServiceClient
    public let client: PBClientServiceClient
    public let pairing: PBPairingServiceClient
    let protocolClient: ProtocolClient

    /// Throws for a server that is not an https origin; plain http only for local addresses.
    public init(server: String, http: HTTP) throws {
        self.server = try Secret.checkServer(server)
        self.http = http
        protocolClient = ProtocolClient(
            httpClient: http,
            config: ProtocolClientConfig(
                host: self.server, networkProtocol: .connect, codec: ProtoCodec()))
        auth = PBAuthServiceClient(client: protocolClient)
        client = PBClientServiceClient(client: protocolClient)
        pairing = PBPairingServiceClient(client: protocolClient)
    }

    public static func headers(token: String) -> Headers {
        token.isEmpty ? [:] : ["Authorization": ["Bearer \(token)"]]
    }

    /// The server's address for a WebSocket path.
    func socketURL(_ path: String) -> URL {
        // checkServer left only http and https.
        URL(string: "ws" + server.dropFirst(4) + path)!
    }
}

extension ResponseMessage {
    /// The message, or the error the call ended with.
    public func value() throws -> Output { try result.get() }
}

/// Registers a new device of the account with root key `root` and logs nothing secret.
public func joinWithRoot(
    root: Data, server: String, http: HTTP, setupToken: String = "", kind: PBDeviceKind = .client,
    now: Int64 = Int64(Date().timeIntervalSince1970 * 1000), enrollment: Enrollment? = nil
) async throws -> Account {
    let keys = try Keys.derive(root: root)
    let seed = enrollment?.seed ?? randomBytes(32)
    let device = try SigningKey(seed: seed)
    if let enrollment { try verifyPairApproval(enrollment, root: root, kind: kind, now: now) }
    var reg = PBDeviceRegistration()
    reg.accountPublicKey = keys.account.publicKey
    reg.devicePublicKey = device.publicKey
    reg.kind = kind
    reg.createdMs = now
    let regBytes: Data = try reg.serializedData()
    let api = try Api(server: server, http: http)
    var req = PBRegisterDeviceRequest()
    req.protocolVersion = protocolVersion
    req.registration = regBytes
    req.signature = keys.account.sign(registerMessage(regBytes))
    req.setupToken = setupToken
    if let enrollment {
        req.approval = enrollment.approval
        req.approvalSignature = enrollment.signature
    }
    let res = try await api.auth.registerDevice(request: req).value()
    if res.deviceID != deviceId(publicKey: device.publicKey) {
        throw UserError("server 返回了意外的设备 ID")
    }
    return Account(server: api.server, root: root, deviceSeed: seed, deviceId: res.deviceID)
}

/// Logs in with the device key and returns a bearer token.
public func login(api: Api, account: Account) async throws -> String {
    try await loginResponse(api: api, account: account).token
}

/// Logs in with the device key and returns the token with its expiry.
public func loginResponse(api: Api, account: Account) async throws -> PBLoginResponse {
    var ask = PBGetChallengeRequest()
    ask.deviceID = account.deviceId
    let ch = try await api.auth.getChallenge(request: ask).value()
    var req = PBLoginRequest()
    req.protocolVersion = protocolVersion
    req.deviceID = account.deviceId
    req.challenge = ch.challenge
    req.signature = try SigningKey(seed: account.deviceSeed).sign(loginMessage(ch.challenge))
    return try await api.auth.login(request: req).value()
}

/// A daemon's answer, or a failure to get one. `code` is a Connect code name.
public struct CommandError: Error, LocalizedError, Equatable, Sendable, CustomStringConvertible {
    public let code: String
    public let message: String
    /// The daemon's own answer for this request, the final word on it
    /// (except that it expired before reaching the daemon).
    public let fromDaemon: Bool

    public init(_ code: String, _ message: String, fromDaemon: Bool = false) {
        self.code = code
        self.message = message
        self.fromDaemon = fromDaemon
    }

    /// The command may or may not have run; retrying with the same request ID is safe.
    /// A daemon that stopped halfway through it answers "aborted" ever after.
    public var unknown: Bool {
        code == CommandError.unknownOutcome || (fromDaemon && code == "aborted")
    }
    public var errorDescription: String? { message }
    public var description: String { message }

    public static let unknownOutcome = "unknown_outcome"
    public static let unavailable = "unavailable"
    public static let expired = "expired"
}

/// What kept a request from getting an answer, as far as the system says.
enum NetworkFailure: Equatable {
    /// No address for the server, or no network to ask on: on a phone, nearly always no network.
    case unresolved
    /// The server's address did not take the connection.
    case refused
    case timedOut
    case insecure(String)
}

/// The network failure in or behind `e`, nil for anything else.
func networkFailure(_ e: Error?) -> NetworkFailure? {
    guard let e else { return nil }
    if let ce = e as? ConnectError { return networkFailure(ce.exception) }
    let ns = e as NSError
    if ns.domain == NSURLErrorDomain {
        switch ns.code {
        // URLSession does not look a name up without a network: it says so
        // instead. That is Android's UnknownHostException, and means the same.
        case NSURLErrorCannotFindHost, NSURLErrorDNSLookupFailed, NSURLErrorNotConnectedToInternet,
            NSURLErrorDataNotAllowed, NSURLErrorInternationalRoamingOff:
            return .unresolved
        case NSURLErrorCannotConnectToHost: return .refused
        case NSURLErrorTimedOut: return .timedOut
        case NSURLErrorSecureConnectionFailed, NSURLErrorServerCertificateHasBadDate,
            NSURLErrorServerCertificateUntrusted, NSURLErrorServerCertificateHasUnknownRoot,
            NSURLErrorServerCertificateNotYetValid, NSURLErrorClientCertificateRejected,
            NSURLErrorClientCertificateRequired,
            NSURLErrorAppTransportSecurityRequiresSecureConnection:
            return .insecure(ns.localizedDescription)
        default: return nil
        }
    }
    // A WebSocket's failures come as the socket's own.
    if ns.domain == NSPOSIXErrorDomain {
        switch Int32(ns.code) {
        case ECONNREFUSED, ENETUNREACH, EHOSTUNREACH, ENETDOWN, EHOSTDOWN: return .refused
        case ETIMEDOUT: return .timedOut
        default: return nil
        }
    }
    return nil
}

private func networkText(_ e: Error?) -> String? {
    switch networkFailure(e) {
    case .unresolved?: return "找不到服务器，检查一下网络"
    case .refused?: return "连不上服务器"
    case .timedOut?: return "连接超时"
    case .insecure(let why)?: return "安全连接失败：" + why
    case nil: return nil
    }
}

/// Cocoa's own errors, whose text the system wrote for people. A Swift
/// error bridged to NSError only gets a sentence about its type and a number.
private func isCocoa(_ e: Error) -> Bool {
    type(of: e) is NSError.Type || e is URLError || e is CocoaError
}

/// An error's own words, or its type's name when it has none.
private func ownText(_ e: Error) -> String {
    if let l = e as? LocalizedError, let d = l.errorDescription { return d }
    if isCocoa(e) { return (e as NSError).localizedDescription }
    // A cast would always succeed, by way of NSError: the type itself is asked.
    if type(of: e as Any) is any CustomStringConvertible.Type { return String(describing: e) }
    return String(describing: type(of: e))
}

/// Short text for an error, without content.
public func errorText(_ e: Error) -> String {
    if let c = e as? CommandError { return c.message }
    if let ce = e as? ConnectError {
        // A request that never reached the server carries the network failure
        // as its cause, and that failure's own message as its text.
        if let t = networkText(ce.exception) { return t }
        switch ce.code {
        case .unavailable: return "连不上服务器" + (ce.message.map { "：\($0)" } ?? "")
        case .unauthenticated: return "登录失效：" + (ce.message ?? "")
        default: return ce.message ?? ce.code.name
        }
    }
    return networkText(e) ?? ownText(e)
}

/// The failure as the system worded it, for the details of a lost connection; without content.
public func detailText(_ e: Error) -> String {
    let cause = (e as? ConnectError)?.exception ?? e
    // A message of the app's own has no kind worth naming.
    if cause is UserError { return ownText(cause) }
    let ns = cause as NSError
    let kind: String
    if cause is ConnectError {
        kind = "ConnectError"
    } else if isCocoa(cause) {
        kind = "\(ns.domain) \(ns.code)"
    } else {
        kind = String(describing: type(of: cause))
    }
    let text = ownText(cause)
    return text == kind ? kind : "\(kind): \(text)"
}

/// The server's address did not resolve: on a phone, the network is down whatever the status bar shows.
public func unresolvedHost(_ e: Error) -> Bool { networkFailure(e) == .unresolved }

/// The request did not leave the device: nothing can have run.
func neverSent(_ e: Error?) -> Bool {
    switch networkFailure(e) {
    case .unresolved?, .refused?: return true
    default: return false
    }
}
