// Relay origin validation, HTTP/WebSocket transport stack, and failure descriptions.
// Matches Android's core/Api.kt.
import Foundation
import RAProto
import os

/// A device of the account. Private keys never leave `SecurityStoreV4` / `Vault`.
public struct Account: Equatable, Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let server: String
    public let deviceId: String
    public let accountId: String
    public var localKey: Data { Data((accountId.isEmpty ? deviceId : accountId).utf8) }

    public init(server: String, deviceId: String, accountId: String = "") {
        self.server = server
        self.deviceId = deviceId
        self.accountId = accountId
    }

    public var description: String { "Account(server=\(server), device=\(deviceId))" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: ["server": server, "deviceId": deviceId]) }
}

public typealias Sleeper = @Sendable (_ ms: Int64) async throws -> Void

public let realSleep: Sleeper = { ms in
    let clamped = max(ms, 0)
    let (ns, overflow) = UInt64(clamped).multipliedReportingOverflow(by: 1_000_000)
    try await Task.sleep(nanoseconds: overflow ? UInt64.max : ns)
}

public enum SecurityFailure: Error, Equatable, Sendable {
    case invalid, stale, lease, unavailable, untrusted, conflict, capacity, storage, clock
}

public enum SecurityPurpose: UInt32, Sendable {
    case event = 1, sessionMeta = 2, sessionKey = 3, machineMeta = 4, command = 5, result = 6,
         terminalOutput = 7, terminalInput = 8, notify = 10, image = 11, prefs = 12,
         deviceMeta = 13, terminalResize = 14, terminalHistory = 15
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

/// Checked relay origin and WebSocket URL builder. Unary RPCs flow through Core's byte-pipe `RelayRpcEffect`.
public struct RelayOrigin: Sendable, Equatable {
    public let server: String

    /// Throws for a server that is not an https origin; plain http only for local addresses.
    public init(server: String) throws {
        self.server = try Secret.checkServer(server)
    }

    /// The server's address for a WebSocket path.
    public func socketURL(_ path: String) -> URL {
        // checkServer left only http and https.
        URL(string: "ws" + server.dropFirst(4) + path)!
    }
}

/// Command outcome classification reported back from daemon/command execution.
public enum Outcome: String, Sendable {
    case notSent = "not_sent", rejected, executed, unknown, conflict
}

/// A daemon's answer, or a failure to get one. `code` is a Connect code name.
public struct CommandError: Error, LocalizedError, Equatable, Sendable, CustomStringConvertible {
    public let code: String
    /// Raw local cause for outbox captions, without the unknown-outcome heading.
    public let outcomeDetail: String?
    let localized: LocalizedMessage
    public var message: String { localized.resolve() }
    /// A verified daemon answer, including an explicit unknown result.
    /// Certainty comes from outcome, not authentication alone.
    public let fromDaemon: Bool
    public let readOnly: Bool
    public let outcome: Outcome
    public let conflictKind: String
    public let resultAvailable: Bool

    public init(_ code: String, _ message: String, fromDaemon: Bool = false, readOnly: Bool = false, outcomeDetail: String? = nil, outcome: Outcome? = nil, conflictKind: String = "", resultAvailable: Bool = true) {
        self.code = code
        self.outcomeDetail = outcomeDetail
        self.localized = (outcome == .executed && !resultAvailable) || conflictKind == "identity" ? LocalizedMessage(raw: message) : code == "disk_full" ? L10nMessage.machineStatusDiskFull() : code == "storage_exhausted" ? L10nMessage.machineStatusStorageExhausted() : LocalizedMessage(raw: message)
        self.fromDaemon = fromDaemon
        self.readOnly = readOnly
        self.outcome = outcome ?? (fromDaemon ? .unknown : .notSent)
        self.conflictKind = conflictKind
        self.resultAvailable = resultAvailable
    }

    public init(_ code: String, _ message: LocalizedMessage, fromDaemon: Bool = false, readOnly: Bool = false, outcomeDetail: String? = nil, outcome: Outcome? = nil, conflictKind: String = "", resultAvailable: Bool = true) {
        self.code = code
        self.localized = message
        self.fromDaemon = fromDaemon
        self.readOnly = readOnly
        self.outcome = outcome ?? (fromDaemon ? .unknown : .notSent)
        self.conflictKind = conflictKind
        self.resultAvailable = resultAvailable
        self.outcomeDetail = outcomeDetail
    }

    /// Reconciliation retains the original request for unknown results, missing
    /// bodies and identity conflicts. The explicit outcome remains authoritative.
    public var needsResultRefresh: Bool { conflictKind == "identity" || (outcome == .executed && !resultAvailable) }
    public var unknown: Bool { outcome == .unknown || needsResultRefresh }
    public var canRetryUpload: Bool { outcome == .unknown || (outcome == .notSent && code == "network") }
    public static func == (lhs: CommandError, rhs: CommandError) -> Bool { lhs.localized == rhs.localized && lhs.code == rhs.code && lhs.fromDaemon == rhs.fromDaemon && lhs.readOnly == rhs.readOnly && lhs.outcomeDetail == rhs.outcomeDetail && lhs.outcome == rhs.outcome && lhs.conflictKind == rhs.conflictKind && lhs.resultAvailable == rhs.resultAvailable }
    public var errorDescription: String? { message }
    public var description: String { message }

    public static let unknownOutcome = "unknown_outcome"
    public static let unavailable = "unavailable"
    public static let expired = "expired"
    public static let diskFull = "disk_full"
    public static let internalFailure = "internal"
    public static let canceled = "canceled"
}

public func uncertain(_ error: Error) -> Bool { (error as? CommandError)?.unknown ?? false }

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
    case .unresolved?: return L10n.errorsIosCannotFindTheServerCheckYourNetwork()
    case .refused?: return L10n.connectionNoServer()
    case .timedOut?: return L10n.errorsConnectionTimedOut()
    case .insecure(let why)?: return L10n.errorsIosSecureConnectionFailed(value: String(describing: why))
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
    if let f = e as? SecurityFailure {
        switch f {
        case .lease: return L10n.errorsIosRemoteControlExpiredSavingChangesSessionControlAndNew()
        case .unavailable: return L10n.errorsWaitingForTheOwningComputerToUpdateTheCurrentRemote()
        case .invalid, .untrusted: return L10n.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed()
        case .conflict: return L10n.errorsMembershipChainConflictSecurityOperationsStoppedVerifyDevicesAnd()
        default: break
        }
    }
    if let sw = e as? SecurityWorkflowError { return sw.message }
    if let c = e as? CommandError { return c.message }
    return networkText(e) ?? ownText(e)
}

/// The failure as the system worded it, for the details of a lost connection; without content.
public func detailText(_ e: Error) -> String {
    // A message of the app's own has no kind worth naming.
    if e is UserError { return ownText(e) }
    let ns = e as NSError
    let kind: String
    if isCocoa(e) {
        kind = ns.domain + " " + String(ns.code)
    } else {
        kind = String(describing: type(of: e))
    }
    let text = ownText(e)
    return text == kind ? kind : "\(kind): \(text)"
}

/// The server's address did not resolve: on a phone, the network is down whatever the status bar shows.
public func unresolvedHost(_ e: Error) -> Bool { networkFailure(e) == .unresolved }
